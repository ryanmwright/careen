package actions

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ryanmwright/careen/internal/activate"
	"github.com/ryanmwright/careen/internal/build"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
	"github.com/ryanmwright/careen/internal/reboot"
)

const DeployPlanSchema = "careen.deploy.plan/v1"

type DeployOptions struct {
	Reboot  string // "", reboot.Policy or reboot.Force
	BuildOn string // overrides each host's buildOn when set
	Builder string // overrides each host's builder when set
	// As overrides the SSH user, for a first deploy to a host whose image
	// doesn't have the inventory's user yet.
	As string
	// RebootSelf allows --reboot to reboot the machine careen runs on.
	RebootSelf bool
	AllowDirty bool
	Parallel   int
	// Watchdog, Settle and HealthTimeout are as in activate.Options.
	Watchdog      time.Duration
	Settle        time.Duration
	HealthTimeout time.Duration
	RebootTimeout time.Duration
	// Rolling is how --reboot runs each host's reboot hooks.
	Rolling Rolling
}

// DeployPlan is what Deploy will do; its JSON form is careen.deploy.plan/v1.
type DeployPlan struct {
	contract.Header
	Flake  inventory.Flake `json:"flake"`
	Reboot string          `json:"reboot"`
	// RebootSelf: --reboot may reboot the machine careen runs on.
	RebootSelf bool          `json:"rebootSelf"`
	Hosts      []PlanHost    `json:"hosts"`
	Skipped    []SkippedHost `json:"skipped"`

	ref     string
	targets []inventory.Host // in deploy order: the local machine last
	named   []string
	opts    DeployOptions
	fleet   []inventory.Host // every host, for hooks that run on:tag
	hooks   map[string]reboot.Hooks
}

// RebootHooks describes what runs around host's reboot when --reboot
// reboots it, for the plan.
func (p *DeployPlan) RebootHooks(host string) []string {
	if p.opts.Rolling.NoHooks {
		return nil
	}
	return p.hooks[host].Summary()
}

// WithAsk sets how the plan's reboots decide a timed-out pre hook; the TUI
// sets its own prompt here.
func (p *DeployPlan) WithAsk(ask contract.Ask) { p.opts.Rolling.Ask = ask }

type PlanHost struct {
	Host      string `json:"host"`
	Address   string `json:"address"`
	BuildOn   string `json:"buildOn"`
	Builder   string `json:"builder,omitempty"`
	Operation string `json:"operation"`
	Local     bool   `json:"local"`
}

type SkippedHost struct {
	Host   string `json:"host"`
	Reason string `json:"reason"`
}

// Targets are the hosts Deploy will deploy, in order.
func (p *DeployPlan) Targets() []inventory.Host { return p.targets }

// PlanDeploy picks the hosts sel selects. Hosts with deploy = false are
// skipped unless named in sel.Only, whose order the plan follows.
func PlanDeploy(inv *inventory.Inventory, sel inventory.Selector, opts DeployOptions) (*DeployPlan, error) {
	switch opts.Reboot {
	case "", reboot.Policy, reboot.Force:
	default:
		return nil, contract.Usage("reboot mode is policy or force, not %q", opts.Reboot)
	}
	if opts.BuildOn != "" && !slices.Contains(build.Modes, opts.BuildOn) {
		return nil, contract.Usage("build mode is one of %s, not %q", strings.Join(build.Modes, ", "), opts.BuildOn)
	}
	if inv.Flake.Dirty && !opts.AllowDirty {
		return nil, contract.Precondition("dirty", "%s has uncommitted changes; commit them or pass --allow-dirty", inv.Flake.Ref)
	}
	selected, err := inv.Select(sel)
	if err != nil {
		return nil, err
	}
	p := &DeployPlan{
		Header: contract.NewHeader(DeployPlanSchema), Flake: inv.Flake, Reboot: opts.Reboot, RebootSelf: opts.RebootSelf,
		Hosts: []PlanHost{}, Skipped: []SkippedHost{},
		ref: inv.Flake.BuildRef(), named: sel.Only, opts: opts,
		fleet: inv.Hosts, hooks: map[string]reboot.Hooks{},
	}
	for _, h := range InOnlyOrder(selected, sel.Only) {
		if !h.Deploy && !slices.Contains(sel.Only, h.Name) {
			p.Skipped = append(p.Skipped, SkippedHost{h.Name, "deploy = false; name it with --only"})
			continue
		}
		if opts.BuildOn != "" {
			h.BuildOn = opts.BuildOn
		}
		if opts.Builder != "" {
			h.Builder = opts.Builder
		}
		if opts.As != "" {
			h.User = opts.As
		}
		if opts.Reboot != "" {
			hooks, err := reboot.PrepareHooks(h)
			if err != nil {
				return nil, contract.Usage("%v", err)
			}
			p.hooks[h.Name] = hooks
		}
		p.targets = append(p.targets, h)
		ph := PlanHost{Host: h.Name, Address: h.Address, BuildOn: h.BuildOn, Operation: h.Operation, Local: IsLocal(h)}
		if h.BuildOn == build.Remote {
			ph.Builder = h.Builder
		}
		p.Hosts = append(p.Hosts, ph)
	}
	// The local machine deploys last, after the others are done.
	slices.SortStableFunc(p.targets, func(a, b inventory.Host) int { return boolInt(IsLocal(a)) - boolInt(IsLocal(b)) })
	return p, nil
}

// DeployResult is one host's outcome.
type DeployResult struct {
	Host     string           `json:"host"`
	Status   string           `json:"status"` // switched, staged, unchanged, rolled-back, failed
	Activate *activate.Result `json:"activate,omitempty"`
	Error    *contract.Error  `json:"error,omitempty"`
	// RebootPending lists why the host needs a reboot after the deploy.
	RebootPending []string       `json:"rebootPending"`
	Reboot        *RebootOutcome `json:"reboot,omitempty"`

	failedUnits []string
}

type DeployDone struct {
	Hosts []DeployResult `json:"hosts"`
}

// Deploy runs the plan: builds and switches the hosts, then reboots them if
// the plan says to. It emits a done event with the result and calls onDone
// with it once; when the local machine is rebooted, that happens just
// before the reboot, which ends careen.
func Deploy(ctx context.Context, em *contract.Emitter, p *DeployPlan, onDone func(DeployDone)) (DeployDone, error) {
	em.Started("deploy", p)
	opts := activate.Options{Operation: "switch", Watchdog: p.opts.Watchdog, HealthTimeout: p.opts.HealthTimeout, Settle: p.opts.Settle}
	done := DeployDone{Hosts: deployAll(ctx, em, p.ref, p.targets, opts, p.opts.Parallel)}
	finish := func() {
		em.Done(done)
		if onDone != nil {
			onDone(done)
		}
	}
	failed := 0
	for _, r := range done.Hosts {
		if r.Error != nil {
			failed++
		}
	}

	var rebootErr error
	if p.opts.Reboot != "" {
		var cands []candidate
		for _, r := range done.Hosts {
			if r.Error != nil {
				continue
			}
			h := p.targets[slices.IndexFunc(p.targets, func(h inventory.Host) bool { return h.Name == r.Host })]
			cands = append(cands, candidate{
				Candidate:    reboot.Candidate{Host: h, Pending: len(r.RebootPending) > 0, Reasons: r.RebootPending, Named: slices.Contains(p.named, h.Name), Local: IsLocal(h)},
				failedBefore: r.failedUnits,
				hooks:        p.hooks[h.Name],
			})
		}
		cands, unchecked := addPendingRestarts(ctx, p.Flake, cands)
		ordered, outs := planReboots(cands, p.opts.Reboot, p.named, p.opts.RebootSelf)
		localRebooted := false
		outs, rebootErr = runReboots(ctx, em, ordered, outs, rebootOptions(p.opts.RebootTimeout, p.opts.HealthTimeout), p.opts.Rolling, p.fleet, func(outs []RebootOutcome) {
			localRebooted = true
			attachReboots(done.Hosts, append(outs, unchecked...))
			finish()
		})
		if localRebooted {
			return done, rebootErr
		}
		attachReboots(done.Hosts, append(outs, unchecked...))
	}

	finish()
	switch {
	case failed > 0:
		return done, contract.Failed("deploy", "%d of %d host(s) failed", failed, len(done.Hosts))
	case rebootErr != nil:
		return done, rebootErr
	}
	return done, nil
}

// deployAll builds and switches hosts, parallel except the local machine,
// which goes alone at the end.
func deployAll(ctx context.Context, em *contract.Emitter, ref string, hosts []inventory.Host, opts activate.Options, parallel int) []DeployResult {
	results := make([]DeployResult, len(hosts))
	sem := make(chan struct{}, max(parallel, 1))
	var wg sync.WaitGroup
	for i, h := range hosts {
		if IsLocal(h) {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = deployOne(ctx, em, ref, h, opts)
		})
	}
	wg.Wait()
	for i, h := range hosts {
		if IsLocal(h) {
			results[i] = deployOne(ctx, em, ref, h, opts)
		}
	}
	return results
}

func deployOne(ctx context.Context, em *contract.Emitter, ref string, h inventory.Host, opts activate.Options) DeployResult {
	res := DeployResult{Host: h.Name, RebootPending: []string{}}
	progress := func(step, msg string) { em.Progress(h.Name, step, "%s", msg) }
	fail := func(step string, err error) DeployResult {
		e := contract.ErrorObject(contract.Failed(step, "%v", err))
		e.Host = h.Name
		res.Error = &e
		if res.Status == "" {
			res.Status = "failed"
		}
		em.Error(h.Name, step, err)
		return res
	}

	job := build.Job{Host: h, Mode: h.BuildOn, Builder: h.Builder}
	if !IsLocal(h) {
		job.Dest = build.HostStore(h)
	}
	path, err := build.Build(ctx, execx.Run, ref, job, progress)
	if err != nil {
		return fail("build", err)
	}

	o := opts
	o.Operation = h.Operation
	act, err := activate.Activate(ctx, Conn(h, false), Conn(h, true), path, o, progress)
	res.Activate = &act
	res.Status = act.Status
	if err != nil {
		if act.Status == "" {
			res.Status = "failed"
		}
		return fail("activate", err)
	}
	progress("done", act.Status)

	// What a reboot would change, and the baseline for its health check.
	if r, err := probe.New().Probe(ctx, h); err == nil {
		res.RebootPending = orEmpty(r.RebootReasons())
		res.failedUnits = r.FailedUnits
	} else if act.Status == activate.Staged {
		res.RebootPending = []string{"staged"}
	}
	return res
}

func attachReboots(results []DeployResult, outs []RebootOutcome) {
	for i := range outs {
		for j := range results {
			if results[j].Host == outs[i].Host {
				results[j].Reboot = &outs[i]
			}
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
