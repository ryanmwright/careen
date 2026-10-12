package proxmox

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Template outcomes.
const (
	TemplateCreated  = "created"
	TemplateReplaced = "replaced"
	TemplateKept     = "kept"
)

// TemplateResult is one node's template after BuildTemplates.
type TemplateResult struct {
	Node   string `json:"node"`
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// BuildTemplates builds the template host's Proxmox image from the flake
// once and installs it as each node's template (all nodes when nodes is
// empty). A node that already has its template keeps it unless replace;
// clones are full copies, so replacing a template never touches existing
// VMs.
func (p *Proxmox) BuildTemplates(ctx context.Context, nodes []string, replace bool, progress func(node, step, msg string)) ([]TemplateResult, error) {
	c, err := p.Config(ctx)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		nodes = slices.Sorted(maps.Keys(c.Nodes))
	}
	for _, n := range nodes {
		if _, ok := c.Nodes[n]; !ok {
			return nil, fmt.Errorf("unknown Proxmox node %q (known: %s)", n, strings.Join(slices.Sorted(maps.Keys(c.Nodes)), ", "))
		}
	}

	attr := p.Flake.BuildRef() + "#nixosConfigurations." + c.TemplateHost + ".config.system.build.images.proxmox"
	progress("", "build", "building "+c.TemplateHost+"'s Proxmox image")
	out, err := p.Run(ctx, "nix", "build", "--no-link", "--print-out-paths", attr)
	if err != nil {
		return nil, fmt.Errorf("building %s's image: %w", c.TemplateHost, err)
	}
	image, err := findVMA(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	name := c.TemplateHost + "-" + shortRev(p.Flake.Revision, p.Flake.Dirty)

	var results []TemplateResult
	for _, n := range nodes {
		node := c.Nodes[n]
		res := TemplateResult{Node: n, VMID: node.Template, Name: name}
		exists, err := p.vmExists(ctx, node.Address, node.Template)
		if err != nil {
			return results, fmt.Errorf("%s: %w", n, err)
		}
		switch {
		case exists && !replace:
			progress(n, "template", fmt.Sprintf("VM %d exists; keeping it (--replace rebuilds it)", node.Template))
			res.Status = TemplateKept
			results = append(results, res)
			continue
		case exists:
			progress(n, "template", fmt.Sprintf("destroying the old template %d", node.Template))
			if _, err := p.ssh(ctx, node.Address, nil, fmt.Sprintf("qm destroy %d", node.Template)); err != nil {
				return results, fmt.Errorf("%s: %w", n, err)
			}
			res.Status = TemplateReplaced
		default:
			res.Status = TemplateCreated
		}
		progress(n, "template", fmt.Sprintf("restoring the image as template %d (%s)", node.Template, name))
		f, err := os.Open(image)
		if err != nil {
			return results, err
		}
		_, err = p.ssh(ctx, node.Address, f, fmt.Sprintf(
			"zstd -d | qmrestore - %d --storage %s --unique 1 && qm set %d --name %s && qm template %d",
			node.Template, c.Storage, node.Template, name, node.Template))
		f.Close()
		if err != nil {
			return results, fmt.Errorf("%s: %w", n, err)
		}
		results = append(results, res)
	}
	return results, nil
}

func (p *Proxmox) vmExists(ctx context.Context, address string, vmid int) (bool, error) {
	out, err := p.ssh(ctx, address, nil, fmt.Sprintf("if qm status %d >/dev/null 2>&1; then echo yes; else echo no; fi", vmid))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}

// ssh runs cmd as root on a Proxmox node.
func (p *Proxmox) ssh(ctx context.Context, address string, stdin *os.File, cmd string) ([]byte, error) {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-l", "root", "--", address, cmd}
	if stdin == nil {
		return p.Cmd(ctx, "", nil, nil, "ssh", args...)
	}
	return p.Cmd(ctx, "", nil, stdin, "ssh", args...)
}

func findVMA(dir string) (string, error) {
	m, _ := filepath.Glob(filepath.Join(dir, "*.vma.zst"))
	if len(m) == 0 {
		return "", fmt.Errorf("no .vma.zst in %s", dir)
	}
	return m[0], nil
}

func shortRev(rev string, dirty bool) string {
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev == "" {
		rev = "unknown"
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
