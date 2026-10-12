// Package activate switches a host to a new system with a rollback
// watchdog, checks its health, and rolls back. It also holds the small
// remote actions reboots need (boot id, reboot, sudo check).
package activate

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/sshx"
)

//go:embed activate.sh
var script string

// Run runs one action of the remote script.
func Run(ctx context.Context, h sshx.Host, action string, args ...string) ([]byte, error) {
	return h.Run(ctx, script, append([]string{action}, args...)...)
}

// State is a host's systems before a change.
type State struct {
	Current string // /run/current-system
	Profile string // what the system profile points to (applied at boot)
	Failed  []string
}

func GetState(ctx context.Context, h sshx.Host) (*State, error) {
	out, err := Run(ctx, h, "state")
	if err != nil {
		return nil, err
	}
	kv := parse(out)
	return &State{Current: kv.one("current"), Profile: kv.one("profile"), Failed: kv.all("failed_unit")}, nil
}

// Health is a host's state after a change.
type Health struct {
	SystemState string   `json:"systemState"`
	Current     string   `json:"current"`
	Failed      []string `json:"failedUnits"`
	NewFailed   []string `json:"newFailedUnits"`
}

// OK reports whether the host is healthy running want: fully up, or
// degraded only by units that had already failed before the change.
func (h *Health) OK(want string) (bool, string) {
	switch {
	case want != "" && h.Current != want:
		return false, "running " + h.Current + ", not " + want
	case len(h.NewFailed) > 0:
		return false, "units failed: " + strings.Join(h.NewFailed, ", ")
	case h.SystemState == "running", h.SystemState == "degraded":
		return true, ""
	case h.SystemState == "":
		return false, "system didn't settle in time"
	default:
		return false, "system is " + h.SystemState
	}
}

// CheckHealth waits up to timeout for the host to settle and compares its
// failed units with before.
func CheckHealth(ctx context.Context, h sshx.Host, before []string, timeout time.Duration) (*Health, error) {
	out, err := Run(ctx, h, "health", strconv.Itoa(max(int(timeout.Seconds()), 1)))
	if err != nil {
		return nil, err
	}
	kv := parse(out)
	hl := &Health{SystemState: kv.one("system_state"), Current: kv.one("current"), Failed: kv.all("failed_unit"), NewFailed: []string{}}
	for _, u := range hl.Failed {
		if !slices.Contains(before, u) {
			hl.NewFailed = append(hl.NewFailed, u)
		}
	}
	return hl, nil
}

// Statuses of a host after Activate.
const (
	Switched   = "switched"  // running the new system, healthy
	Staged     = "staged"    // operation boot: applied at next reboot
	Unchanged  = "unchanged" // already on the new system
	RolledBack = "rolled-back"
)

type Options struct {
	Operation string // switch or boot
	// Watchdog is how long the host waits for careen's confirmation
	// before rolling back by itself. Zero turns it off.
	Watchdog      time.Duration
	HealthTimeout time.Duration
	// Settle is how long to wait after the switch before the health check,
	// so a unit that fails shortly after starting is caught.
	Settle time.Duration
	// Poll is the wait between connection attempts; zero means 5s.
	Poll time.Duration
}

type Result struct {
	Status string  `json:"status"`
	From   string  `json:"from,omitempty"`
	To     string  `json:"to"`
	Health *Health `json:"health,omitempty"`
	// Reason says why it rolled back.
	Reason string `json:"reason,omitempty"`
}

// Activate switches h to newPath. fresh must reach the same host over a
// new connection; it is used for the health check, so a switch that breaks
// SSH can't pass it. An error means the host may be in an unknown state;
// a rollback that succeeded is a Result with Status RolledBack and an
// error describing why.
func Activate(ctx context.Context, h, fresh sshx.Host, newPath string, opts Options, progress func(step, msg string)) (Result, error) {
	st, err := GetState(ctx, h)
	if err != nil {
		return Result{To: newPath}, fmt.Errorf("reading state: %w", err)
	}
	res := Result{From: st.Current, To: newPath}
	if opts.Operation == "boot" {
		if st.Profile == newPath {
			res.Status = Unchanged
			return res, nil
		}
		progress("activate", "installing for next boot")
		if _, err := Run(ctx, h, "switch", newPath, st.Current, "boot", "0", "-"); err != nil {
			return res, fmt.Errorf("switch-to-configuration boot: %w", err)
		}
		res.Status = Staged
		return res, nil
	}
	if st.Current == newPath {
		res.Status = Unchanged
		return res, nil
	}

	unit := "careen-rollback-" + randomID()
	secs := strconv.Itoa(int(opts.Watchdog.Seconds()))
	if opts.Watchdog > 0 {
		progress("activate", fmt.Sprintf("switching (rolls back by itself after %s unless confirmed)", opts.Watchdog))
	} else {
		progress("activate", "switching")
	}
	_, switchErr := Run(ctx, h, "switch", newPath, st.Current, "switch", secs, unit)

	if switchErr != nil && !errors.Is(switchErr, sshx.ErrUnreachable) {
		// switch-to-configuration reported a failure: roll back now.
		return rollback(ctx, h, res, unit, "switch failed: "+switchErr.Error(), progress)
	}
	if switchErr != nil {
		progress("activate", "lost the connection during the switch; reconnecting")
	}

	if opts.Settle > 0 {
		progress("health", fmt.Sprintf("letting it settle for %s", opts.Settle))
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(opts.Settle):
		}
	}
	progress("health", "checking over a new connection")
	hl, err := waitHealth(ctx, fresh, st.Failed, opts)
	if err != nil {
		if opts.Watchdog > 0 {
			return res, fmt.Errorf("host unreachable after the switch; its watchdog should have rolled it back: %w", err)
		}
		return res, fmt.Errorf("host unreachable after the switch: %w", err)
	}
	res.Health = hl
	if hl.Current == st.Current {
		res.Status = RolledBack
		return res, errors.New("the watchdog rolled the host back before careen could confirm")
	}
	if ok, why := hl.OK(newPath); !ok {
		return rollback(ctx, fresh, res, unit, why, progress)
	}
	if _, err := Run(ctx, fresh, "confirm", unit); err != nil {
		return res, fmt.Errorf("stopping the rollback watchdog %s: %w", unit, err)
	}
	res.Status = Switched
	return res, nil
}

// waitHealth retries the health check while the host is unreachable, up
// to the watchdog (plus a margin) so a rollback by the watchdog is seen.
func waitHealth(ctx context.Context, h sshx.Host, before []string, opts Options) (*Health, error) {
	deadline := time.Now().Add(opts.Watchdog + opts.HealthTimeout + 30*time.Second)
	for {
		hl, err := CheckHealth(ctx, h, before, opts.HealthTimeout)
		if err == nil || !errors.Is(err, sshx.ErrUnreachable) || time.Now().After(deadline) {
			return hl, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(cmp.Or(opts.Poll, 5*time.Second)):
		}
	}
}

func rollback(ctx context.Context, h sshx.Host, res Result, unit, why string, progress func(step, msg string)) (Result, error) {
	progress("rollback", "rolling back: "+why)
	if _, err := Run(ctx, h, "rollback", res.From, unit); err != nil {
		return res, fmt.Errorf("%s; rollback failed: %w", why, err)
	}
	res.Status, res.Reason = RolledBack, why
	return res, errors.New(why)
}

// RollbackGeneration switches h to its previous system generation and
// checks its health.
func RollbackGeneration(ctx context.Context, h, fresh sshx.Host, healthTimeout time.Duration, progress func(step, msg string)) (Result, error) {
	st, err := GetState(ctx, h)
	if err != nil {
		return Result{}, fmt.Errorf("reading state: %w", err)
	}
	progress("rollback", "switching to the previous generation")
	if _, err := Run(ctx, h, "rollback-generation"); err != nil && !errors.Is(err, sshx.ErrUnreachable) {
		return Result{From: st.Current}, fmt.Errorf("rollback: %w", err)
	}
	progress("health", "checking over a new connection")
	hl, err := waitHealth(ctx, fresh, st.Failed, Options{HealthTimeout: healthTimeout})
	if err != nil {
		return Result{From: st.Current}, err
	}
	res := Result{Status: RolledBack, From: st.Current, To: hl.Current, Health: hl}
	if ok, why := hl.OK(""); !ok {
		return res, errors.New(why)
	}
	return res, nil
}

func randomID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type kv map[string][]string

func parse(out []byte) kv {
	m := kv{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			m[k] = append(m[k], strings.TrimSpace(v))
		}
	}
	return m
}

func (m kv) one(k string) string {
	if v := m[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (m kv) all(k string) []string {
	if v := m[k]; v != nil {
		return v
	}
	return []string{}
}
