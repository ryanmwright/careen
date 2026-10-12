package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/reboot"
)

var timedOut = contract.Question{Host: "web1", Step: "pre", Message: "pre timed out after 5m0s", Options: []string{"wait", "force", "skip", "abort"}}

func TestAskPrompt(t *testing.T) {
	m := loaded(t, newFake())
	m.events = make(chan tea.Msg, 1)
	step := func(msg tea.Msg) {
		next, _ := m.Update(msg) // its command waits on the action; not run here
		m = next.(Model)
	}

	reply := make(chan string, 1)
	step(askMsg{timedOut, reply})
	if m.mode != modeAsk {
		t.Fatalf("mode = %v, want the prompt", m.mode)
	}
	if v := ansi.Strip(m.render()); !strings.Contains(v, "web1: pre timed out after 5m0s") || !strings.Contains(v, "f force") || !strings.Contains(v, "no answer (stops)") {
		t.Errorf("prompt not shown:\n%s", v)
	}
	step(keyMsg("x")) // not an option: ignored
	if m.mode != modeAsk || len(reply) != 0 {
		t.Fatal("a stray key answered")
	}
	step(keyMsg("f"))
	if got := <-reply; got != "force" {
		t.Errorf("answer = %q, want force", got)
	}
	if m.mode != modeNormal || m.ask != nil {
		t.Errorf("after answering: mode %v, ask %v", m.mode, m.ask)
	}

	// esc gives no answer; a question during a full-screen view returns to it.
	m.mode = modeHelp
	step(askMsg{timedOut, reply})
	step(keyMsg("esc"))
	if got := <-reply; got != "" {
		t.Errorf("esc answered %q", got)
	}
	if m.mode != modeHelp {
		t.Errorf("mode after esc = %v, want back to help", m.mode)
	}
}

func TestAskVia(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	ask := askVia(ch)
	answerWith := func(a string) {
		go func() { msg := (<-ch).(askMsg); msg.reply <- a }()
	}

	answerWith("skip")
	if a, err := ask(context.Background(), timedOut); err != nil || a != "skip" {
		t.Errorf("got %q, %v", a, err)
	}
	answerWith("")
	if _, err := ask(context.Background(), timedOut); contract.ExitCode(err) != contract.ExitFailed {
		t.Errorf("no answer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch <- nil // full: nobody reads, so only ctx can end it
	if _, err := ask(ctx, timedOut); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestRebootSummarySkipAndManual(t *testing.T) {
	runs := []reboot.HookRun{{Hook: "pre", TimedOut: true}, {Hook: "post"}}
	skipped := actions.RebootDone{Hosts: []actions.RebootOutcome{
		{Host: "web1", Skip: "pre timed out; skipped, not rebooted", HookRuns: runs},
		{Host: "web2", Skip: "no reboot needed"},
	}}
	if got := rebootSummary(skipped); got != "reboot: web1 skipped: pre timed out; skipped, not rebooted" {
		t.Errorf("skip summary = %q", got)
	}
	aborted := actions.RebootDone{Hosts: []actions.RebootOutcome{
		{Host: "web1", Error: &contract.Error{Message: "aborted"}, HookRuns: runs[:1], Manual: "run post on tag:lb to put the host back: lbctl enable web1"},
	}}
	if got := manualNotes(aborted); !strings.Contains(got, "web1: run post on tag:lb") {
		t.Errorf("manual notes = %q", got)
	}
}

func TestWrapLinesKeepsTheEnd(t *testing.T) {
	s := "reboot failed: " + strings.Repeat("drain error ", 30) + "; web1: run post on self to put the host back: uncordon web1"
	ls := wrapLines(s, 40, 3)
	if len(ls) != 3 || !strings.HasSuffix(ls[2], "uncordon web1") || !strings.HasPrefix(ls[0], "…") {
		t.Errorf("wrapLines = %q", ls)
	}
	for _, l := range ls {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("line wider than 40: %q", l)
		}
	}
}
