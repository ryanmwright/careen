package actions

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/reboot"
	"github.com/ryanmwright/careen/internal/sshx"
)

// fakeRun answers hook commands by name: "hang" outlasts its timeout,
// "fail" exits 1, anything else succeeds. It records what ran where.
type fakeRun struct {
	mu    sync.Mutex
	ran   []string
	plays map[string][]string // command -> answers in turn
}

func (f *fakeRun) run(ctx context.Context, on, cmd string) (sshx.Output, error) {
	f.mu.Lock()
	f.ran = append(f.ran, on+": "+cmd)
	answer := "ok"
	if q := f.plays[cmd]; len(q) > 0 {
		answer = q[0]
		if len(q) > 1 {
			f.plays[cmd] = q[1:]
		}
	}
	f.mu.Unlock()
	switch answer {
	case "hang":
		<-ctx.Done()
		return sshx.Output{}, ctx.Err()
	case "fail":
		return sshx.Output{ExitCode: 1, Stderr: []byte("nope\n")}, nil
	}
	return sshx.Output{Stdout: []byte("done\n")}, nil
}

func rollingHost(t *testing.T, r inventory.RebootHooks) (inventory.Host, reboot.Hooks) {
	t.Helper()
	h := inventory.Host{Name: "web1", Address: "10.0.0.21", Reboot: r}
	hs, err := reboot.PrepareHooks(h)
	if err != nil {
		t.Fatal(err)
	}
	return h, hs
}

func quietEmitter() *contract.Emitter {
	return contract.NewSinkEmitter("careen.events/v1", func(contract.Event) {})
}

func TestRunPreTimeoutAnswers(t *testing.T) {
	hooks := inventory.RebootHooks{
		Pre:  inventory.Hook{Run: "out {{.Name}}", Timeout: "20ms", Force: "out --now {{.Name}}"},
		Post: inventory.Hook{Run: "in {{.Name}}", On: "tag:lb"},
	}
	ctx := context.Background()

	for _, c := range []struct {
		name       string
		roll       Rolling
		plays      map[string][]string
		wantSkip   bool
		wantErr    string
		wantForced bool
		wantRan    []string
		wantManual bool
	}{
		{
			name: "wait, then it passes",
			roll: Rolling{Ask: answer(reboot.TimeoutWait)}, plays: map[string][]string{"out web1": {"hang", "ok"}},
			wantRan: []string{"self: out web1", "self: out web1"},
		},
		{
			name: "force", roll: Rolling{Ask: answer(reboot.TimeoutForce)}, plays: map[string][]string{"out web1": {"hang"}},
			wantForced: true, wantRan: []string{"self: out web1", "self: out --now web1"},
		},
		{
			name: "skip undoes pre with post", roll: Rolling{Ask: answer(reboot.TimeoutSkip)}, plays: map[string][]string{"out web1": {"hang"}},
			wantSkip: true, wantRan: []string{"self: out web1", "tag:lb: in web1"},
		},
		{
			name: "abort leaves it", roll: Rolling{Ask: answer(reboot.TimeoutAbort)}, plays: map[string][]string{"out web1": {"hang"}},
			wantErr: "pre timed out after 20ms; aborted", wantManual: true, wantRan: []string{"self: out web1"},
		},
		{
			name: "no way to ask: abort", roll: Rolling{}, plays: map[string][]string{"out web1": {"hang"}},
			wantErr: "aborted", wantManual: true, wantRan: []string{"self: out web1"},
		},
		{
			name: "--on-timeout beats asking", roll: Rolling{OnTimeout: reboot.TimeoutForce, Ask: answer(reboot.TimeoutAbort)}, plays: map[string][]string{"out web1": {"hang"}},
			wantForced: true, wantRan: []string{"self: out web1", "self: out --now web1"},
		},
		{
			name: "a failing pre stops without asking", roll: Rolling{Ask: answer(reboot.TimeoutSkip)}, plays: map[string][]string{"out web1": {"fail"}},
			wantErr: "pre exited 1: nope", wantManual: true, wantRan: []string{"self: out web1"},
		},
		{
			name: "a forced pre that times out stops", roll: Rolling{Ask: answer(reboot.TimeoutForce)}, plays: map[string][]string{"out web1": {"hang"}, "out --now web1": {"hang"}},
			wantErr: "force timed out", wantManual: true, wantRan: []string{"self: out web1", "self: out --now web1"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, hs := rollingHost(t, hooks)
			f := &fakeRun{plays: c.plays}
			var o RebootOutcome
			skipped, err := runPre(ctx, quietEmitter(), c.roll, h, hs, f.run, &o)
			if skipped != c.wantSkip {
				t.Errorf("skipped = %v", skipped)
			}
			if (err != nil) != (c.wantErr != "") || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
				t.Errorf("err = %v, want %q", err, c.wantErr)
			}
			if o.Forced != c.wantForced {
				t.Errorf("forced = %v", o.Forced)
			}
			if !slices.Equal(f.ran, c.wantRan) {
				t.Errorf("ran %v, want %v", f.ran, c.wantRan)
			}
			if (o.Manual != "") != c.wantManual || (c.wantManual && !strings.Contains(o.Manual, "tag:lb: in web1") && !strings.Contains(o.Manual, "on tag:lb")) {
				t.Errorf("manual = %q", o.Manual)
			}
			if len(o.HookRuns) != len(c.wantRan) {
				t.Errorf("hook runs %d, want %d", len(o.HookRuns), len(c.wantRan))
			}
		})
	}
}

func answer(a string) contract.Ask {
	return func(context.Context, contract.Question) (string, error) { return a, nil }
}

func TestTimeoutChoiceAsks(t *testing.T) {
	h, hs := rollingHost(t, inventory.RebootHooks{Pre: inventory.Hook{Run: "out", Force: "out --now"}})
	var q contract.Question
	ask := func(_ context.Context, got contract.Question) (string, error) {
		q = got
		return reboot.TimeoutSkip, nil
	}
	if a, err := timeoutChoice(context.Background(), Rolling{Ask: ask}, h, hs, reboot.HookRun{Output: "evicting pod x"}); err != nil || a != reboot.TimeoutSkip {
		t.Fatalf("got %q, %v", a, err)
	}
	if q.Host != "web1" || !slices.Equal(q.Options, []string{"wait", "force", "skip", "abort"}) || !strings.Contains(q.Message, "evicting pod x") {
		t.Errorf("question = %+v", q)
	}

	// No force command: force isn't offered, and can't be preset.
	h, hs = rollingHost(t, inventory.RebootHooks{Pre: inventory.Hook{Run: "out"}})
	timeoutChoice(context.Background(), Rolling{Ask: ask}, h, hs, reboot.HookRun{})
	if slices.Contains(q.Options, "force") {
		t.Errorf("force offered without pre.force: %v", q.Options)
	}
	if _, err := timeoutChoice(context.Background(), Rolling{OnTimeout: "force"}, h, hs, reboot.HookRun{}); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("--on-timeout force without pre.force: %v", err)
	}

	// The host's own onTimeout answers without asking.
	h, hs = rollingHost(t, inventory.RebootHooks{Pre: inventory.Hook{Run: "out", OnTimeout: "skip"}})
	asked := false
	a, _ := timeoutChoice(context.Background(), Rolling{Ask: func(context.Context, contract.Question) (string, error) { asked = true; return "", nil }}, h, hs, reboot.HookRun{})
	if a != reboot.TimeoutSkip || asked {
		t.Errorf("onTimeout skip: got %q, asked %v", a, asked)
	}

	// A front end that can't ask (a script) stops with its own error.
	h, hs = rollingHost(t, inventory.RebootHooks{Pre: inventory.Hook{Run: "out"}})
	stop := contract.Precondition("decision", "rerun with --on-timeout")
	if _, err := timeoutChoice(context.Background(), Rolling{Ask: func(context.Context, contract.Question) (string, error) { return "", stop }}, h, hs, reboot.HookRun{}); !errors.Is(err, stop) {
		t.Errorf("err = %v", err)
	}
}

func TestRunAfter(t *testing.T) {
	h, hs := rollingHost(t, inventory.RebootHooks{
		Pre:    inventory.Hook{Run: "out"},
		Post:   inventory.Hook{Run: "in"},
		Gate:   inventory.Hook{Run: "healthy", Timeout: "1s", Interval: "1ms"},
		Settle: "5ms",
	})
	f := &fakeRun{plays: map[string][]string{"healthy": {"fail", "fail", "ok"}}}
	var o RebootOutcome
	start := time.Now()
	if err := runAfter(context.Background(), quietEmitter(), h, hs, f.run, &o); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.ran, []string{"self: in", "self: healthy", "self: healthy", "self: healthy"}) {
		t.Errorf("ran %v", f.ran)
	}
	if time.Since(start) < 5*time.Millisecond {
		t.Error("didn't settle")
	}

	// A failing post stops, and says how to put the host back.
	f = &fakeRun{plays: map[string][]string{"in": {"fail"}}}
	o = RebootOutcome{}
	if err := runAfter(context.Background(), quietEmitter(), h, hs, f.run, &o); err == nil || !strings.Contains(o.Manual, "run post on self") {
		t.Errorf("post failing: %v, manual %q", err, o.Manual)
	}

	// A gate that never passes stops.
	_, hs = rollingHost(t, inventory.RebootHooks{Gate: inventory.Hook{Run: "healthy", Timeout: "20ms", Interval: "1ms"}})
	f = &fakeRun{plays: map[string][]string{"healthy": {"fail"}}}
	if err := runAfter(context.Background(), quietEmitter(), h, hs, f.run, &o); err == nil || !strings.Contains(err.Error(), "gate didn't pass") {
		t.Errorf("gate: %v", err)
	}
}

func TestHookHost(t *testing.T) {
	target := inventory.Host{Name: "web1", Address: "10.0.0.21", User: "admin"}
	if h, _ := hookHost(context.Background(), nil, target, reboot.OnLocal); !h.Local() {
		t.Error("local didn't run here")
	}
	h, _ := hookHost(context.Background(), nil, target, reboot.OnSelf)
	if r, ok := h.(sshx.Remote); !ok || r.Address != "10.0.0.21" || !r.NoMaster {
		t.Errorf("self = %#v, want a fresh connection to the host", h)
	}
	// Only the rebooting host has the tag: nobody else to run it on.
	fleet := []inventory.Host{{Name: "web1", Tags: []string{"lb"}}, {Name: "db1", Tags: []string{"db"}}}
	if _, err := hookHost(context.Background(), fleet, target, "tag:lb"); err == nil || !strings.Contains(err.Error(), "no other host tagged lb") {
		t.Errorf("tag with no other host: %v", err)
	}
}
