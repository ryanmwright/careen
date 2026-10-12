package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/provision"
)

var _ provision.Restarter = (*Proxmox)(nil)

// PendingRestart lists the VM settings Proxmox holds back until the VM is
// restarted from outside, such as an OpenTofu change applied with
// reboot_after_update off. It asks the VM's node over SSH, like template
// build, so it needs no API token.
func (p *Proxmox) PendingRestart(ctx context.Context, h inventory.Host) ([]string, error) {
	vm, node, err := p.vmNode(ctx, h)
	if err != nil {
		return nil, err
	}
	out, err := p.ssh(ctx, node.Address, nil, fmt.Sprintf("pvesh get /nodes/%s/qemu/%d/pending --output-format json", vm.Node, vm.VMID))
	if err != nil {
		return nil, fmt.Errorf("reading VM %d's pending changes: %w", vm.VMID, err)
	}
	return parsePending(out)
}

// Restart runs qm reboot on the VM's node: a shutdown and a fresh start,
// which applies the pending changes. careen then waits for the host to
// come back as after any reboot.
func (p *Proxmox) Restart(ctx context.Context, h inventory.Host) error {
	vm, node, err := p.vmNode(ctx, h)
	if err != nil {
		return err
	}
	if _, err := p.ssh(ctx, node.Address, nil, fmt.Sprintf("qm reboot %d", vm.VMID)); err != nil {
		return fmt.Errorf("restarting VM %d: %w", vm.VMID, err)
	}
	return nil
}

// vmNode is h's VM and the Proxmox node it lives on.
func (p *Proxmox) vmNode(ctx context.Context, h inventory.Host) (VM, Node, error) {
	cfg, err := p.Config(ctx)
	if err != nil {
		return VM{}, Node{}, err
	}
	vm, ok := cfg.Hosts[h.Name]
	if !ok {
		return VM{}, Node{}, fmt.Errorf("%s has no VM in the flake's pve output (pve.hosts.%s)", h.Name, h.Name)
	}
	node, ok := cfg.Nodes[vm.Node]
	if !ok {
		return VM{}, Node{}, fmt.Errorf("%s: Proxmox node %q isn't in the flake's pve.nodes", h.Name, vm.Node)
	}
	return vm, node, nil
}

// parsePending reads `pvesh get .../pending`: every setting, with a
// "pending" value when a new one waits and a non-zero "delete" when its
// removal does.
func parsePending(out []byte) ([]string, error) {
	var entries []struct {
		Key     string          `json:"key"`
		Pending json.RawMessage `json:"pending"`
		Delete  json.RawMessage `json:"delete"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parsing the VM's pending changes: %w", err)
	}
	set := func(r json.RawMessage) bool {
		s := string(r)
		return s != "" && s != "null" && s != "0" && s != `"0"`
	}
	keys := []string{}
	for _, e := range entries {
		if set(e.Pending) || set(e.Delete) {
			keys = append(keys, e.Key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}
