// Package proxmox is careen's Proxmox provisioner. The flake defines each
// VM (`nix eval .#pve`) and renders it to
// OpenTofu JSON (`.#pve-tofu`); this package runs the flake's own OpenTofu
// (`.#opentofu`, which bundles the bpg/proxmox provider) against it, so
// careen never talks to the Proxmox API to change hardware. It does use the
// API to wait for a VM's QEMU guest agent and read the VM's host key.
//
// OpenTofu's state lives in Dir on the machine careen runs on (TODO: a
// MinIO backend). The API token comes from the flake's sops file through
// Token and is only ever passed to tofu in its environment.
//
// It imports nothing from careen's deploy or sops code.
package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/hostkey"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/provision"
)

// Config is the flake's `pve` output.
type Config struct {
	Endpoint     string          `json:"endpoint"`
	Storage      string          `json:"storage"`
	TemplateHost string          `json:"templateHost"`
	Nodes        map[string]Node `json:"nodes"`
	Token        struct {
		File string `json:"file"`
		Key  string `json:"key"`
	} `json:"token"`
	Hosts map[string]VM `json:"hosts"`
}

// Node is one Proxmox cluster node.
type Node struct {
	Address  string `json:"address"`  // SSH destination (as root)
	Template int    `json:"template"` // VM ID of its template (from pve.templateHost)
}

// VM is the part of a host's pve entry careen needs; the rest only
// matters to the rendered OpenTofu config.
type VM struct {
	Node string `json:"node"`
	VMID int    `json:"vmid"`
}

// Cmd runs a program in dir with extra environment and optional stdin,
// returning its stdout. Tests replace it.
type Cmd func(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error)

// RunCmd is the real Cmd.
func RunCmd(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	c.Stdin = stdin
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, firstArg(args), err, execx.Tail(stderr.String(), 6))
	}
	return out, nil
}

func firstArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// Proxmox provisions hosts as VMs on a Proxmox cluster.
type Proxmox struct {
	Flake inventory.Flake
	// Dir holds the rendered config and OpenTofu's state.
	Dir string
	Run execx.Runner // nix
	Cmd Cmd          // tofu, ssh
	// Token returns the API token (user@realm!id=uuid) given the token's
	// sops file (absolute) and key.
	Token func(ctx context.Context, file, key string) (string, error)
	// API waits for guest agents; NewAPI when nil.
	API func(endpoint, token string) *API
	// Poll and AgentTimeout bound waiting for a guest agent.
	Poll, AgentTimeout time.Duration
	// Scan reads the key a machine's sshd serves (agekey.ScanLine when
	// nil), to know the key read through the agent is the one in use.
	Scan func(ctx context.Context, address string, port int) (string, error)
	// StuckAfter is how long sshd may serve another key than the VM
	// reports before careen reboots the VM once (default a minute).
	StuckAfter time.Duration

	once  sync.Once
	err   error
	cfg   *Config
	tofu  string
	token string
}

const planFile = "careen.tfplan"

func (p *Proxmox) Name() string { return inventory.Proxmox }

// Config reads the flake's pve inventory.
func (p *Proxmox) Config(ctx context.Context) (*Config, error) {
	if p.cfg != nil {
		return p.cfg, nil
	}
	out, err := p.Run(ctx, "nix", "eval", "--json", p.Flake.BuildRef()+"#pve")
	if err != nil {
		return nil, fmt.Errorf("reading the flake's pve inventory: %w", err)
	}
	var c Config
	if err := json.Unmarshal(out, &c); err != nil {
		return nil, fmt.Errorf("parsing the flake's pve inventory: %w", err)
	}
	if c.TemplateHost == "" {
		c.TemplateHost = "pve-base"
	}
	p.cfg = &c
	return p.cfg, nil
}

// prepare renders the config into Dir and initializes OpenTofu, once.
func (p *Proxmox) prepare(ctx context.Context) error {
	p.once.Do(func() { p.err = p.doPrepare(ctx) })
	return p.err
}

func (p *Proxmox) doPrepare(ctx context.Context) error {
	if _, err := p.Config(ctx); err != nil {
		return err
	}
	ref := p.Flake.BuildRef()
	out, err := p.Run(ctx, "nix", "build", "--no-link", "--print-out-paths", ref+"#pve-tofu", ref+"#opentofu")
	if err != nil {
		return fmt.Errorf("building the flake's pve-tofu and opentofu: %w", err)
	}
	paths := strings.Fields(string(out))
	if len(paths) != 2 {
		return fmt.Errorf("nix build printed %q, want the config and opentofu", out)
	}
	tfjson, err := os.ReadFile(paths[0])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(p.Dir, "main.tf.json"), tfjson); err != nil {
		return err
	}
	override, err := noRebootOverride(tfjson)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(p.Dir, overrideFile), override); err != nil {
		return err
	}
	p.tofu = filepath.Join(paths[1], "bin", "tofu")
	if _, err := p.tf(ctx, false, "init", "-input=false"); err != nil {
		return err
	}
	return nil
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// tf runs tofu in Dir; withToken passes the API token.
func (p *Proxmox) tf(ctx context.Context, withToken bool, args ...string) ([]byte, error) {
	env := []string{"TF_IN_AUTOMATION=1"}
	if withToken {
		tok, err := p.apiToken(ctx)
		if err != nil {
			return nil, err
		}
		env = append(env, "PROXMOX_VE_API_TOKEN="+tok)
	}
	return p.Cmd(ctx, p.Dir, env, nil, p.tofu, append(append([]string{}, args...), "-no-color")...)
}

func (p *Proxmox) apiToken(ctx context.Context) (string, error) {
	if p.token != "" {
		return p.token, nil
	}
	c, err := p.Config(ctx)
	if err != nil {
		return "", err
	}
	tok, err := p.Token(ctx, filepath.Join(p.Flake.Path, c.Token.File), c.Token.Key)
	if err != nil {
		return "", fmt.Errorf("decrypting the Proxmox API token (%s): %w", c.Token.File, err)
	}
	p.token = strings.TrimSpace(tok)
	return p.token, nil
}

// targets are the -target flags for hosts; every host must have a VM entry.
func (p *Proxmox) targets(hosts []inventory.Host) ([]string, error) {
	var args []string
	for _, h := range hosts {
		if _, ok := p.cfg.Hosts[h.Name]; !ok {
			return nil, fmt.Errorf("%s has no VM in the flake's pve output (pve.hosts.%s)", h.Name, h.Name)
		}
		args = append(args, "-target="+Resource+"."+h.Name)
	}
	return args, nil
}

func (p *Proxmox) plan(ctx context.Context, hosts []inventory.Host, destroy bool) (provision.Plan, error) {
	if err := p.prepare(ctx); err != nil {
		return provision.Plan{}, err
	}
	targets, err := p.targets(hosts)
	if err != nil {
		return provision.Plan{}, err
	}
	args := []string{"plan", "-input=false", "-out=" + planFile}
	if destroy {
		args = append(args, "-destroy")
	}
	if _, err := p.tf(ctx, true, append(args, targets...)...); err != nil {
		return provision.Plan{}, err
	}
	out, err := p.tf(ctx, false, "show", "-json", planFile)
	if err != nil {
		return provision.Plan{}, err
	}
	pl, err := ParsePlan(out)
	if err != nil {
		return pl, err
	}
	if err := CheckDeletes(out, p.cfg.Hosts); err != nil {
		return pl, err
	}
	// Hosts the plan doesn't mention are already as they should be.
	for _, h := range hosts {
		if !slices.ContainsFunc(pl.Changes, func(c provision.Change) bool { return c.Host == h.Name }) {
			pl.Changes = append(pl.Changes, provision.Change{Host: h.Name, Action: provision.NoOp})
		}
	}
	return pl, nil
}

func (p *Proxmox) Plan(ctx context.Context, hosts []inventory.Host, destroy bool) (provision.Plan, error) {
	return p.plan(ctx, hosts, destroy)
}

// apply plans and, if the plan changes anything, applies it.
func (p *Proxmox) apply(ctx context.Context, hosts []inventory.Host, destroy bool, progress provision.Progress) (provision.Plan, error) {
	pl, err := p.plan(ctx, hosts, destroy)
	if err != nil {
		return pl, err
	}
	for _, c := range pl.Changes {
		if c.Action != provision.NoOp {
			progress(c.Host, "provision", c.Action+" VM")
		}
	}
	if !pl.Pending() {
		return pl, nil
	}
	if _, err := p.tf(ctx, true, "apply", "-input=false", planFile); err != nil {
		return pl, err
	}
	return pl, nil
}

func (p *Proxmox) Apply(ctx context.Context, hosts []inventory.Host, progress provision.Progress) (provision.Plan, error) {
	return p.apply(ctx, hosts, false, progress)
}

func (p *Proxmox) Destroy(ctx context.Context, hosts []inventory.Host, progress provision.Progress) (provision.Plan, error) {
	return p.apply(ctx, hosts, true, progress)
}

// ErrReplace is returned when bringing a host up would replace its VM.
var ErrReplace = errors.New("the plan replaces the VM, destroying its disks")

func (p *Proxmox) Ensure(ctx context.Context, h inventory.Host, progress provision.Progress) (provision.Machine, error) {
	pl, err := p.plan(ctx, []inventory.Host{h}, false)
	if err != nil {
		return provision.Machine{}, err
	}
	created := false
	for _, c := range pl.Changes {
		switch c.Action {
		case provision.Replace:
			return provision.Machine{}, fmt.Errorf("%s: %w; run careen down %s first if that's intended", h.Name, ErrReplace, h.Name)
		case provision.Create:
			created = true
		}
	}
	if pl.Pending() {
		for _, c := range pl.Changes {
			progress(c.Host, "provision", c.Action+" VM")
		}
		if _, err := p.tf(ctx, true, "apply", "-input=false", planFile); err != nil {
			return provision.Machine{}, err
		}
	} else {
		progress(h.Name, "provision", "VM is up to date")
	}

	vm := p.cfg.Hosts[h.Name]
	tok, err := p.apiToken(ctx)
	if err != nil {
		return provision.Machine{}, err
	}
	newAPI := p.API
	if newAPI == nil {
		newAPI = NewAPI
	}
	progress(h.Name, "provision", fmt.Sprintf("waiting for VM %d's guest agent", vm.VMID))
	api := newAPI(p.cfg.Endpoint, tok)
	key, err := api.WaitHostKey(ctx, vm.Node, vm.VMID, p.Poll, p.AgentTimeout)
	if err != nil {
		return provision.Machine{}, fmt.Errorf("%s: %w", h.Name, err)
	}
	key, err = p.settledKey(ctx, api, h, vm, key, progress)
	if err != nil {
		return provision.Machine{}, fmt.Errorf("%s: %w", h.Name, err)
	}
	return provision.Machine{Address: h.Address, BootHostKey: key, Created: created}, nil
}

// settledKey waits until the key read through the guest agent is the one
// sshd serves. A fresh clone can still change its keys on first boot
// (cloud-init's ssh module regenerates them after sshd made its own), so
// the first key read may be gone by the time careen connects. The key is
// always taken from inside the VM; ssh-keyscan only says when it's in use.
func (p *Proxmox) settledKey(ctx context.Context, api *API, h inventory.Host, vm VM, key string, progress provision.Progress) (string, error) {
	scan := p.Scan
	if scan == nil {
		scan = agekey.ScanLine
	}
	poll, timeout := p.Poll, p.AgentTimeout
	if poll <= 0 {
		poll = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	// sshd that loaded its keys before they were replaced keeps serving
	// the old one until it restarts. After this long, reboot the VM once.
	stuck := p.StuckAfter
	if stuck <= 0 {
		stuck = time.Minute
	}
	since, rebooted := time.Time{}, false
	said := false
	for {
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		live, err := scan(sctx, h.Address, 0)
		cancel()
		if err == nil && hostkey.SameKey(live, key) {
			return key, nil
		}
		if err == nil && !rebooted { // sshd answers, with another key
			if since.IsZero() {
				since = time.Now()
			} else if time.Since(since) > stuck {
				progress(h.Name, "provision", "sshd still serves a key the VM no longer has (it loaded its keys before they were replaced); rebooting the VM once")
				if err := api.Reboot(ctx, vm.Node, vm.VMID); err != nil {
					return "", fmt.Errorf("rebooting VM %d: %w", vm.VMID, err)
				}
				rebooted = true
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return "", fmt.Errorf("sshd on %s never answered: %v", h.Address, err)
			}
			return "", fmt.Errorf("sshd on %s serves a different key than the VM reports (%s); is something else on that address?", h.Address, key)
		}
		if !said {
			progress(h.Name, "provision", "waiting for sshd to serve the key the VM reports")
			said = true
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
		if k, err := api.WaitHostKey(ctx, vm.Node, vm.VMID, poll, timeout); err == nil {
			key = k
		}
	}
}

// overrideFile is an OpenTofu override file: it's merged after main.tf.json
// and its values win.
const overrideFile = "careen_override.tf.json"

// noRebootOverride sets reboot_after_update = false on every VM in the
// rendered config, whatever the flake set. careen restarts VMs itself, one
// at a time and per rebootPolicy (qm reboot for pending hardware changes);
// the provider rebooting running VMs during an apply would bypass all of
// that. An override must name existing resources, so they're read from the
// config rather than the inventory.
func noRebootOverride(tfjson []byte) ([]byte, error) {
	var cfg struct {
		Resource map[string]map[string]json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(tfjson, &cfg); err != nil {
		return nil, fmt.Errorf("reading the rendered OpenTofu config: %w", err)
	}
	vms := map[string]any{}
	for name := range cfg.Resource[Resource] {
		vms[name] = map[string]any{"reboot_after_update": false}
	}
	if len(vms) == 0 {
		return []byte("{}\n"), nil
	}
	return json.MarshalIndent(map[string]any{"resource": map[string]any{Resource: vms}}, "", "  ")
}
