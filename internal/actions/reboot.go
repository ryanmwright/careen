package actions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/activate"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
	"github.com/ryanmwright/careen/internal/provision"
	"github.com/ryanmwright/careen/internal/reboot"
	"github.com/ryanmwright/careen/internal/sshx"
)

const RebootPlanSchema = "careen.reboot.plan/v1"

// ReasonHardware is the reboot reason for hardware changes the host's
// provisioner holds until the machine restarts from outside; careen then
// restarts it through the provisioner instead of over SSH.
const ReasonHardware = "hardware"

// SkipSelf is why the machine careen runs on wasn't rebooted.
const SkipSelf = "this machine (careen runs here); pass --reboot-self to include it"

type RebootOptions struct {
	Mode string // reboot.Policy or reboot.Force
	// Self allows rebooting the machine careen runs on. Without it that
	// machine is always skipped, whatever its policy or --force.
	Self          bool
	Timeout       time.Duration
	HealthTimeout time.Duration
	Rolling       Rolling
}

// Rolling is how reboots run each host's reboot hooks (inventory.Host.Reboot).
type Rolling struct {
	// Ask decides what to do when a pre hook times out and the host's
	// onTimeout (or OnTimeout) is ask. nil answers abort.
	Ask contract.Ask
	// OnTimeout, when set, answers every pre timeout this way instead of
	// each host's onTimeout: force, skip or abort (--on-timeout).
	OnTimeout string
	// NoHooks reboots without running any hooks (--no-hooks).
	NoHooks bool
}

// RebootOutcome is what happens, or happened, to one host.
type RebootOutcome struct {
	Host     string          `json:"host"`
	Pending  bool            `json:"rebootPending"`
	Reasons  []string        `json:"rebootReasons"`
	Rebooted bool            `json:"rebooted"`
	Skip     string          `json:"skip,omitempty"`
	Error    *contract.Error `json:"error,omitempty"`
	// RebootOrder and Hooks describe the plan: the host's place in the
	// order, and what runs around its reboot.
	RebootOrder int      `json:"rebootOrder"`
	Hooks       []string `json:"hooks,omitempty"`
	// HookRuns is what the hooks did; Forced is set when a timed-out pre
	// was replaced by its force command.
	HookRuns []reboot.HookRun `json:"hookRuns,omitempty"`
	Forced   bool             `json:"forced,omitempty"`
	// Manual is what's left to do by hand, e.g. the post command after an
	// aborted pre.
	Manual string `json:"manual,omitempty"`
}

// RebootPlan is what Reboot will do; its JSON form is careen.reboot.plan/v1.
type RebootPlan struct {
	contract.Header
	Mode  string          `json:"mode"`
	Hosts []RebootOutcome `json:"hosts"` // in reboot order, then hosts that couldn't be checked
	// NoHooks: the hosts' reboot hooks won't run (--no-hooks).
	NoHooks bool `json:"noHooks"`

	ordered     []candidate
	outs        []RebootOutcome // aligned with ordered
	unreachable []RebootOutcome
	opts        RebootOptions
	fleet       []inventory.Host // every host, for hooks that run on:tag
}

// WithAsk sets how the plan's run decides a timed-out pre hook; the TUI
// sets its own prompt here.
func (p *RebootPlan) WithAsk(ask contract.Ask) { p.opts.Rolling.Ask = ask }

// Count is how many hosts the plan reboots.
func (p *RebootPlan) Count() int {
	n := 0
	for _, o := range p.outs {
		if o.Skip == "" {
			n++
		}
	}
	return n
}

// Targets are the hosts the plan reboots, in order.
func (p *RebootPlan) Targets() []inventory.Host {
	var out []inventory.Host
	for i, c := range p.ordered {
		if p.outs[i].Skip == "" {
			out = append(out, c.Host)
		}
	}
	return out
}

// PlanReboot probes the hosts sel selects and decides which to reboot.
// Hosts named in sel.Only count as named for rebootPolicy = manual.
func PlanReboot(ctx context.Context, inv *inventory.Inventory, sel inventory.Selector, opts RebootOptions) (*RebootPlan, error) {
	hosts, err := inv.Select(sel)
	if err != nil {
		return nil, err
	}
	hosts = InOnlyOrder(hosts, sel.Only)

	var cands []candidate
	var unreachable []RebootOutcome
	for _, o := range probe.New().ProbeAll(ctx, hosts) {
		if o.Err != nil {
			unreachable = append(unreachable, RebootOutcome{Host: o.Host.Name, Reasons: []string{}, Skip: "can't check it: " + o.Err.Message})
			continue
		}
		reasons := o.Result.RebootReasons()
		hooks, err := reboot.PrepareHooks(o.Host)
		if err != nil {
			return nil, contract.Usage("%v", err)
		}
		cands = append(cands, candidate{
			Candidate:    reboot.Candidate{Host: o.Host, Pending: len(reasons) > 0, Reasons: reasons, Named: slices.Contains(sel.Only, o.Host.Name), Local: IsLocal(o.Host)},
			failedBefore: o.Result.FailedUnits,
			hooks:        hooks,
		})
	}
	cands, unchecked := addPendingRestarts(ctx, inv.Flake, cands)
	unreachable = append(unreachable, unchecked...)
	ordered, outs := planReboots(cands, opts.Mode, sel.Only, opts.Self)
	return &RebootPlan{
		Header: contract.NewHeader(RebootPlanSchema), Mode: opts.Mode, NoHooks: opts.Rolling.NoHooks,
		Hosts:   append(slices.Clone(outs), unreachable...),
		ordered: ordered, outs: outs, unreachable: unreachable, opts: opts, fleet: inv.Hosts,
	}, nil
}

type RebootDone struct {
	Hosts []RebootOutcome `json:"hosts"`
}

// Reboot runs the plan, one host at a time, stopping at the first failure.
// It emits a done event and calls onDone once, just before rebooting the
// local machine if the plan includes it.
func Reboot(ctx context.Context, em *contract.Emitter, p *RebootPlan, onDone func(RebootDone)) (RebootDone, error) {
	var done RebootDone
	finish := func(outs []RebootOutcome) {
		done = RebootDone{Hosts: append(slices.Clone(outs), p.unreachable...)}
		em.Done(done)
		if onDone != nil {
			onDone(done)
		}
	}
	if p.Count() == 0 {
		finish(p.outs)
		return done, nil
	}
	em.Started("reboot", p)
	localRebooted := false
	outs, err := runReboots(ctx, em, p.ordered, slices.Clone(p.outs), rebootOptions(p.opts.Timeout, p.opts.HealthTimeout), p.opts.Rolling, p.fleet, func(outs []RebootOutcome) {
		localRebooted = true
		finish(outs)
	})
	if !localRebooted {
		finish(outs)
	}
	return done, err
}

// candidate is a host that may be rebooted, with its failed units
// beforehand for the health check afterwards.
type candidate struct {
	reboot.Candidate
	failedBefore []string
	// restart restarts the machine through its provisioner, when it holds
	// hardware changes until then; nil reboots over SSH.
	restart func(ctx context.Context) error
	// hooks are the host's reboot hooks, checked and rendered.
	hooks reboot.Hooks
}

// addPendingRestarts asks the provisioner of each candidate that has one
// able to restart machines (Proxmox) whether hardware changes wait for a
// restart. Those candidates need a reboot, for ReasonHardware, and get it
// through the provisioner. A host whose provisioner can't answer comes
// back in unchecked, skipped, like a host that can't be probed. The local
// machine is left alone: rebooting it ends careen either way.
func addPendingRestarts(ctx context.Context, flake inventory.Flake, cands []candidate) (checked []candidate, unchecked []RebootOutcome) {
	var ps *Provisioners
	for _, c := range cands {
		if c.Local || c.Host.ProvisionerName() == inventory.Existing {
			checked = append(checked, c)
			continue
		}
		if ps == nil {
			ps = NewProvisioners(&inventory.Inventory{Flake: flake}, nil)
		}
		prov, err := ps.For(c.Host)
		if err != nil {
			unchecked = append(unchecked, RebootOutcome{Host: c.Host.Name, Reasons: orEmpty(c.Reasons), Skip: "can't check its machine: " + err.Error()})
			continue
		}
		r, ok := prov.(provision.Restarter)
		if !ok {
			checked = append(checked, c)
			continue
		}
		pending, err := r.PendingRestart(ctx, c.Host)
		if err != nil {
			unchecked = append(unchecked, RebootOutcome{Host: c.Host.Name, Reasons: orEmpty(c.Reasons), Skip: "can't check its machine: " + err.Error()})
			continue
		}
		if len(pending) > 0 {
			h := c.Host
			c.Pending = true
			c.Reasons = append(slices.Clone(c.Reasons), ReasonHardware)
			c.restart = func(ctx context.Context) error { return r.Restart(ctx, h) }
		}
		checked = append(checked, c)
	}
	return checked, unchecked
}

func rebootOptions(timeout, healthTimeout time.Duration) reboot.Options {
	return reboot.Options{Timeout: timeout, HealthTimeout: healthTimeout}
}

// planReboots decides and orders the reboots.
func planReboots(cands []candidate, mode string, only []string, self bool) ([]candidate, []RebootOutcome) {
	byName := map[string]candidate{}
	var plain []reboot.Candidate
	for _, c := range cands {
		byName[c.Host.Name] = c
		plain = append(plain, c.Candidate)
	}
	var ordered []candidate
	outs := []RebootOutcome{}
	for _, c := range reboot.Order(plain, only) {
		d := reboot.Decide(c, mode)
		if d.Reboot && c.Local && !self {
			d.Reboot, d.Skip = false, SkipSelf
		}
		outs = append(outs, RebootOutcome{Host: c.Host.Name, Pending: c.Pending, Reasons: orEmpty(c.Reasons), Skip: d.Skip, RebootOrder: c.Host.RebootOrder, Hooks: byName[c.Host.Name].hooks.Summary()})
		ordered = append(ordered, byName[c.Host.Name])
	}
	return ordered, outs
}

// runReboots reboots the planned hosts one at a time and stops at the first
// failure. Around each reboot it runs the host's hooks: pre (whose timeout
// is decided by roll), the reboot, post, the gate, then settle. The local
// machine is always last: beforeLocal runs (to report the results so far)
// and then it reboots, ending careen, so its post and gate can't run.
func runReboots(ctx context.Context, em *contract.Emitter, ordered []candidate, outs []RebootOutcome, opts reboot.Options, roll Rolling, fleet []inventory.Host, beforeLocal func([]RebootOutcome)) ([]RebootOutcome, error) {
	var firstErr error
	fail := func(o *RebootOutcome, name string, err error) {
		e := contract.ErrorObject(contract.Failed("reboot", "%v", err))
		if contract.ExitCode(err) == contract.ExitPrecondition {
			e = contract.ErrorObject(err)
		}
		e.Host = name
		o.Error = &e
		em.Error(name, "reboot", err)
		if contract.ExitCode(err) == contract.ExitPrecondition {
			firstErr = err
		} else {
			firstErr = contract.Failed("reboot", "%s: %v", name, err)
		}
	}
	for i, c := range ordered {
		o := &outs[i]
		if o.Skip != "" {
			continue
		}
		if firstErr != nil {
			o.Skip = "not reached: an earlier reboot failed"
			continue
		}
		name := c.Host.Name
		hs := c.hooks
		if roll.NoHooks && !hs.Empty() {
			em.Progress(name, "hooks", "%s", "--no-hooks: rebooting without its reboot hooks")
			hs = reboot.Hooks{}
		}
		run := hookRunner(fleet, c.Host)

		if hs.Pre != "" {
			skipped, err := runPre(ctx, em, roll, c.Host, hs, run, o)
			if err != nil {
				fail(o, name, err)
				continue
			}
			if skipped {
				continue
			}
		}

		if c.Local {
			if hs.Post != "" || hs.Gate != "" {
				em.Progress(name, "hooks", "%s", "this machine's post and gate can't run: careen stops when it reboots")
			}
			o.Rebooted = true
			em.Progress(name, "reboot", "rebooting this machine; careen stops here")
			beforeLocal(outs)
			if _, err := activate.Run(ctx, sshx.LocalHost{}, "reboot"); err != nil {
				return outs, contract.Failed("reboot", "rebooting %s: %v", name, err)
			}
			return outs, nil
		}
		ro := opts
		ro.Restart = c.restart
		_, err := reboot.Reboot(ctx, Conn(c.Host, true), c.failedBefore, ro, func(step, msg string) {
			em.Progress(name, step, "%s", msg)
		})
		if err != nil {
			o.Manual = undoPre(hs)
			fail(o, name, err)
			continue
		}
		o.Rebooted = true
		em.Progress(name, "reboot", "back and healthy")

		if err := runAfter(ctx, em, c.Host, hs, run, o); err != nil {
			fail(o, name, err)
			continue
		}
	}
	return outs, firstErr
}

// runPre runs h's pre hook, and when it times out decides what to do. It
// reports skipped when the answer was skip (post has then undone pre), and
// an error when the run should stop.
func runPre(ctx context.Context, em *contract.Emitter, roll Rolling, h inventory.Host, hs reboot.Hooks, run reboot.Runner, o *RebootOutcome) (skipped bool, err error) {
	cmd, hook := hs.Pre, reboot.HookPre
	for {
		em.Progress(h.Name, hook, "running on %s", hs.PreOn)
		r, err := reboot.RunHook(ctx, run, hook, hs.PreOn, cmd, hs.PreTimeout)
		o.HookRuns = append(o.HookRuns, r)
		if err == nil {
			o.Forced = hook == reboot.HookForce
			if o.Forced {
				em.Progress(h.Name, hook, "%s", "forced")
			}
			return false, nil
		}
		if !errors.Is(err, reboot.ErrHookTimeout) || hook == reboot.HookForce {
			o.Manual = undoPre(hs)
			return false, err
		}
		choice, err := timeoutChoice(ctx, roll, h, hs, r)
		if err != nil {
			o.Manual = undoPre(hs)
			return false, err
		}
		em.Progress(h.Name, reboot.HookPre, "timed out; answer: %s", choice)
		switch choice {
		case reboot.TimeoutWait:
		case reboot.TimeoutForce:
			cmd, hook = hs.Force, reboot.HookForce
		case reboot.TimeoutSkip:
			if hs.Post != "" {
				em.Progress(h.Name, reboot.HookPost, "undoing pre on %s", hs.PostOn)
				r, err := reboot.RunHook(ctx, run, reboot.HookPost, hs.PostOn, hs.Post, hs.PostTimeout)
				o.HookRuns = append(o.HookRuns, r)
				if err != nil {
					o.Manual = undoPre(hs)
					return false, fmt.Errorf("skipping it, undoing pre: %w", err)
				}
			}
			o.Skip = "pre timed out; skipped, not rebooted"
			return true, nil
		default:
			o.Manual = undoPre(hs)
			return false, contract.Failed("reboot", "%s; aborted, so the host is left as pre left it", preTimedOut(hs, o.HookRuns[len(o.HookRuns)-1]))
		}
	}
}

// timeoutChoice decides what to do about a timed-out pre: --on-timeout,
// else the host's onTimeout, else ask.
func timeoutChoice(ctx context.Context, roll Rolling, h inventory.Host, hs reboot.Hooks, r reboot.HookRun) (string, error) {
	choice := hs.OnTimeout
	if roll.OnTimeout != "" {
		choice = roll.OnTimeout
	}
	if choice == reboot.TimeoutForce && hs.Force == "" {
		return "", contract.Usage("%s has no pre.force to force with", h.Name)
	}
	if choice != "" && choice != reboot.TimeoutAsk {
		return choice, nil
	}
	if roll.Ask == nil {
		return reboot.TimeoutAbort, nil
	}
	opts := []string{reboot.TimeoutWait}
	if hs.Force != "" {
		opts = append(opts, reboot.TimeoutForce)
	}
	opts = append(opts, reboot.TimeoutSkip, reboot.TimeoutAbort)
	msg := preTimedOut(hs, r)
	if r.Output != "" {
		msg += " (" + r.Output + ")"
	}
	return roll.Ask(ctx, contract.Question{Host: h.Name, Step: reboot.HookPre, Message: msg, Options: opts})
}

// preTimedOut says how pre timed out: careen's timeout, or the command's own
// (exit 124).
func preTimedOut(hs reboot.Hooks, r reboot.HookRun) string {
	if r.ExitCode == reboot.ExitTimedOut {
		return "pre timed out (its own timeout)"
	}
	return fmt.Sprintf("pre timed out after %s", hs.PreTimeout)
}

// runAfter runs post, then waits for the gate and settles.
func runAfter(ctx context.Context, em *contract.Emitter, h inventory.Host, hs reboot.Hooks, run reboot.Runner, o *RebootOutcome) error {
	if hs.Post != "" {
		em.Progress(h.Name, reboot.HookPost, "running on %s", hs.PostOn)
		r, err := reboot.RunHook(ctx, run, reboot.HookPost, hs.PostOn, hs.Post, hs.PostTimeout)
		o.HookRuns = append(o.HookRuns, r)
		if err != nil {
			o.Manual = undoPre(hs)
			return err
		}
	}
	if hs.Gate != "" {
		em.Progress(h.Name, reboot.HookGate, "waiting on %s, up to %s", hs.GateOn, hs.GateTimeout)
		r, err := reboot.WaitGate(ctx, run, hs.GateOn, hs.Gate, hs.GateTimeout, hs.GateInterval, func(msg string) {
			em.Progress(h.Name, reboot.HookGate, "%s", msg)
		})
		o.HookRuns = append(o.HookRuns, r)
		if err != nil {
			return err
		}
		em.Progress(h.Name, reboot.HookGate, "%s", "passed")
	}
	if hs.Settle > 0 {
		em.Progress(h.Name, "settle", "waiting %s", hs.Settle)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(hs.Settle):
		}
	}
	return nil
}

// undoPre says how to undo pre by hand: run post where it runs.
func undoPre(hs reboot.Hooks) string {
	if hs.Pre == "" || hs.Post == "" {
		return ""
	}
	return fmt.Sprintf("run post on %s to put the host back: %s", hs.PostOn, hs.Post)
}

// hookOutputLimit caps what careen keeps of a hook's output.
const hookOutputLimit = 64 << 10

// hookRunner runs hook commands for target: on itself (self), on the
// machine careen runs on (local), or on the first other host with a tag
// that answers (tag:<tag>).
func hookRunner(fleet []inventory.Host, target inventory.Host) reboot.Runner {
	return func(ctx context.Context, on, cmd string) (sshx.Output, error) {
		h, err := hookHost(ctx, fleet, target, on)
		if err != nil {
			return sshx.Output{}, err
		}
		return sshx.Capture(ctx, h, hookOutputLimit, cmd)
	}
}

func hookHost(ctx context.Context, fleet []inventory.Host, target inventory.Host, on string) (sshx.Host, error) {
	switch {
	case on == reboot.OnLocal:
		return sshx.LocalHost{}, nil
	case strings.HasPrefix(on, reboot.OnTagPref):
		tag := strings.TrimPrefix(on, reboot.OnTagPref)
		for _, h := range fleet {
			if h.Name == target.Name || !slices.Contains(h.Tags, tag) {
				continue
			}
			conn := Conn(h, false)
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := conn.Run(pctx, "true")
			cancel()
			if err == nil {
				return conn, nil
			}
		}
		return nil, fmt.Errorf("no other host tagged %s answers", tag)
	}
	// self: a fresh connection, since the host reboots in between.
	return Conn(target, true), nil
}
