package actions

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/hostkey"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/knownhosts"
	"github.com/ryanmwright/careen/internal/provision"
	"github.com/ryanmwright/careen/internal/provision/proxmox"
	"github.com/ryanmwright/careen/internal/reboot"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/sopsfile"
	"github.com/ryanmwright/careen/internal/sshx"
)

const (
	UpPlanSchema   = "careen.up.plan/v1"
	DownPlanSchema = "careen.down.plan/v1"
	VMPlanSchema   = "careen.vm.plan/v1"
)

// Provisioners gives each host its provisioner. Hosts that share one get
// the same instance, so a run renders and initializes OpenTofu once.
type Provisioners struct {
	proxmox  *proxmox.Proxmox
	existing provision.Provisioner
	dec      *Decrypter
}

// Decrypter decrypts secrets with the master key. A decryption that's
// still running after PromptAfter is waiting on the key (a touch, or a
// PIN), so only then does it announce itself; one that needs neither (a
// touch cached by the key, an age key) stays quiet. A missed touch can be
// retried.
type Decrypter struct {
	// Prompt is where touch prompts and retry questions go (stderr); nil
	// stays silent.
	Prompt io.Writer
	// PromptAfter is how long a decryption may run before careen says it's
	// waiting for the key (default 750ms). Whether a touch is needed can't
	// be asked in advance: the touch policy is on the card, often behind a
	// forwarded gpg-agent, and a cached touch expires on the key's clock.
	PromptAfter time.Duration
	// Interactive allows asking to retry a failed decryption.
	Interactive bool
	// Attempts bounds tries per secret when interactive (default 3).
	Attempts int
	// Extract decrypts one value; sops when nil.
	Extract func(ctx context.Context, file, key string) (string, error)
}

// Decrypt decrypts key from file; what names it in the prompts.
func (d *Decrypter) Decrypt(ctx context.Context, what, file, key string) (string, error) {
	extract := d.Extract
	if extract == nil {
		extract = sopsfile.Sops{}.Extract
	}
	attempts := d.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	after := d.PromptAfter
	if after <= 0 {
		after = 750 * time.Millisecond
	}
	for i := 1; ; i++ {
		v, err := d.waitingPrompt(after, what, func() (string, error) { return extract(ctx, file, key) })
		if err == nil {
			return v, nil
		}
		if !d.Interactive || d.Prompt == nil || i >= attempts || ctx.Err() != nil {
			return "", err
		}
		fmt.Fprintf(d.Prompt, "    decrypting %s failed (a missed touch times out): %s\n", what, execx.Tail(err.Error(), 1))
		if !contract.Confirm(d.Prompt, fmt.Sprintf("    Try again (%d of %d)?", i+1, attempts)) {
			return "", err
		}
	}
}

// waitingPrompt runs decrypt and, if it's still running after delay,
// prints the prompt (with a bell). It returns once the prompt, if any, is
// written, so later output can't interleave with it.
func (d *Decrypter) waitingPrompt(delay time.Duration, what string, decrypt func() (string, error)) (string, error) {
	if d.Prompt == nil {
		return decrypt()
	}
	done := make(chan struct{})
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			fmt.Fprintf(d.Prompt, "\a>>> Waiting for your security key (touch it if it's blinking): decrypting %s\n", what)
		}
	}()
	v, err := decrypt()
	close(done)
	<-printed
	return v, err
}

// StateDir is where provisioners keep state on this machine
// ($XDG_STATE_HOME/careen, else ~/.local/state/careen).
func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "careen")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "careen")
}

// NewProvisioners sets up the provisioners for inv's flake. The Proxmox
// API token is decrypted from the flake's sops file with the master key,
// through dec, when first needed.
func NewProvisioners(inv *inventory.Inventory, dec *Decrypter) *Provisioners {
	if dec == nil {
		dec = &Decrypter{}
	}
	return &Provisioners{
		dec:      dec,
		existing: provision.Existing{},
		proxmox: &proxmox.Proxmox{
			Flake: inv.Flake,
			Dir:   filepath.Join(StateDir(), "pve"),
			Run:   execx.Run,
			Cmd:   proxmox.RunCmd,
			Token: func(ctx context.Context, file, key string) (string, error) {
				return dec.Decrypt(ctx, "the Proxmox API token", file, key)
			},
		},
	}
}

// Proxmox is the Proxmox provisioner, for templates.
func (ps *Provisioners) Proxmox() *proxmox.Proxmox { return ps.proxmox }

// For is h's provisioner.
func (ps *Provisioners) For(h inventory.Host) (provision.Provisioner, error) {
	switch h.ProvisionerName() {
	case inventory.Existing:
		return ps.existing, nil
	case inventory.Proxmox:
		return ps.proxmox, nil
	}
	return nil, contract.Usage("%s: unknown provisioner %q", h.Name, h.Provisioner)
}

// group splits hosts by provisioner, keeping their order within each.
func (ps *Provisioners) group(hosts []inventory.Host) ([]provision.Provisioner, map[provision.Provisioner][]inventory.Host, error) {
	var order []provision.Provisioner
	by := map[provision.Provisioner][]inventory.Host{}
	for _, h := range hosts {
		p, err := ps.For(h)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := by[p]; !ok {
			order = append(order, p)
		}
		by[p] = append(by[p], h)
	}
	return order, by, nil
}

// MachineHost is one host in an up, down or vm plan.
type MachineHost struct {
	Host        string `json:"host"`
	Address     string `json:"address"`
	Provisioner string `json:"provisioner"`
	// Machine is what the provisioner would do: create, update, replace,
	// delete or no-op.
	Machine string `json:"machine"`
	// HostKey is the pre-generated key's file, when the host has one.
	HostKey string `json:"hostKey,omitempty"`
}

// MachinePlan is what up, down or vm apply would do; careen.up.plan/v1,
// careen.down.plan/v1 or careen.vm.plan/v1.
type MachinePlan struct {
	contract.Header
	Flake   inventory.Flake `json:"flake"`
	Hosts   []MachineHost   `json:"hosts"`
	Skipped []SkippedHost   `json:"skipped"`

	targets  []inventory.Host
	settings repo.Settings
}

// Targets are the hosts the plan acts on, in order.
func (p *MachinePlan) Targets() []inventory.Host { return p.targets }

// planMachines plans hosts' machines; destroy plans their removal. Hosts
// careen didn't provision are skipped when skipExisting, or refused.
func planMachines(ctx context.Context, schema string, inv *inventory.Inventory, sel inventory.Selector, ps *Provisioners, settings repo.Settings, destroy, skipExisting bool) (*MachinePlan, error) {
	selected, err := inv.Select(sel)
	if err != nil {
		return nil, err
	}
	p := &MachinePlan{Header: contract.NewHeader(schema), Flake: inv.Flake, Hosts: []MachineHost{}, Skipped: []SkippedHost{}, settings: settings}
	for _, h := range InOnlyOrder(selected, sel.Only) {
		if h.ProvisionerName() == inventory.Existing && skipExisting {
			p.Skipped = append(p.Skipped, SkippedHost{h.Name, "not provisioned by careen"})
			continue
		}
		p.targets = append(p.targets, h)
	}
	if destroy {
		for _, h := range p.targets {
			if h.ProvisionerName() == inventory.Existing {
				return nil, contract.Usage("%s isn't provisioned by careen, so careen won't remove it", h.Name)
			}
		}
	}
	order, by, err := ps.group(p.targets)
	if err != nil {
		return nil, err
	}
	actions := map[string]string{}
	for _, prov := range order {
		pl, err := prov.Plan(ctx, by[prov], destroy)
		if err != nil {
			return nil, contract.Failed("plan", "%v", err)
		}
		for _, c := range pl.Changes {
			actions[c.Host] = c.Action
		}
	}
	for _, h := range p.targets {
		mh := MachineHost{Host: h.Name, Address: h.Address, Provisioner: h.ProvisionerName(), Machine: actions[h.Name]}
		if mh.Machine == "" {
			mh.Machine = provision.NoOp
		}
		if rel := settings.HostKeyFile(h.Name); exists(filepath.Join(inv.Flake.Path, rel)) {
			mh.HostKey = rel
		}
		p.Hosts = append(p.Hosts, mh)
	}
	return p, nil
}

// PlanUp plans bringing hosts up: their machines, then a deploy each.
func PlanUp(ctx context.Context, inv *inventory.Inventory, sel inventory.Selector, ps *Provisioners, settings repo.Settings, allowDirty bool) (*MachinePlan, error) {
	if inv.Flake.Dirty && !allowDirty {
		return nil, contract.Precondition("dirty", "%s has uncommitted changes; commit them or pass --allow-dirty", inv.Flake.Ref)
	}
	return planMachines(ctx, UpPlanSchema, inv, sel, ps, settings, false, false)
}

// PlanDown plans removing hosts' machines.
func PlanDown(ctx context.Context, inv *inventory.Inventory, sel inventory.Selector, ps *Provisioners, settings repo.Settings) (*MachinePlan, error) {
	return planMachines(ctx, DownPlanSchema, inv, sel, ps, settings, true, false)
}

// PlanVM plans making provisioned hosts' machines match the inventory,
// without deploying; other hosts are skipped.
func PlanVM(ctx context.Context, inv *inventory.Inventory, sel inventory.Selector, ps *Provisioners, settings repo.Settings) (*MachinePlan, error) {
	return planMachines(ctx, VMPlanSchema, inv, sel, ps, settings, false, true)
}

// MachineResult is one host's outcome of up, down or vm apply.
type MachineResult struct {
	Host    string `json:"host"`
	Machine string `json:"machine"`
	// KeyInstalled is set when up installed the pre-generated host key.
	KeyInstalled bool `json:"keyInstalled"`
	// FirstDeploy is set when up deployed the host's own system for the
	// first time (as a boot entry, then a reboot).
	FirstDeploy bool            `json:"firstDeploy"`
	Deploy      *DeployResult   `json:"deploy,omitempty"`
	Error       *contract.Error `json:"error,omitempty"`
}

type MachineDone struct {
	Hosts []MachineResult `json:"hosts"`
}

// UpOptions are the deploy options for up; Reboot and Parallel are set per
// host.
type UpOptions struct {
	Deploy DeployOptions
	// KnownHosts is the known_hosts file up and down keep current;
	// knownhosts.Default() when empty.
	KnownHosts string
	// NoAgentWait doesn't wait for the guest agent of a VM that exists.
	NoAgentWait bool
}

// Up brings each host up in turn, stopping at the first failure:
//  1. the provisioner makes the machine exist and run;
//  2. careen installs the host's pre-generated SSH key (if it has one),
//     over a connection pinned to the key the provisioner read;
//  3. careen records the key in known_hosts and deploys the host. A first
//     deploy over a generic image goes in as a boot entry, then a reboot,
//     since it swaps the network setup under the connection.
//
// Every step is a no-op when already done, so running it again continues.
func Up(ctx context.Context, em *contract.Emitter, p *MachinePlan, ps *Provisioners, o UpOptions) (MachineDone, error) {
	em.Started("up", p)
	kh := o.KnownHosts
	if kh == "" {
		kh = knownhosts.Default()
	}
	if px := ps.proxmox; px != nil {
		px.NoAgentWait = o.NoAgentWait
		px.TrustedKeys = func(h inventory.Host) []string { return trustedKeys(p, h, kh) }
	}
	done := MachineDone{Hosts: []MachineResult{}}
	keys, err := prepareKeys(ctx, em, p, ps.dec)
	if err != nil {
		em.Done(done)
		return done, err
	}
	for _, h := range p.targets {
		r, err := upOne(ctx, em, p, ps, h, kh, keys, o)
		done.Hosts = append(done.Hosts, r)
		if err != nil {
			e := contract.ErrorObject(err)
			e.Host = h.Name
			done.Hosts[len(done.Hosts)-1].Error = &e
			em.Done(done)
			return done, err
		}
	}
	em.Done(done)
	return done, nil
}

// prepareKeys decrypts, before any slow work, the pre-generated key of
// every host that doesn't run it yet, so the master key (maybe a touch)
// is needed while the user is still at the terminal rather than minutes in.
// A host that already answers with its key is skipped.
func prepareKeys(ctx context.Context, em *contract.Emitter, p *MachinePlan, dec *Decrypter) (map[string]string, error) {
	keys := map[string]string{}
	for _, h := range p.targets {
		rel := p.settings.HostKeyFile(h.Name)
		file := filepath.Join(p.Flake.Path, rel)
		if !exists(file) {
			continue
		}
		pub, err := sopsfile.Plain(file, repo.HostKeyPublic)
		if err != nil {
			return nil, contract.Failed("hostkey", "%v", err)
		}
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		live, err := agekey.ScanLine(sctx, h.Address, 0)
		cancel()
		if err == nil && hostkey.SameKey(live, pub) {
			continue
		}
		em.Progress(h.Name, "hostkey", "decrypting its key from %s now, before the slow steps", rel)
		priv, err := dec.Decrypt(ctx, h.Name+"'s host key", file, repo.HostKeyPrivate)
		if err != nil {
			em.Decision(h.Name, "hostkey", "decrypting the host key needs the master key", "make the master key available and run careen up again")
			return nil, contract.Precondition("master-key", "decrypting %s: %v", rel, err)
		}
		keys[h.Name] = priv
	}
	return keys, nil
}

// trustedKeys are the keys careen trusts for h without asking the machine:
// its pre-generated key in the repo and its known_hosts entry.
func trustedKeys(p *MachinePlan, h inventory.Host, kh string) []string {
	keys := knownhosts.Keys(kh, h.Name, h.Address)
	if file := filepath.Join(p.Flake.Path, p.settings.HostKeyFile(h.Name)); exists(file) {
		if pub, err := sopsfile.Plain(file, repo.HostKeyPublic); err == nil {
			keys = append(keys, pub)
		}
	}
	return keys
}

// ReportFlake says on w which commit a remote flake reference resolved to,
// and warns when the checkout at repoDir has commits that commit lacks:
// careen builds what the remote has, so deploying then would use stale
// state. A local flake needs neither.
func ReportFlake(ctx context.Context, w io.Writer, f inventory.Flake, repoDir string) {
	if !f.Remote || f.Revision == "" {
		return
	}
	branch := f.Branch
	if branch == "" {
		branch = "its default branch"
	}
	fmt.Fprintf(w, "Flake %s is %s at %.12s\n", f.Ref, branch, f.Revision)
	if repoDir == "" {
		return
	}
	out, err := execx.Run(ctx, "git", "-C", repoDir, "rev-list", "--count", f.Revision+"..HEAD")
	if err != nil {
		fmt.Fprintf(w, "warning: %.12s isn't in %s, so careen can't tell whether it is behind your checkout; fetch there to compare\n", f.Revision, repoDir)
		return
	}
	if n := strings.TrimSpace(string(out)); n != "0" {
		fmt.Fprintf(w, "warning: %s has %s commit(s) that %.12s lacks; push them first, or this deploys stale state\n", repoDir, n, f.Revision)
	}
}

// took reports how long a step of a host's up took.
func took(em *contract.Emitter, host, step string, start time.Time) {
	em.Progress(host, step, "took %s", time.Since(start).Round(100*time.Millisecond))
}

func upOne(ctx context.Context, em *contract.Emitter, p *MachinePlan, ps *Provisioners, h inventory.Host, kh string, keys map[string]string, o UpOptions) (MachineResult, error) {
	r := MachineResult{Host: h.Name, Machine: provision.NoOp}
	progress := func(host, step, msg string) { em.Progress(host, step, "%s", msg) }
	prov, err := ps.For(h)
	if err != nil {
		return r, err
	}
	start := time.Now()
	m, err := prov.Ensure(ctx, h, progress)
	if err != nil {
		return r, contract.Failed("provision", "%v", err)
	}
	took(em, h.Name, "provision", start)
	if m.Created {
		r.Machine = provision.Create
	}

	start = time.Now()
	installed, err := installHostKey(ctx, em, p, ps.dec, h, m, kh, keys[h.Name])
	r.KeyInstalled = installed
	if err != nil {
		return r, err
	}
	took(em, h.Name, "hostkey", start)

	start = time.Now()
	first, err := firstDeploy(ctx, h, m)
	if err != nil {
		return r, contract.Failed("connect", "%v", err)
	}
	took(em, h.Name, "connect", start)
	r.FirstDeploy = first
	opts := o.Deploy
	opts.Parallel = 1
	hc := h
	if first {
		em.Progress(h.Name, "deploy", "first deploy over the generic image: as a boot entry, then a reboot")
		hc.Operation = "boot"
		opts.Reboot = reboot.Force
		// The host isn't part of anything yet (a cluster to drain it from,
		// a pool to take it out of), so its reboot hooks don't apply.
		opts.Rolling.NoHooks = true
	}
	plan, err := PlanDeploy(&inventory.Inventory{Flake: p.Flake, Hosts: []inventory.Host{hc}}, inventory.Selector{Only: []string{h.Name}}, opts)
	if err != nil {
		return r, err
	}
	start = time.Now()
	dd, err := Deploy(ctx, em, plan, nil)
	took(em, h.Name, "deploy", start)
	if len(dd.Hosts) == 1 {
		r.Deploy = &dd.Hosts[0]
	}
	return r, err
}

// installHostKey puts h's pre-generated key on the machine unless it's
// already there, and records the key the machine ends up with in
// known_hosts. Without a pre-generated key a provisioned machine's boot
// key (read out of band) is recorded; an existing machine's entry is left
// alone.
func installHostKey(ctx context.Context, em *contract.Emitter, p *MachinePlan, dec *Decrypter, h inventory.Host, m provision.Machine, kh, priv string) (bool, error) {
	keyFile := filepath.Join(p.Flake.Path, p.settings.HostKeyFile(h.Name))
	names := []string{h.Name, h.Address}
	if !exists(keyFile) {
		if h.ProvisionerName() == inventory.Existing || m.BootHostKey == "" {
			return false, nil
		}
		bare, err := hostkey.Bare(m.BootHostKey)
		if err != nil {
			return false, contract.Failed("hostkey", "%s's boot key: %v", h.Name, err)
		}
		return false, knownhosts.Set(kh, bare, names...)
	}

	pub, err := sopsfile.Plain(keyFile, repo.HostKeyPublic)
	if err != nil {
		return false, contract.Failed("hostkey", "%v", err)
	}
	bare, err := hostkey.Bare(pub)
	if err != nil {
		return false, contract.Failed("hostkey", "%s: %v", p.settings.HostKeyFile(h.Name), err)
	}
	if m.BootHostKey != "" && hostkey.SameKey(m.BootHostKey, pub) {
		em.Progress(h.Name, "hostkey", "already has its key")
		return false, knownhosts.Set(kh, bare, names...)
	}
	if m.BootHostKey == "" {
		return false, contract.Failed("hostkey", "%s's current host key is unknown, so careen can't connect safely to install its key", h.Name)
	}

	if priv == "" { // not decrypted up front (the host answered with its key then)
		em.Progress(h.Name, "hostkey", "decrypting its key from %s", p.settings.HostKeyFile(h.Name))
		priv, err = dec.Decrypt(ctx, h.Name+"'s host key", keyFile, repo.HostKeyPrivate)
		if err != nil {
			em.Decision(h.Name, "hostkey", "decrypting the host key needs the master key", "make the master key available and run careen up again")
			return false, contract.Precondition("master-key", "decrypting %s: %v", p.settings.HostKeyFile(h.Name), err)
		}
	}

	em.Progress(h.Name, "hostkey", "installing it over a connection pinned to the machine's boot key")
	if err := pushHostKey(ctx, h, m.BootHostKey, priv, pub); err != nil {
		return false, contract.Failed("hostkey", "%v", err)
	}
	if err := knownhosts.Set(kh, bare, names...); err != nil {
		return true, err
	}
	// sshd restarts with the new key; wait for it to answer with that key.
	if err := waitSSH(ctx, h, 90*time.Second); err != nil {
		return true, contract.Failed("hostkey", "%s didn't come back with its new key: %v", h.Name, err)
	}
	return true, nil
}

// pushHostKey writes the key into /etc/ssh and restarts sshd, connecting
// with known_hosts holding only bootKey for the address.
func pushHostKey(ctx context.Context, h inventory.Host, bootKey, priv, pub string) error {
	tmp, err := os.CreateTemp("", "careen-known-hosts-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	bare, err := hostkey.Bare(bootKey)
	if err != nil {
		return err
	}
	fmt.Fprintf(tmp, "%s %s\n", h.Address, bare)
	tmp.Close()

	const script = `set -e
umask 077
cat > /etc/ssh/ssh_host_ed25519_key.careen
mv /etc/ssh/ssh_host_ed25519_key.careen /etc/ssh/ssh_host_ed25519_key
printf '%s\n' "$1" > /etc/ssh/ssh_host_ed25519_key.pub
chmod 644 /etc/ssh/ssh_host_ed25519_key.pub
systemctl restart sshd`
	remote := "sudo sh -c " + sshx.Quote(script) + " careen " + sshx.Quote(pub)
	r := sshx.Remote{User: h.User, Address: h.Address, NoMaster: true, ConnectTimeout: 15 * time.Second, Args: []string{
		"-o", "UserKnownHostsFile=" + tmp.Name(),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes",
	}}
	_, err = proxmox.RunCmd(ctx, "", nil, strings.NewReader(priv), "ssh", r.SSHArgs(remote)...)
	return err
}

// waitSSH waits until a fresh connection to h works with the known_hosts
// careen keeps.
func waitSSH(ctx context.Context, h inventory.Host, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := Conn(h, true).Run(ctx, "true")
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// firstDeploy reports whether h is still running something other than its
// own system (a fresh clone of the generic image).
func firstDeploy(ctx context.Context, h inventory.Host, m provision.Machine) (bool, error) {
	if m.Created {
		return true, nil
	}
	if h.ProvisionerName() == inventory.Existing {
		return false, nil
	}
	if err := waitSSH(ctx, h, 2*time.Minute); err != nil {
		return false, err
	}
	out, err := Conn(h, true).Run(ctx, "readlink /run/current-system")
	if err != nil {
		return false, err
	}
	return !strings.Contains(string(out), "-nixos-system-"+h.Name+"-"), nil
}

// Down removes hosts' machines, then forgets their keys in known_hosts.
// Their pre-generated keys and secrets stay in the repo, so the next up
// makes the same hosts.
func Down(ctx context.Context, em *contract.Emitter, p *MachinePlan, ps *Provisioners, knownHostsFile string) (MachineDone, error) {
	em.Started("down", p)
	if knownHostsFile == "" {
		knownHostsFile = knownhosts.Default()
	}
	done, err := applyMachines(ctx, em, p, ps, true)
	if err != nil {
		em.Done(done)
		return done, err
	}
	for _, h := range p.targets {
		if err := knownhosts.Remove(knownHostsFile, h.Name, h.Address); err != nil {
			em.Done(done)
			return done, err
		}
	}
	em.Done(done)
	return done, nil
}

// ApplyVM makes the hosts' machines match the inventory, without
// deploying.
func ApplyVM(ctx context.Context, em *contract.Emitter, p *MachinePlan, ps *Provisioners) (MachineDone, error) {
	em.Started("vm apply", p)
	done, err := applyMachines(ctx, em, p, ps, false)
	em.Done(done)
	return done, err
}

func applyMachines(ctx context.Context, em *contract.Emitter, p *MachinePlan, ps *Provisioners, destroy bool) (MachineDone, error) {
	done := MachineDone{Hosts: []MachineResult{}}
	progress := func(host, step, msg string) { em.Progress(host, step, "%s", msg) }
	order, by, err := ps.group(p.targets)
	if err != nil {
		return done, err
	}
	for _, prov := range order {
		var pl provision.Plan
		if destroy {
			pl, err = prov.Destroy(ctx, by[prov], progress)
		} else {
			pl, err = prov.Apply(ctx, by[prov], progress)
		}
		for _, c := range pl.Changes {
			done.Hosts = append(done.Hosts, MachineResult{Host: c.Host, Machine: c.Action})
		}
		if err != nil {
			return done, contract.Failed("provision", "%v", err)
		}
	}
	slices.SortStableFunc(done.Hosts, func(a, b MachineResult) int {
		return slices.IndexFunc(p.targets, func(h inventory.Host) bool { return h.Name == a.Host }) -
			slices.IndexFunc(p.targets, func(h inventory.Host) bool { return h.Name == b.Host })
	})
	return done, nil
}
