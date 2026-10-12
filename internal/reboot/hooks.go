package reboot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshx"
)

// Hook names, as they appear in progress, plans and results.
const (
	HookPre   = "pre"
	HookForce = "force"
	HookPost  = "post"
	HookGate  = "gate"
)

// Where a hook runs (inventory.Hook.On).
const (
	OnSelf    = "self"
	OnLocal   = "local"
	OnTagPref = "tag:"
)

// Answers to a pre hook that timed out (inventory.Hook.OnTimeout, and the
// options careen asks with).
const (
	TimeoutAsk   = "ask"
	TimeoutWait  = "wait"
	TimeoutForce = "force"
	TimeoutSkip  = "skip"
	TimeoutAbort = "abort"
)

// Defaults for hooks that don't say.
const (
	DefaultPreTimeout   = 10 * time.Minute
	DefaultPostTimeout  = 5 * time.Minute
	DefaultGateTimeout  = 10 * time.Minute
	DefaultGateInterval = 10 * time.Second
)

// ErrHookTimeout is returned when a hook ran out of time: careen's timeout,
// or the command's own, signalled by exit code 124 as timeout(1) does.
var ErrHookTimeout = errors.New("timed out")

// ExitTimedOut is the exit code that means a command timed out by itself.
// Wrapping a hook in timeout(1), shorter than the hook's timeout, ends it
// cleanly on the host, which careen can't do once its ssh is gone.
const ExitTimedOut = 124

// Runner runs cmd where on says (OnSelf, OnLocal or a tag:), for the host
// being rebooted.
type Runner func(ctx context.Context, on, cmd string) (sshx.Output, error)

// HookRun is what one run of a hook did.
type HookRun struct {
	Hook     string  `json:"hook"`
	On       string  `json:"on"`
	Seconds  float64 `json:"seconds"`
	ExitCode int     `json:"exitCode"`
	TimedOut bool    `json:"timedOut,omitempty"`
	// Output is the last line it printed (stderr if it failed and printed
	// there), to say why without dumping everything.
	Output string `json:"output,omitempty"`
}

// Hooks are a host's reboot hooks, checked and with their commands
// rendered for that host.
type Hooks struct {
	Pre, Force, Post, Gate string // rendered commands, "" when unset
	PreOn, PostOn, GateOn  string
	PreTimeout             time.Duration
	PostTimeout            time.Duration
	GateTimeout            time.Duration
	GateInterval           time.Duration
	Settle                 time.Duration
	OnTimeout              string // "" means ask
}

// Empty reports whether there's nothing to run around the reboot.
func (h Hooks) Empty() bool {
	return h.Pre == "" && h.Post == "" && h.Gate == "" && h.Settle == 0
}

// Summary describes the hooks for a plan: "pre on self, post on self,
// gate on tag:db (10m), settle 15s".
func (h Hooks) Summary() []string {
	var out []string
	if h.Pre != "" {
		s := "pre on " + h.PreOn
		if h.Force != "" {
			s += " (force available)"
		}
		out = append(out, s)
	}
	if h.Post != "" {
		out = append(out, "post on "+h.PostOn)
	}
	if h.Gate != "" {
		out = append(out, fmt.Sprintf("gate on %s (up to %s)", h.GateOn, h.GateTimeout))
	}
	if h.Settle > 0 {
		out = append(out, "settle "+h.Settle.String())
	}
	return out
}

// PrepareHooks checks h's reboot hooks and renders their commands. Any
// mistake (a bad duration, an unknown on or onTimeout, a template that
// doesn't render) is reported with the host and field, before anything
// runs.
func PrepareHooks(h inventory.Host) (Hooks, error) {
	r := h.Reboot
	var hs Hooks
	var err error
	fail := func(field string, e error) (Hooks, error) {
		return Hooks{}, fmt.Errorf("%s: reboot.%s: %w", h.Name, field, e)
	}
	render := func(field, cmd string) (string, error) {
		if cmd == "" {
			return "", nil
		}
		t, err := template.New(field).Option("missingkey=error").Parse(cmd)
		if err != nil {
			return "", err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, struct{ Name, Address string }{h.Name, h.Address}); err != nil {
			return "", err
		}
		return b.String(), nil
	}
	dur := func(s string, def time.Duration) (time.Duration, error) {
		if s == "" {
			return def, nil
		}
		d, err := time.ParseDuration(s)
		if err == nil && d <= 0 {
			err = errors.New("must be positive")
		}
		return d, err
	}
	on := func(s string) (string, error) {
		switch {
		case s == "" || s == OnSelf:
			return OnSelf, nil
		case s == OnLocal:
			return OnLocal, nil
		case strings.HasPrefix(s, OnTagPref) && len(s) > len(OnTagPref):
			return s, nil
		}
		return "", fmt.Errorf("%q isn't self, local or tag:<tag>", s)
	}

	if hs.Pre, err = render("pre.run", r.Pre.Run); err != nil {
		return fail("pre.run", err)
	}
	if hs.Force, err = render("pre.force", r.Pre.Force); err != nil {
		return fail("pre.force", err)
	}
	if hs.Post, err = render("post.run", r.Post.Run); err != nil {
		return fail("post.run", err)
	}
	if hs.Gate, err = render("gate.run", r.Gate.Run); err != nil {
		return fail("gate.run", err)
	}
	if hs.PreOn, err = on(r.Pre.On); err != nil {
		return fail("pre.on", err)
	}
	if hs.PostOn, err = on(r.Post.On); err != nil {
		return fail("post.on", err)
	}
	if hs.GateOn, err = on(r.Gate.On); err != nil {
		return fail("gate.on", err)
	}
	if hs.PreTimeout, err = dur(r.Pre.Timeout, DefaultPreTimeout); err != nil {
		return fail("pre.timeout", err)
	}
	if hs.PostTimeout, err = dur(r.Post.Timeout, DefaultPostTimeout); err != nil {
		return fail("post.timeout", err)
	}
	if hs.GateTimeout, err = dur(r.Gate.Timeout, DefaultGateTimeout); err != nil {
		return fail("gate.timeout", err)
	}
	if hs.GateInterval, err = dur(r.Gate.Interval, DefaultGateInterval); err != nil {
		return fail("gate.interval", err)
	}
	if r.Settle != "" {
		if hs.Settle, err = dur(r.Settle, 0); err != nil {
			return fail("settle", err)
		}
	}
	switch r.Pre.OnTimeout {
	case "", TimeoutAsk, TimeoutForce, TimeoutSkip, TimeoutAbort:
		hs.OnTimeout = r.Pre.OnTimeout
	default:
		return fail("pre.onTimeout", fmt.Errorf("%q isn't ask, force, skip or abort", r.Pre.OnTimeout))
	}
	if hs.OnTimeout == TimeoutForce && hs.Force == "" {
		return fail("pre.onTimeout", errors.New("is force, but pre.force isn't set"))
	}
	return hs, nil
}

// RunHook runs cmd where on says, within timeout. A non-zero exit is an
// error with the last line it printed; running out of time, or exiting
// ExitTimedOut, is ErrHookTimeout.
func RunHook(ctx context.Context, run Runner, hook, on, cmd string, timeout time.Duration) (HookRun, error) {
	start := time.Now()
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run(hctx, on, cmd)
	r := HookRun{Hook: hook, On: on, Seconds: time.Since(start).Round(100 * time.Millisecond).Seconds(), ExitCode: out.ExitCode, Output: lastLine(out)}
	switch {
	case ctx.Err() != nil:
		return r, ctx.Err()
	case errors.Is(hctx.Err(), context.DeadlineExceeded):
		r.TimedOut = true
		return r, fmt.Errorf("%s %w after %s", hook, ErrHookTimeout, timeout)
	case err != nil:
		return r, fmt.Errorf("%s on %s: %w", hook, on, err)
	case out.ExitCode == ExitTimedOut:
		r.TimedOut = true
		return r, fmt.Errorf("%s %w (exit %d): %s", hook, ErrHookTimeout, ExitTimedOut, orDash(r.Output))
	case out.ExitCode != 0:
		return r, fmt.Errorf("%s exited %d: %s", hook, out.ExitCode, r.Output)
	}
	return r, nil
}

// WaitGate reruns cmd every interval until it exits 0, within timeout.
// progress is told about each failed try.
func WaitGate(ctx context.Context, run Runner, on, cmd string, timeout, interval time.Duration, progress func(msg string)) (HookRun, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	for try := 1; ; try++ {
		left := time.Until(deadline)
		if left <= 0 {
			left = time.Millisecond
		}
		r, err := RunHook(ctx, run, HookGate, on, cmd, left)
		r.Seconds = time.Since(start).Round(100 * time.Millisecond).Seconds()
		if err == nil {
			return r, nil
		}
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		if !time.Now().Before(deadline) {
			r.TimedOut = true
			return r, fmt.Errorf("gate didn't pass within %s: %s", timeout, orDash(r.Output))
		}
		progress(fmt.Sprintf("gate not passing yet (try %d): %s", try, orDash(r.Output)))
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(min(interval, time.Until(deadline))):
		}
	}
}

// lastLine is the last non-empty line of the output that explains it best:
// stderr for a failure that printed there, else stdout.
func lastLine(out sshx.Output) string {
	pick := out.Stdout
	if out.ExitCode != 0 && len(bytes.TrimSpace(out.Stderr)) > 0 {
		pick = out.Stderr
	}
	lines := strings.Split(strings.TrimSpace(string(pick)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
