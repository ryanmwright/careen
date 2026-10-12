package reboot

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshx"
)

func TestDecide(t *testing.T) {
	host := func(policy string) inventory.Host { return inventory.Host{Name: "h", RebootPolicy: policy} }
	cases := []struct {
		policy         string
		pending, named bool
		mode           string
		want           bool
	}{
		{"auto", true, false, Policy, true},
		{"auto", false, false, Policy, false},
		{"auto", false, false, Force, true},
		{"manual", true, false, Policy, false},
		{"manual", true, true, Policy, true},
		{"manual", false, true, Policy, false},
		{"manual", false, false, Force, true},
		{"never", true, true, Policy, false},
		{"never", true, true, Force, false},
	}
	for _, c := range cases {
		d := Decide(Candidate{Host: host(c.policy), Pending: c.pending, Named: c.named}, c.mode)
		if d.Reboot != c.want {
			t.Errorf("policy=%s pending=%v named=%v mode=%s: reboot=%v (skip %q), want %v",
				c.policy, c.pending, c.named, c.mode, d.Reboot, d.Skip, c.want)
		}
		if !d.Reboot && d.Skip == "" {
			t.Errorf("policy=%s mode=%s: skipped without a reason", c.policy, c.mode)
		}
	}
}

func TestOrder(t *testing.T) {
	c := func(name string, local bool) Candidate {
		return Candidate{Host: inventory.Host{Name: name}, Local: local}
	}
	cs := []Candidate{c("mgr", true), c("web", false), c("db", false), c("ci", false)}
	names := func(cs []Candidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Host.Name)
		}
		return out
	}
	if got := names(Order(cs, nil)); !slices.Equal(got, []string{"ci", "db", "web", "mgr"}) {
		t.Errorf("by name: %v", got)
	}
	if got := names(Order(cs, []string{"mgr", "web", "db"})); !slices.Equal(got, []string{"web", "db", "ci", "mgr"}) {
		t.Errorf("by --only: %v", got)
	}
	// rebootOrder beats the name; --only beats rebootOrder; local stays last.
	o := func(name string, order int, local bool) Candidate {
		return Candidate{Host: inventory.Host{Name: name, RebootOrder: order}, Local: local}
	}
	cs = []Candidate{o("mgr", -5, true), o("web", 20, false), o("db", 10, false), o("ci", 20, false)}
	if got := names(Order(cs, nil)); !slices.Equal(got, []string{"db", "ci", "web", "mgr"}) {
		t.Errorf("by rebootOrder: %v", got)
	}
	if got := names(Order(cs, []string{"web", "db"})); !slices.Equal(got, []string{"web", "db", "ci", "mgr"}) {
		t.Errorf("--only over rebootOrder: %v", got)
	}
}

// fakeHost answers the remote script's actions. Its boot id changes once
// reboot has been called and a few polls have passed.
type fakeHost struct {
	rebooted  bool
	polls     int
	downPolls int // polls that fail after the reboot, like a host booting
	state     string
	failedNow []string
}

func (f *fakeHost) Local() bool { return false }

func (f *fakeHost) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch args[0] {
	case "boot-id":
		if !f.rebooted {
			return []byte("old\n"), nil
		}
		f.polls++
		if f.polls <= f.downPolls {
			return nil, fmt.Errorf("%w: connection refused", sshx.ErrUnreachable)
		}
		return []byte("new\n"), nil
	case "reboot":
		f.rebooted = true
		return nil, fmt.Errorf("%w: connection closed", sshx.ErrUnreachable)
	case "health":
		out := "system_state=" + f.state + "\ncurrent=/nix/store/sys\n"
		for _, u := range f.failedNow {
			out += "failed_unit=" + u + "\n"
		}
		return []byte(out), nil
	}
	return nil, fmt.Errorf("unexpected action %v", args)
}

func TestRebootWaitsForNewBoot(t *testing.T) {
	opts := Options{Timeout: 5 * time.Second, HealthTimeout: time.Second, Poll: time.Millisecond}
	h := &fakeHost{downPolls: 3, state: "degraded", failedNow: []string{"old.service"}}
	if _, err := Reboot(context.Background(), h, []string{"old.service"}, opts, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if h.polls != 4 {
		t.Errorf("polls = %d, want 4", h.polls)
	}

	h = &fakeHost{state: "degraded", failedNow: []string{"new.service"}}
	if _, err := Reboot(context.Background(), h, nil, opts, func(string, string) {}); err == nil {
		t.Error("a unit that newly failed after the reboot passed the health check")
	}

	h = &fakeHost{downPolls: 1 << 30}
	opts.Timeout = 20 * time.Millisecond
	if _, err := Reboot(context.Background(), h, nil, opts, func(string, string) {}); err == nil {
		t.Error("a host that never came back didn't fail")
	}
}
