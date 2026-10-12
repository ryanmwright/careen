package main

import (
	"context"
	"slices"
	"testing"

	"github.com/ryanmwright/careen/internal/contract"
)

// Under --json careen never prompts: a pre timeout becomes a decision event
// naming the --on-timeout values that answer it, and exit 3.
func TestAskerWithoutATerminal(t *testing.T) {
	var events []contract.Event
	em := contract.NewSinkEmitter("careen.events/v1", func(e contract.Event) { events = append(events, e) })
	rf := &rebootFlags{}
	roll, err := rf.rolling(&globalFlags{json: true}, em)
	if err != nil {
		t.Fatal(err)
	}
	_, err = roll.Ask(context.Background(), contract.Question{Host: "web1", Step: "pre", Message: "pre timed out after 5m0s", Options: []string{"wait", "force", "skip", "abort"}})
	if contract.ExitCode(err) != contract.ExitPrecondition {
		t.Fatalf("err = %v, want exit 3", err)
	}
	if len(events) != 1 || events[0].Event != contract.EventDecision || events[0].Host != "web1" {
		t.Fatalf("events = %+v", events)
	}
	if want := []string{"--on-timeout force", "--on-timeout skip", "--on-timeout abort"}; !slices.Equal(events[0].Options, want) {
		t.Errorf("options = %v, want %v (wait can't be preset)", events[0].Options, want)
	}

	for _, bad := range []string{"wait", "ask", "later"} {
		rf := &rebootFlags{onTimeout: bad}
		if _, err := rf.rolling(&globalFlags{}, em); contract.ExitCode(err) != contract.ExitUsage {
			t.Errorf("--on-timeout %s: %v", bad, err)
		}
	}
}
