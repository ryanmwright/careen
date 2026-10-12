package reboot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshx"
)

func TestPrepareHooks(t *testing.T) {
	h := inventory.Host{Name: "web1", Address: "10.0.0.21", Reboot: inventory.RebootHooks{
		Pre:    inventory.Hook{Run: "lb disable {{.Name}}", Timeout: "2m", OnTimeout: "ask", Force: "lb disable --now {{.Name}}"},
		Post:   inventory.Hook{Run: "lb enable {{.Address}}", On: "tag:lb"},
		Gate:   inventory.Hook{Run: "curl -fs http://{{.Address}}/health", On: "local", Timeout: "5m", Interval: "3s"},
		Settle: "15s",
	}}
	hs, err := PrepareHooks(h)
	if err != nil {
		t.Fatal(err)
	}
	if hs.Pre != "lb disable web1" || hs.Force != "lb disable --now web1" || hs.Post != "lb enable 10.0.0.21" || hs.Gate != "curl -fs http://10.0.0.21/health" {
		t.Errorf("rendered = %+v", hs)
	}
	if hs.PreOn != OnSelf || hs.PostOn != "tag:lb" || hs.GateOn != OnLocal {
		t.Errorf("on = %s %s %s", hs.PreOn, hs.PostOn, hs.GateOn)
	}
	if hs.PreTimeout != 2*time.Minute || hs.PostTimeout != DefaultPostTimeout || hs.GateTimeout != 5*time.Minute || hs.GateInterval != 3*time.Second || hs.Settle != 15*time.Second {
		t.Errorf("durations = %+v", hs)
	}
	if got := strings.Join(hs.Summary(), "; "); got != "pre on self (force available); post on tag:lb; gate on local (up to 5m0s); settle 15s" {
		t.Errorf("summary = %q", got)
	}
	if hs, err := PrepareHooks(inventory.Host{Name: "x"}); err != nil || !hs.Empty() {
		t.Errorf("no hooks: %+v, %v", hs, err)
	}

	for field, r := range map[string]inventory.RebootHooks{
		"pre.run":       {Pre: inventory.Hook{Run: "drain {{.Nmae}}"}},
		"pre.timeout":   {Pre: inventory.Hook{Run: "x", Timeout: "soon"}},
		"post.on":       {Post: inventory.Hook{Run: "x", On: "elsewhere"}},
		"gate.interval": {Gate: inventory.Hook{Run: "x", Interval: "-1s"}},
		"settle":        {Settle: "0s"},
		"pre.onTimeout": {Pre: inventory.Hook{Run: "x", OnTimeout: "force"}}, // force without pre.force
	} {
		_, err := PrepareHooks(inventory.Host{Name: "web1", Reboot: r})
		if err == nil || !strings.Contains(err.Error(), "web1: reboot."+field) {
			t.Errorf("%s: err = %v", field, err)
		}
	}
}

// script answers a hook's runs in turn: exit codes, or a sleep past the
// timeout for "hang".
func script(steps ...string) (Runner, *[]string) {
	var calls []string
	i := 0
	return func(ctx context.Context, on, cmd string) (sshx.Output, error) {
		calls = append(calls, on+": "+cmd)
		step := steps[min(i, len(steps)-1)]
		i++
		switch step {
		case "ok":
			return sshx.Output{Stdout: []byte("fine\n")}, nil
		case "fail":
			return sshx.Output{ExitCode: 1, Stdout: []byte("progress\n"), Stderr: []byte("not ready yet\n")}, nil
		case "hang":
			<-ctx.Done()
			return sshx.Output{}, ctx.Err()
		case "unreachable":
			return sshx.Output{ExitCode: 255}, sshx.ErrUnreachable
		}
		panic(step)
	}, &calls
}

func TestRunHook(t *testing.T) {
	ctx := context.Background()
	run, calls := script("ok")
	if r, err := RunHook(ctx, run, HookPre, OnSelf, "drain", time.Second); err != nil || r.Output != "fine" {
		t.Errorf("ok: %+v, %v", r, err)
	}
	if (*calls)[0] != "self: drain" {
		t.Errorf("calls = %v", *calls)
	}

	run, _ = script("fail")
	if r, err := RunHook(ctx, run, HookPost, OnSelf, "x", time.Second); err == nil || r.ExitCode != 1 || !strings.Contains(err.Error(), "post exited 1: not ready yet") {
		t.Errorf("fail: %+v, %v", r, err)
	}

	run, _ = script("hang")
	r, err := RunHook(ctx, run, HookPre, OnSelf, "x", 20*time.Millisecond)
	if !errors.Is(err, ErrHookTimeout) || !r.TimedOut {
		t.Errorf("hang: %+v, %v", r, err)
	}

	// A command that timed out by itself, as timeout(1) reports it.
	run = func(context.Context, string, string) (sshx.Output, error) {
		return sshx.Output{ExitCode: ExitTimedOut, Stderr: []byte("still evicting\n")}, nil
	}
	if r, err := RunHook(ctx, run, HookPre, OnSelf, "timeout 5m drain", time.Second); !errors.Is(err, ErrHookTimeout) || !r.TimedOut || !strings.Contains(err.Error(), "still evicting") {
		t.Errorf("exit 124: %+v, %v", r, err)
	}

	run, _ = script("unreachable")
	if _, err := RunHook(ctx, run, HookPre, "tag:lb", "x", time.Second); !errors.Is(err, sshx.ErrUnreachable) || errors.Is(err, ErrHookTimeout) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestWaitGate(t *testing.T) {
	ctx := context.Background()
	run, calls := script("fail", "fail", "ok")
	var progress []string
	if _, err := WaitGate(ctx, run, OnSelf, "check", time.Second, time.Millisecond, func(m string) { progress = append(progress, m) }); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || len(progress) != 2 || !strings.Contains(progress[0], "(try 1): not ready yet") {
		t.Errorf("calls %v, progress %v", *calls, progress)
	}

	run, _ = script("fail")
	r, err := WaitGate(ctx, run, OnSelf, "check", 30*time.Millisecond, 5*time.Millisecond, func(string) {})
	if err == nil || !r.TimedOut || !strings.Contains(err.Error(), "didn't pass within 30ms: not ready yet") {
		t.Errorf("never passes: %+v, %v", r, err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	run, _ = script("fail")
	if _, err := WaitGate(cctx, run, OnSelf, "check", time.Second, time.Millisecond, func(string) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}
