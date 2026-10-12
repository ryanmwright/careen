// Package reboot decides which hosts to reboot and reboots them one at a
// time, waiting for each to come back healthy.
package reboot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/activate"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshx"
)

// Modes.
const (
	// Policy reboots hosts that need it and whose rebootPolicy allows it.
	Policy = "policy"
	// Force reboots every candidate whose policy isn't never, needed or not.
	Force = "force"
)

// Candidate is a host that might be rebooted.
type Candidate struct {
	Host    inventory.Host
	Pending bool     // a reboot is needed
	Reasons []string // why: kernel, initrd, kernel-modules, staged
	Named   bool     // named with --only
	Local   bool     // the machine careen runs on
}

// Decision is whether a candidate is rebooted, and why not.
type Decision struct {
	Candidate
	Reboot bool   `json:"reboot"`
	Skip   string `json:"skip,omitempty"`
}

// Decide applies the reboot policy:
//
//	never   never rebooted
//	manual  only when named with --only (Force counts as explicit)
//	auto    rebooted by --reboot and careen reboot
//
// In Policy mode a host must also need a reboot.
func Decide(c Candidate, mode string) Decision {
	d := Decision{Candidate: c}
	switch {
	case c.Host.RebootPolicy == "never":
		d.Skip = "rebootPolicy is never"
	case c.Host.RebootPolicy == "manual" && !c.Named && mode != Force:
		d.Skip = "rebootPolicy is manual; name it with --only"
	case mode == Policy && !c.Pending:
		d.Skip = "no reboot needed"
	default:
		d.Reboot = true
	}
	return d
}

// Order sorts candidates for rebooting: in the order of names when given
// (the --only list), otherwise by rebootOrder and then host name, with the
// local machine last.
func Order(cs []Candidate, names []string) []Candidate {
	out := slices.Clone(cs)
	rank := func(c Candidate) int {
		if c.Local {
			return len(names) + 1
		}
		if i := slices.Index(names, c.Host.Name); i >= 0 {
			return i
		}
		return len(names)
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		if r := rank(a) - rank(b); r != 0 {
			return r
		}
		if r := a.Host.RebootOrder - b.Host.RebootOrder; r != 0 {
			return r
		}
		return strings.Compare(a.Host.Name, b.Host.Name)
	})
	return out
}

type Options struct {
	// Timeout bounds how long a host may take to come back.
	Timeout       time.Duration
	HealthTimeout time.Duration
	// Poll is the wait between connection attempts; zero means 5s.
	Poll time.Duration
	// Restart, when set, restarts the machine from outside (through its
	// provisioner) instead of running reboot on it, so hardware changes
	// waiting for that restart apply.
	Restart func(ctx context.Context) error
}

// Reboot reboots h and waits until it is back on a new boot and healthy.
// before is its failed units before the reboot. h must not share a
// ControlMaster connection, which wouldn't survive the reboot.
func Reboot(ctx context.Context, h sshx.Host, before []string, opts Options, progress func(step, msg string)) (*activate.Health, error) {
	poll := opts.Poll
	if poll == 0 {
		poll = 5 * time.Second
	}
	out, err := activate.Run(ctx, h, "boot-id")
	if err != nil {
		return nil, fmt.Errorf("reading boot id: %w", err)
	}
	bootID := strings.TrimSpace(string(out))

	if opts.Restart != nil {
		progress("reboot", "restarting the machine from outside, to apply its pending hardware changes")
		if err := opts.Restart(ctx); err != nil {
			return nil, fmt.Errorf("restart: %w", err)
		}
	} else {
		progress("reboot", "rebooting")
		// The connection usually drops while systemctl reboot runs.
		if _, err := activate.Run(ctx, h, "reboot"); err != nil && !errors.Is(err, sshx.ErrUnreachable) {
			return nil, fmt.Errorf("reboot: %w", err)
		}
	}

	progress("reboot", fmt.Sprintf("waiting up to %s for it to come back", opts.Timeout))
	deadline := time.Now().Add(opts.Timeout)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
		out, err := activate.Run(ctx, h, "boot-id")
		if err == nil && strings.TrimSpace(string(out)) != bootID {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("not back on a new boot within %s", opts.Timeout)
		}
	}

	progress("health", "back; checking health")
	hl, err := activate.CheckHealth(ctx, h, before, opts.HealthTimeout)
	if err != nil {
		return nil, fmt.Errorf("health check: %w", err)
	}
	if ok, why := hl.OK(""); !ok {
		return hl, errors.New(why)
	}
	return hl, nil
}
