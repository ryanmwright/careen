package actions

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/reboot"
)

func testInventory() *inventory.Inventory {
	h := func(name string, deploy bool, policy string) inventory.Host {
		return inventory.Host{Name: name, Address: name + ".lan", User: "admin", Deploy: deploy, RebootPolicy: policy,
			BuildOn: "local", Builder: "ssh-ng://b", Operation: "switch", Tags: []string{}}
	}
	return &inventory.Inventory{
		Flake: inventory.Flake{Ref: "git+ssh://x", Revision: "abc", Locked: "git+ssh://x?rev=abc"},
		Hosts: []inventory.Host{h("app1", true, "auto"), h("app2", true, "auto"), h("mgr", false, "manual"), h("app3", true, "auto")},
	}
}

func names[T any](xs []T, name func(T) string) []string {
	var out []string
	for _, x := range xs {
		out = append(out, name(x))
	}
	return out
}

func hostName(h inventory.Host) string { return h.Name }

func TestPlanDeploy(t *testing.T) {
	defer func(old string) { ThisHost = old }(ThisHost)
	ThisHost = "mgr"
	inv := testInventory()

	p, err := PlanDeploy(inv, inventory.Selector{}, DeployOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(p.Targets(), hostName); !slices.Equal(got, []string{"app1", "app2", "app3"}) {
		t.Errorf("all: targets = %v", got)
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Host != "mgr" {
		t.Errorf("all: skipped = %+v", p.Skipped)
	}

	// Named hosts deploy even with deploy = false, in the order named, with
	// this machine last.
	p, err = PlanDeploy(inv, inventory.Selector{Only: []string{"mgr", "app3", "app1"}}, DeployOptions{BuildOn: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(p.Targets(), hostName); !slices.Equal(got, []string{"app3", "app1", "mgr"}) {
		t.Errorf("only: targets = %v", got)
	}
	if got := names(p.Hosts, func(h PlanHost) string { return h.Host }); !slices.Equal(got, []string{"mgr", "app3", "app1"}) {
		t.Errorf("only: plan order = %v", got)
	}
	if p.Hosts[0].BuildOn != "remote" || p.Hosts[0].Builder != "ssh-ng://b" || !p.Hosts[0].Local {
		t.Errorf("override: %+v", p.Hosts[0])
	}

	if _, err := PlanDeploy(inv, inventory.Selector{}, DeployOptions{Reboot: "sometimes"}); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("bad reboot mode: %v", err)
	}
	if _, err := PlanDeploy(inv, inventory.Selector{}, DeployOptions{BuildOn: "cloud"}); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("bad build mode: %v", err)
	}
	inv.Flake.Dirty = true
	if _, err := PlanDeploy(inv, inventory.Selector{}, DeployOptions{}); contract.ExitCode(err) != contract.ExitPrecondition {
		t.Errorf("dirty: %v", err)
	}
	if _, err := PlanDeploy(inv, inventory.Selector{}, DeployOptions{AllowDirty: true}); err != nil {
		t.Errorf("dirty allowed: %v", err)
	}
}

func TestPlanReboots(t *testing.T) {
	inv := testInventory()
	c := func(i int, pending, local bool) candidate {
		return candidate{Candidate: reboot.Candidate{Host: inv.Hosts[i], Pending: pending, Local: local}}
	}
	cands := []candidate{c(2, true, true), c(0, true, false), c(1, false, false), c(3, true, false)}
	ordered, outs := planReboots(cands, reboot.Policy, nil, false)
	if got := names(ordered, func(c candidate) string { return c.Host.Name }); !slices.Equal(got, []string{"app1", "app2", "app3", "mgr"}) {
		t.Errorf("order = %v", got)
	}
	skips := names(outs, func(o RebootOutcome) string { return o.Skip })
	if skips[0] != "" || skips[1] == "" || skips[2] != "" || skips[3] == "" {
		t.Errorf("skips = %q", skips)
	}
}

func TestParseGCSummary(t *testing.T) {
	n, freed, ok := ParseGCSummary("1234 store paths deleted, 567.50 MiB freed")
	if !ok || n != 1234 || freed != int64(567.5*(1<<20)) {
		t.Errorf("got %d %d %v", n, freed, ok)
	}
	if _, freed, ok := ParseGCSummary("0 store paths deleted, 0.00 B freed"); !ok || freed != 0 {
		t.Errorf("zero: %d %v", freed, ok)
	}
	if _, _, ok := ParseGCSummary("finding garbage collector roots..."); ok {
		t.Error("parsed a line that isn't the summary")
	}
}

func TestShellCommand(t *testing.T) {
	if got := ShellCommand([]string{"df -h | grep nix"}); got != "df -h | grep nix" {
		t.Errorf("one arg: %q", got)
	}
	if got := ShellCommand([]string{"echo", "a b", "it's"}); got != `echo 'a b' 'it'\''s'` {
		t.Errorf("several: %q", got)
	}
}

func TestExecLocal(t *testing.T) {
	defer func(old string) { ThisHost = old }(ThisHost)
	ThisHost = "here"
	inv := &inventory.Inventory{Hosts: []inventory.Host{{Name: "here"}}}
	em := contract.NewSinkEmitter("test", func(contract.Event) {})

	p, err := PlanExec(inv, inventory.Selector{}, ShellCommand([]string{"echo out; echo err >&2; exit 4"}), ExecOptions{Timeout: 5 * time.Second, Parallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	done, err := Exec(context.Background(), em, p)
	r := done.Hosts[0]
	if err == nil || r.ExitCode != 4 || r.Stdout != "out\n" || r.Stderr != "err\n" || r.Error != nil {
		t.Errorf("err=%v result=%+v", err, r)
	}

	p, _ = PlanExec(inv, inventory.Selector{}, "sleep 5", ExecOptions{Timeout: 100 * time.Millisecond, Parallel: 1})
	done, err = Exec(context.Background(), em, p)
	if err == nil || done.Hosts[0].Error == nil || done.Hosts[0].Error.Code != "timeout" {
		t.Errorf("timeout: err=%v result=%+v", err, done.Hosts[0])
	}

	if _, err := PlanExec(inv, inventory.Selector{}, "  ", ExecOptions{}); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("empty command: %v", err)
	}
}

func TestLineDiff(t *testing.T) {
	a := []string{"keys:", "  - &a 1", "", "rules:", "  age: [*a]"}
	b := []string{"keys:", "  - &a 1", "  - &b 2", "", "rules:", "  key_groups:", "  age: [*a, *b]"}
	got := lineDiff(a, b)
	// A changed block reads as diff shows it: what went, then what came.
	want := []string{"+   - &b 2", "-   age: [*a]", "+   key_groups:", "+   age: [*a, *b]"}
	if !slices.Equal(got, want) {
		t.Errorf("diff = %q\nwant %q", got, want)
	}
}

func TestRebootSelfIsOptIn(t *testing.T) {
	inv := testInventory()
	mgr := inv.Hosts[2] // manual policy
	mgr.RebootPolicy = "auto"
	c := []candidate{{Candidate: reboot.Candidate{Host: mgr, Pending: true, Named: true, Local: true}}}
	for _, mode := range []string{reboot.Policy, reboot.Force} {
		if _, outs := planReboots(c, mode, []string{"mgr"}, false); outs[0].Skip != SkipSelf {
			t.Errorf("%s without --reboot-self: skip = %q", mode, outs[0].Skip)
		}
		if _, outs := planReboots(c, mode, []string{"mgr"}, true); outs[0].Skip != "" {
			t.Errorf("%s with --reboot-self: skip = %q", mode, outs[0].Skip)
		}
	}
	// rebootPolicy still applies on top: never stays never.
	mgr.RebootPolicy = "never"
	c[0].Host = mgr
	if _, outs := planReboots(c, reboot.Force, nil, true); outs[0].Skip == "" {
		t.Error("never host rebooted with --reboot-self")
	}
}
