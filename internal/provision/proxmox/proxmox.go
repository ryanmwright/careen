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
	"net/http"
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
	"github.com/ryanmwright/careen/internal/sshx"
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
	// GuestAgent says whether the host's own NixOS config runs the QEMU
	// guest agent. Nil when the flake doesn't say; careen then reads the
	// host's services.qemuGuest.enable (see hasAgent).
	GuestAgent *bool `json:"guestAgent"`
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

// TimedCmd is c recording how long each call takes in t. A call is named by
// its program and arguments, except ssh, whose arguments are long and
// mostly options.
func TimedCmd(t *execx.Timings, c Cmd) Cmd {
	return func(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
		start := time.Now()
		out, err := c(ctx, dir, env, stdin, name, args...)
		what := filepath.Base(name)
		if what != "ssh" {
			what += " " + strings.Join(args, " ")
		}
		t.Add(what, time.Since(start))
		return out, err
	}
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
	// PlanNotice is how often a running tofu plan says it still runs
	// (waitNotice when zero).
	PlanNotice time.Duration
	// Notice, when set, is where Plan, which has no progress to report
	// through, says what it is doing.
	Notice io.Writer
	// Scan reads the key a machine's sshd serves (agekey.ScanLine when
	// nil), to know the key read through the agent is the one in use.
	Scan func(ctx context.Context, address string, port int) (string, error)
	// StuckAfter is how long sshd may serve another key than the VM
	// reports before careen reboots the VM once (default a minute).
	StuckAfter time.Duration
	// NoAgentWait skips waiting for the guest agent of a VM that already
	// exists; its host key is then checked over SSH instead.
	NoAgentWait bool
	// TrustedKeys lists the keys careen already trusts for h, from outside
	// the machine (its pre-generated key in the repo, its known_hosts
	// entry). Without the guest agent, sshd's key must be one of them.
	TrustedKeys func(h inventory.Host) []string

	once   sync.Once
	err    error
	cfg    *Config
	tofu   string
	token  string
	tfjson []byte

	agentMu sync.Mutex
	agents  map[string]bool
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
	p.tfjson = tfjson
	if err := p.writeOverride(nil); err != nil {
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

// plan runs tofu plan for hosts, telling say what it is doing.
func (p *Proxmox) plan(ctx context.Context, hosts []inventory.Host, destroy bool, say func(string)) (provision.Plan, error) {
	say("planning with OpenTofu")
	if err := p.prepare(ctx); err != nil {
		return provision.Plan{}, err
	}
	targets, err := p.targets(hosts)
	if err != nil {
		return provision.Plan{}, err
	}
	var agentless []string
	for _, h := range hosts {
		if !p.hasAgent(ctx, h) {
			agentless = append(agentless, h.Name)
		}
	}
	if err := p.writeOverride(agentless); err != nil {
		return provision.Plan{}, err
	}
	args := []string{"plan", "-input=false", "-out=" + planFile}
	if destroy {
		args = append(args, "-destroy")
	}
	stop := p.stillPlanning(say)
	_, err = p.tf(ctx, true, append(args, targets...)...)
	stop()
	if err != nil {
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

// stillPlanning tells say every PlanNotice that tofu plan is still running,
// until the returned stop is called. The first notice names the usual
// cause: the provider reads each VM's IPs from its guest agent and, when the
// agent doesn't answer, waits out the agent timeout in pve-tofu.
func (p *Proxmox) stillPlanning(say func(string)) (stop func()) {
	every := p.PlanNotice
	if every <= 0 {
		every = waitNotice
	}
	start := time.Now()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		for first := true; ; first = false {
			select {
			case <-done:
				return
			case <-t.C:
			}
			msg := fmt.Sprintf("tofu plan still running (%s)", time.Since(start).Round(time.Second))
			if first {
				msg += "; a VM whose guest agent doesn't answer holds it for the agent timeout pve-tofu sets (see `careen up --timings`)"
			}
			say(msg)
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// notice is say for Plan: lines on Notice, naming the hosts.
func (p *Proxmox) notice(hosts []inventory.Host) func(string) {
	if p.Notice == nil {
		return func(string) {}
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	return func(msg string) { fmt.Fprintf(p.Notice, "    %s: %s\n", strings.Join(names, ", "), msg) }
}

// each is say for Apply and Destroy: progress for every host.
func each(hosts []inventory.Host, progress provision.Progress) func(string) {
	return func(msg string) {
		for _, h := range hosts {
			progress(h.Name, "provision", msg)
		}
	}
}

func (p *Proxmox) Plan(ctx context.Context, hosts []inventory.Host, destroy bool) (provision.Plan, error) {
	return p.plan(ctx, hosts, destroy, p.notice(hosts))
}

// apply plans and, if the plan changes anything, applies it.
func (p *Proxmox) apply(ctx context.Context, hosts []inventory.Host, destroy bool, progress provision.Progress) (provision.Plan, error) {
	pl, err := p.plan(ctx, hosts, destroy, each(hosts, progress))
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
	start := time.Now()
	pl, err := p.plan(ctx, []inventory.Host{h}, false, func(msg string) { progress(h.Name, "provision", msg) })
	if err != nil {
		return provision.Machine{}, err
	}
	progress(h.Name, "provision", fmt.Sprintf("planned in %s", time.Since(start).Round(100*time.Millisecond)))
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
	if !created {
		switch {
		case p.NoAgentWait:
			progress(h.Name, "provision", "not waiting for the guest agent (--no-agent-wait); checking its SSH key against the repo and known_hosts")
			return p.overSSH(ctx, h)
		case !p.hasAgent(ctx, h):
			progress(h.Name, "provision", h.Name+"'s config doesn't run the QEMU guest agent; checking its SSH key against the repo and known_hosts")
			return p.overSSH(ctx, h)
		}
	}
	// A new VM boots the template, which has an agent whatever the host's
	// config says, and its first boot is slow; an existing one answers
	// quickly or its agent is gone.
	timeout := p.AgentTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
		if !created {
			timeout = time.Minute
		}
	}
	tok, err := p.apiToken(ctx)
	if err != nil {
		return provision.Machine{}, err
	}
	newAPI := p.API
	if newAPI == nil {
		newAPI = NewAPI
	}
	api := newAPI(p.cfg.Endpoint, tok)
	api.OnWait = func(left time.Duration, last error) {
		progress(h.Name, "provision", fmt.Sprintf("waiting for VM %d's guest agent to report its host key (%s left): %s", vm.VMID, left.Round(time.Second), execx.Tail(last.Error(), 1)))
	}
	key, err := api.WaitHostKey(ctx, vm.Node, vm.VMID, p.Poll, timeout)
	if err != nil {
		var se *StatusError
		if created || ctx.Err() != nil || (errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden)) {
			return provision.Machine{}, fmt.Errorf("%s: %w", h.Name, err)
		}
		return p.agentSilent(ctx, api, h, vm, err, progress)
	}
	key, err = p.settledKey(ctx, api, h, vm, key, progress)
	if err != nil {
		return provision.Machine{}, fmt.Errorf("%s: %w", h.Name, err)
	}
	return provision.Machine{Address: h.Address, BootHostKey: key, Created: created}, nil
}

// overSSH is Ensure for an existing VM without its guest agent: the key
// sshd serves must be one careen already trusts (see verifiedKey), and it
// stands in for the key the agent would have read.
func (p *Proxmox) overSSH(ctx context.Context, h inventory.Host) (provision.Machine, error) {
	key, err := p.verifiedKey(ctx, h)
	if err != nil {
		return provision.Machine{}, fmt.Errorf("%s: no guest agent to ask, and %w", h.Name, err)
	}
	return provision.Machine{Address: h.Address, BootHostKey: key}, nil
}

// agentSilent handles an existing VM whose agent didn't answer in time,
// finding out why over SSH. A deployed system without the agent unit is the
// case the next deploy fixes, so it continues; a stopped unit is started
// (it is device-triggered and may not start after a switch).
func (p *Proxmox) agentSilent(ctx context.Context, api *API, h inventory.Host, vm VM, waitErr error, progress provision.Progress) (provision.Machine, error) {
	progress(h.Name, "provision", fmt.Sprintf("VM %d's guest agent didn't answer; checking %s over SSH", vm.VMID, h.Address))
	key, err := p.verifiedKey(ctx, h)
	if err != nil {
		return provision.Machine{}, fmt.Errorf("%s: %v, and %w", h.Name, waitErr, err)
	}
	m := provision.Machine{Address: h.Address, BootHostKey: key}
	const probe = `if systemctl cat qemu-guest-agent.service >/dev/null 2>&1; then systemctl is-active qemu-guest-agent.service; else echo missing; fi; true`
	out, err := p.sshPinned(ctx, h, key, "sh -c "+sshx.Quote(probe))
	if err != nil {
		return provision.Machine{}, fmt.Errorf("%s: %v, and checking its agent over SSH failed: %w", h.Name, waitErr, err)
	}
	switch state := strings.TrimSpace(string(out)); state {
	case "missing":
		progress(h.Name, "provision", fmt.Sprintf("warning: the running system has no guest agent, though %s's config has one (it was deployed from an older commit); continuing over SSH, and the deploy brings the agent back", h.Name))
		return m, nil
	case "active":
		return provision.Machine{}, fmt.Errorf("%s: the guest agent runs in the VM but Proxmox can't reach it (is the agent enabled on VM %d?): %v", h.Name, vm.VMID, waitErr)
	default:
		progress(h.Name, "provision", fmt.Sprintf("the guest agent is %s; starting it", state))
		if _, err := p.sshPinned(ctx, h, key, "sudo systemctl start qemu-guest-agent.service"); err != nil {
			return provision.Machine{}, fmt.Errorf("%s: starting its guest agent: %w", h.Name, err)
		}
		after := 30 * time.Second
		if p.AgentTimeout > 0 && p.AgentTimeout < after {
			after = p.AgentTimeout
		}
		if k, err := api.WaitHostKey(ctx, vm.Node, vm.VMID, p.Poll, after); err == nil {
			m.BootHostKey = k
		} else {
			progress(h.Name, "provision", "warning: the guest agent was started but Proxmox still can't reach it; continuing over SSH")
		}
		return m, nil
	}
}

// verifiedKey reads the key h's sshd serves and returns it if it is one
// careen trusts without the guest agent (TrustedKeys). Anything else fails:
// accepting whatever answers would be trust on first use.
func (p *Proxmox) verifiedKey(ctx context.Context, h inventory.Host) (string, error) {
	scan := p.Scan
	if scan == nil {
		scan = agekey.ScanLine
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	live, err := scan(sctx, h.Address, 0)
	if err != nil {
		return "", fmt.Errorf("SSH on %s is unreachable: %w", h.Address, err)
	}
	if p.TrustedKeys != nil {
		for _, k := range p.TrustedKeys(h) {
			if hostkey.SameKey(live, k) {
				bare, err := hostkey.Bare(live)
				if err != nil {
					return "", err
				}
				return bare, nil
			}
		}
	}
	return "", fmt.Errorf("can't verify the SSH key %s serves: it is neither %s's pre-generated key in the repo nor its entry in known_hosts; start the guest agent in the VM, or put its key in known_hosts if you know it", h.Address, h.Name)
}

// sshPinned runs a command on h, over a connection pinned to
// key alone.
func (p *Proxmox) sshPinned(ctx context.Context, h inventory.Host, key, remote string) ([]byte, error) {
	bare, err := hostkey.Bare(key)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "careen-known-hosts-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	fmt.Fprintf(tmp, "%s %s\n", h.Address, bare)
	tmp.Close()
	r := sshx.Remote{User: h.User, Address: h.Address, NoMaster: true, ConnectTimeout: 15 * time.Second, Args: []string{
		"-o", "UserKnownHostsFile=" + tmp.Name(),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes",
	}}
	return p.Cmd(ctx, "", nil, nil, "ssh", r.SSHArgs(remote)...)
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

// hasAgent says whether h's own NixOS config runs the QEMU guest agent: its
// pve entry's guestAgent, else the host's services.qemuGuest.enable (an
// eval of the locked flake, which the runner caches). If neither answers
// careen assumes it does, and an agent that doesn't answer is found out by
// waiting for it.
func (p *Proxmox) hasAgent(ctx context.Context, h inventory.Host) bool {
	if vm, ok := p.cfg.Hosts[h.Name]; ok && vm.GuestAgent != nil {
		return *vm.GuestAgent
	}
	p.agentMu.Lock()
	defer p.agentMu.Unlock()
	if v, ok := p.agents[h.Name]; ok {
		return v
	}
	v := true
	out, err := p.Run(ctx, "nix", "eval", "--json", p.Flake.BuildRef()+"#nixosConfigurations."+h.Name+".config.services.qemuGuest.enable")
	if err == nil {
		var b bool
		if json.Unmarshal(out, &b) == nil {
			v = b
		}
	}
	if p.agents == nil {
		p.agents = map[string]bool{}
	}
	p.agents[h.Name] = v
	return v
}

// overrideFile is an OpenTofu override file: it's merged after main.tf.json
// and its values win.
const overrideFile = "careen_override.tf.json"

// writeOverride writes the override file for a plan of hosts, agentless
// naming those whose config doesn't run the guest agent.
func (p *Proxmox) writeOverride(agentless []string) error {
	b, err := override(p.tfjson, agentless)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(p.Dir, overrideFile), b)
}

// override sets reboot_after_update = false on every VM in the rendered
// config, whatever the flake set. careen restarts VMs itself, one at a time
// and per rebootPolicy (see Restart); the provider rebooting running VMs
// during an apply would bypass all of that.
//
// For the agentless VMs whose agent block is enabled it also shortens
// agent.timeout to agentlessTimeout. The provider reads each VM's IPs from
// its agent on every refresh and waits up to that timeout for an agent that
// isn't there, holding every plan; running out is only a warning. The agent
// stays enabled: a new VM boots the template, whose agent careen reads the
// first host key through. An override replaces a whole nested block, so
// the rendered block is copied. (Newer providers'
// agent.wait_for_ip.disabled would skip the wait, but older ones reject
// it.)
//
// An override must name existing resources, so they're read from the
// config rather than the inventory.
func override(tfjson []byte, agentless []string) ([]byte, error) {
	var cfg struct {
		Resource map[string]map[string]map[string]json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(tfjson, &cfg); err != nil {
		return nil, fmt.Errorf("reading the rendered OpenTofu config: %w", err)
	}
	vms := map[string]any{}
	for name, attrs := range cfg.Resource[Resource] {
		vm := map[string]any{"reboot_after_update": false}
		if slices.Contains(agentless, name) {
			if agent, ok := shortAgentTimeout(attrs["agent"]); ok {
				vm["agent"] = agent
			}
		}
		vms[name] = vm
	}
	if len(vms) == 0 {
		return []byte("{}\n"), nil
	}
	return json.MarshalIndent(map[string]any{"resource": map[string]any{Resource: vms}}, "", "  ")
}

// agentlessTimeout is agent.timeout for a VM whose config doesn't run
// the agent.
const agentlessTimeout = "1s"

// shortAgentTimeout is a rendered agent block (an object, or a list of
// one) with agentlessTimeout; false when there's no enabled agent to
// change.
func shortAgentTimeout(raw json.RawMessage) (any, bool) {
	var block map[string]any
	list := false
	if json.Unmarshal(raw, &block) != nil {
		var blocks []map[string]any
		if json.Unmarshal(raw, &blocks) != nil || len(blocks) != 1 {
			return nil, false
		}
		block, list = blocks[0], true
	}
	if block["enabled"] != true {
		return nil, false
	}
	block["timeout"] = agentlessTimeout
	if list {
		return []any{block}, true
	}
	return block, true
}
