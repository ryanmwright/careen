package tui

import (
	"context"
	"os"
	"os/exec"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/logs"
	"github.com/ryanmwright/careen/internal/probe"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/status"
	"github.com/ryanmwright/careen/internal/vulns"
)

// Backend is everything the TUI does to the fleet. The real one calls the
// same functions as the CLI commands; tests use a fake.
type Backend interface {
	// Status is what `careen status` shows: the inventory and a probe of
	// every host.
	Status(ctx context.Context) (*inventory.Inventory, status.Report, error)
	// Inventory loads the flake fresh, as actions do before planning.
	Inventory(ctx context.Context) (*inventory.Inventory, error)
	PlanDeploy(inv *inventory.Inventory, only []string, rebootMode string, rebootSelf bool) (*actions.DeployPlan, error)
	Deploy(ctx context.Context, p *actions.DeployPlan, sink func(contract.Event)) (actions.DeployDone, error)
	PlanReboot(ctx context.Context, inv *inventory.Inventory, only []string, mode string, self bool) (*actions.RebootPlan, error)
	Reboot(ctx context.Context, p *actions.RebootPlan, sink func(contract.Event)) (actions.RebootDone, error)
	Rollback(ctx context.Context, h inventory.Host, sink func(contract.Event)) (actions.RollbackDone, error)
	// FollowLogs calls fn with the last entries and then new ones until ctx
	// is done.
	FollowLogs(ctx context.Context, h inventory.Host, unit string, fn func(logs.Entry)) error
	// NeedsSudo reports whether acting on hosts needs a sudo password here
	// first.
	NeedsSudo(ctx context.Context, hosts []inventory.Host) bool
	// Shell is the command that opens a shell on h.
	Shell(h inventory.Host) *exec.Cmd

	PlanGC(ctx context.Context, inv *inventory.Inventory, only []string) (*actions.GCPlan, error)
	GC(ctx context.Context, p *actions.GCPlan, sink func(contract.Event)) (actions.GCDone, error)
	PlanExec(inv *inventory.Inventory, only []string, command string, sudo bool) (*actions.ExecPlan, error)
	Exec(ctx context.Context, p *actions.ExecPlan, sink func(contract.Event)) (actions.ExecDone, error)
	// Vulns scans the hosts (or reads their cached scans); progress may be
	// called from another goroutine.
	Vulns(ctx context.Context, inv *inventory.Inventory, only []string, refresh bool, progress func(host, msg string)) (actions.VulnsReport, error)
	// CachedVulns is host's last scan of system, or nil.
	CachedVulns(host, system string) *vulns.Report

	// Onboarding, keys and secrets work on the writable checkout.
	Keys(ctx context.Context) (actions.KeysReport, error)
	MasterKey(ctx context.Context) error
	RegisterKey(ctx context.Context, host string, replace bool, sink func(contract.Event)) error
	RekeyHost(ctx context.Context, host string, sink func(contract.Event)) error
	Secrets(ctx context.Context) (actions.SecretsReport, error)
	SetSecret(ctx context.Context, host, file, name, value string) (bool, error)
	UnsetSecret(ctx context.Context, host, file, name string) error
	EditSecrets(ctx context.Context, host, file string) (*exec.Cmd, error)
	AddStatus(ctx context.Context, v repo.HostVars) (actions.AddStatus, error)
	PreviewAdd(ctx context.Context, v repo.HostVars) (actions.AddPreview, error)
	Add(ctx context.Context, v repo.HostVars, sink func(contract.Event)) (actions.AddStatus, error)
	// ScanKey fetches address's host key as an age key.
	ScanKey(ctx context.Context, address string) (string, error)
	// PlanDeployWorktree plans deploying host from the checkout as it is.
	PlanDeployWorktree(ctx context.Context, host, as string) (*actions.DeployPlan, error)
	// GitDiff is the command that shows the checkout's uncommitted changes.
	GitDiff() *exec.Cmd
}

// Options are the CLI's defaults for actions started from the TUI.
type Options struct {
	Flake  string
	Repo   string // the writable checkout, for onboarding, keys and secrets
	Deploy actions.DeployOptions
	Reboot actions.RebootOptions
	GC     actions.GCOptions
	Exec   actions.ExecOptions // Sudo is chosen per run
	Vulns  actions.VulnsOptions
}

// NewBackend is the real backend.
func NewBackend(o Options) Backend { return real{o} }

type real struct{ o Options }

const eventsSchema = "careen.events/v1"

func (r real) Status(ctx context.Context) (*inventory.Inventory, status.Report, error) {
	inv, err := inventory.Load(ctx, execx.Run, r.o.Flake, true)
	if err != nil {
		return nil, status.Report{}, err
	}
	return inv, status.Build(inv.Flake, probe.New().ProbeAll(ctx, inv.Hosts)), nil
}

func (r real) Inventory(ctx context.Context) (*inventory.Inventory, error) {
	return inventory.Load(ctx, execx.Run, r.o.Flake, true)
}

func (r real) PlanDeploy(inv *inventory.Inventory, only []string, rebootMode string, rebootSelf bool) (*actions.DeployPlan, error) {
	o := r.o.Deploy
	o.Reboot, o.RebootSelf = rebootMode, rebootSelf
	return actions.PlanDeploy(inv, inventory.Selector{Only: only}, o)
}

func (r real) Deploy(ctx context.Context, p *actions.DeployPlan, sink func(contract.Event)) (actions.DeployDone, error) {
	return actions.Deploy(ctx, contract.NewSinkEmitter(eventsSchema, sink), p, nil)
}

func (r real) PlanReboot(ctx context.Context, inv *inventory.Inventory, only []string, mode string, self bool) (*actions.RebootPlan, error) {
	o := r.o.Reboot
	o.Mode, o.Self = mode, self
	return actions.PlanReboot(ctx, inv, inventory.Selector{Only: only}, o)
}

func (r real) Reboot(ctx context.Context, p *actions.RebootPlan, sink func(contract.Event)) (actions.RebootDone, error) {
	return actions.Reboot(ctx, contract.NewSinkEmitter(eventsSchema, sink), p, nil)
}

func (r real) Rollback(ctx context.Context, h inventory.Host, sink func(contract.Event)) (actions.RollbackDone, error) {
	return actions.Rollback(ctx, contract.NewSinkEmitter(eventsSchema, sink), h, r.o.Reboot.HealthTimeout)
}

func (r real) FollowLogs(ctx context.Context, h inventory.Host, unit string, fn func(logs.Entry)) error {
	return logs.Follow(ctx, actions.Conn(h, false), unit, 500, fn)
}

func (r real) NeedsSudo(ctx context.Context, hosts []inventory.Host) bool {
	return actions.IncludesLocal(hosts) && actions.LocalSudo(ctx) != nil
}

func (r real) Shell(h inventory.Host) *exec.Cmd {
	if actions.IsLocal(h) {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		return exec.Command(sh)
	}
	dest := h.Address
	if h.User != "" {
		dest = h.User + "@" + h.Address
	}
	return exec.Command("ssh", dest)
}

func (r real) PlanGC(ctx context.Context, inv *inventory.Inventory, only []string) (*actions.GCPlan, error) {
	return actions.PlanGC(ctx, inv, inventory.Selector{Only: only}, r.o.GC)
}

func (r real) GC(ctx context.Context, p *actions.GCPlan, sink func(contract.Event)) (actions.GCDone, error) {
	return actions.GC(ctx, contract.NewSinkEmitter(eventsSchema, sink), p)
}

func (r real) PlanExec(inv *inventory.Inventory, only []string, command string, sudo bool) (*actions.ExecPlan, error) {
	o := r.o.Exec
	o.Sudo = sudo
	return actions.PlanExec(inv, inventory.Selector{Only: only}, command, o)
}

func (r real) Exec(ctx context.Context, p *actions.ExecPlan, sink func(contract.Event)) (actions.ExecDone, error) {
	return actions.Exec(ctx, contract.NewSinkEmitter(eventsSchema, sink), p)
}

func (r real) Vulns(ctx context.Context, inv *inventory.Inventory, only []string, refresh bool, progress func(host, msg string)) (actions.VulnsReport, error) {
	o := r.o.Vulns
	o.Refresh = refresh
	return actions.Vulns(ctx, inv, inventory.Selector{Only: only}, o, progress)
}

func (r real) CachedVulns(host, system string) *vulns.Report {
	return vulns.Cached(host, system, 0)
}

// repo opens the checkout each time, so edits made outside careen show.
func (r real) repo(ctx context.Context) (*repo.Repo, error) {
	dir, err := repo.Find(r.o.Repo)
	if err != nil {
		return nil, err
	}
	return repo.Open(ctx, execx.Run, dir)
}

func (r real) Keys(ctx context.Context) (actions.KeysReport, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return actions.KeysReport{}, err
	}
	inv, _ := inventory.Load(ctx, execx.Run, r.o.Flake, false)
	return actions.Keys(ctx, rp, inv, nil)
}

func (r real) MasterKey(ctx context.Context) error {
	rp, err := r.repo(ctx)
	if err != nil {
		return err
	}
	return actions.MasterKey(ctx, rp)
}

func (r real) RegisterKey(ctx context.Context, host string, replace bool, sink func(contract.Event)) error {
	rp, err := r.repo(ctx)
	if err != nil {
		return err
	}
	addr := ""
	if inv, err := inventory.Load(ctx, execx.Run, r.o.Flake, false); err == nil {
		for _, h := range inv.Hosts {
			if h.Name == host {
				addr = h.Address
			}
		}
	}
	return actions.RegisterKey(ctx, contract.NewSinkEmitter(eventsSchema, sink), rp, host, actions.RegisterOptions{Address: addr, Replace: replace})
}

func (r real) RekeyHost(ctx context.Context, host string, sink func(contract.Event)) error {
	rp, err := r.repo(ctx)
	if err != nil {
		return err
	}
	return actions.RekeyHost(ctx, contract.NewSinkEmitter(eventsSchema, sink), rp, host)
}

func (r real) Secrets(ctx context.Context) (actions.SecretsReport, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return actions.SecretsReport{}, err
	}
	return actions.Secrets(rp, nil)
}

func (r real) SetSecret(ctx context.Context, host, file, name, value string) (bool, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return false, err
	}
	return actions.SetSecret(ctx, rp, host, file, name, value)
}

func (r real) UnsetSecret(ctx context.Context, host, file, name string) error {
	rp, err := r.repo(ctx)
	if err != nil {
		return err
	}
	return actions.UnsetSecret(ctx, rp, host, file, name)
}

func (r real) EditSecrets(ctx context.Context, host, file string) (*exec.Cmd, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return nil, err
	}
	c, _, err := actions.EditSecrets(ctx, rp, host, file)
	return c, err
}

func (r real) AddStatus(ctx context.Context, v repo.HostVars) (actions.AddStatus, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return actions.AddStatus{}, err
	}
	return actions.AddStatusFor(ctx, rp, v.Name, v.Address)
}

func (r real) PreviewAdd(ctx context.Context, v repo.HostVars) (actions.AddPreview, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return actions.AddPreview{}, err
	}
	return actions.PreviewAdd(rp, v)
}

func (r real) Add(ctx context.Context, v repo.HostVars, sink func(contract.Event)) (actions.AddStatus, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return actions.AddStatus{}, err
	}
	return actions.Add(ctx, contract.NewSinkEmitter(eventsSchema, sink), rp, actions.AddOptions{Vars: v})
}

func (r real) ScanKey(ctx context.Context, address string) (string, error) {
	return agekey.Scan(ctx, address, 0)
}

func (r real) PlanDeployWorktree(ctx context.Context, host, as string) (*actions.DeployPlan, error) {
	rp, err := r.repo(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := inventory.Load(ctx, execx.Run, rp.Dir, false)
	if err != nil {
		return nil, err
	}
	o := r.o.Deploy
	o.AllowDirty, o.As, o.Reboot = true, as, ""
	return actions.PlanDeploy(inv, inventory.Selector{Only: []string{host}}, o)
}

func (r real) GitDiff() *exec.Cmd {
	dir, _ := repo.Find(r.o.Repo)
	return exec.Command("git", "-C", dir, "diff")
}
