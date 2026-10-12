package tui

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/logs"
	"github.com/ryanmwright/careen/internal/probe"
	"github.com/ryanmwright/careen/internal/reboot"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/status"
	"github.com/ryanmwright/careen/internal/vulns"
)

// fakeBackend serves a fixed fleet and records what it was asked to do.
type fakeBackend struct {
	mu       sync.Mutex
	inv      *inventory.Inventory
	rep      status.Report
	deployed [][]string
	release  chan struct{} // Deploy waits on it, when set
	execs    []string
	cached   *vulns.Report
	statuses int                  // Status calls
	last     *inventory.Inventory // what LastStatus serves, when set
}

func newFake() *fakeBackend {
	h := func(name string, deploy bool, policy string) inventory.Host {
		return inventory.Host{Name: name, Address: name + ".lan", User: "admin", Deploy: deploy, RebootPolicy: policy, BuildOn: "local", Operation: "switch", Tags: []string{}}
	}
	inv := &inventory.Inventory{
		Flake: inventory.Flake{Ref: "git+ssh://x/nix", Revision: "e9f1c070000", Locked: "git+ssh://x/nix?rev=e9f1c070000"},
		Hosts: []inventory.Host{h("app1", true, "auto"), h("mgr", false, "manual"), h("app3", true, "auto")},
	}
	st := func(rev string, pending bool, failed ...string) *status.State {
		s := &status.State{Revision: rev, System: "/nix/store/sys-" + rev, Drift: status.DriftDiffers, RebootReasons: []string{}, SystemState: "running", FailedUnits: failed, UptimeSeconds: 3600}
		if rev == "e9f1c070000" {
			s.Drift = status.DriftCurrent
		}
		if pending {
			s.RebootPending, s.RebootReasons = true, []string{"kernel"}
		}
		if len(failed) > 0 {
			s.SystemState = "degraded"
		}
		return s
	}
	rep := status.Report{Flake: inv.Flake, Hosts: []status.Row{
		{Host: "app1", Address: "app1.lan", State: st("b72d6a1", false)},
		{Host: "mgr", Address: "mgr.lan", State: st("e9f1c070000", true, "nginx.service")},
		{Host: "app3", Address: "app3.lan", Error: &contract.Error{Code: "unreachable", Message: "no route to host"}},
	}}
	return &fakeBackend{inv: inv, rep: rep}
}

func (f *fakeBackend) Status(context.Context) (*inventory.Inventory, status.Report, error) {
	f.mu.Lock()
	f.statuses++
	f.mu.Unlock()
	return f.inv, f.rep, nil
}
func (f *fakeBackend) LastStatus(context.Context) (*inventory.Inventory, status.Report, bool) {
	if f.last == nil {
		return nil, status.Report{}, false
	}
	rep := f.rep
	rep.Flake = f.last.Flake
	return f.last, rep, true
}
func (f *fakeBackend) Inventory(context.Context) (*inventory.Inventory, error) { return f.inv, nil }
func (f *fakeBackend) PlanDeploy(inv *inventory.Inventory, only []string, mode string, self bool) (*actions.DeployPlan, error) {
	return actions.PlanDeploy(inv, inventory.Selector{Only: only}, actions.DeployOptions{Reboot: mode, RebootSelf: self})
}
func (f *fakeBackend) Deploy(_ context.Context, p *actions.DeployPlan, sink func(contract.Event)) (actions.DeployDone, error) {
	var names []string
	for _, h := range p.Targets() {
		names = append(names, h.Name)
	}
	f.mu.Lock()
	f.deployed = append(f.deployed, names)
	f.mu.Unlock()
	sink(contract.Event{Event: contract.EventProgress, Host: names[0], Step: "build", Message: "building here"})
	if f.release != nil {
		<-f.release
	}
	done := actions.DeployDone{}
	for _, n := range names {
		done.Hosts = append(done.Hosts, actions.DeployResult{Host: n, Status: "switched", RebootPending: []string{}})
	}
	return done, nil
}
func (f *fakeBackend) PlanReboot(context.Context, *inventory.Inventory, []string, string, bool) (*actions.RebootPlan, error) {
	return &actions.RebootPlan{Hosts: []actions.RebootOutcome{}}, nil
}
func (f *fakeBackend) Reboot(context.Context, *actions.RebootPlan, func(contract.Event)) (actions.RebootDone, error) {
	return actions.RebootDone{}, nil
}
func (f *fakeBackend) Rollback(context.Context, inventory.Host, func(contract.Event)) (actions.RollbackDone, error) {
	return actions.RollbackDone{}, nil
}
func (f *fakeBackend) FollowLogs(ctx context.Context, _ inventory.Host, unit string, fn func(logs.Entry)) error {
	fn(logs.Entry{Time: time.Unix(0, 0), Unit: unit, Priority: 3, Message: "bind() failed"})
	return nil
}
func (f *fakeBackend) NeedsSudo(context.Context, []inventory.Host) bool { return false }
func (f *fakeBackend) Shell(inventory.Host) *exec.Cmd                   { return exec.Command("true") }

func (f *fakeBackend) PlanGC(_ context.Context, inv *inventory.Inventory, only []string) (*actions.GCPlan, error) {
	h, _ := inv.Select(inventory.Selector{Only: only})
	res := &probe.Result{Generations: 11, StoreUsed: 7 << 30, StoreSize: 19 << 30}
	return actions.PlanGCFrom([]probe.Outcome{{Host: h[0], Result: res}}, actions.GCOptions{OlderThan: "14d"}), nil
}
func (f *fakeBackend) GC(context.Context, *actions.GCPlan, func(contract.Event)) (actions.GCDone, error) {
	return actions.GCDone{Freed: 1 << 20}, nil
}
func (f *fakeBackend) PlanExec(inv *inventory.Inventory, only []string, command string, sudo bool) (*actions.ExecPlan, error) {
	return actions.PlanExec(inv, inventory.Selector{Only: only}, command, actions.ExecOptions{Sudo: sudo})
}
func (f *fakeBackend) Exec(_ context.Context, p *actions.ExecPlan, _ func(contract.Event)) (actions.ExecDone, error) {
	f.mu.Lock()
	f.execs = append(f.execs, p.Command)
	f.mu.Unlock()
	var d actions.ExecDone
	for i, h := range p.Hosts {
		d.Hosts = append(d.Hosts, actions.ExecResult{Host: h, ExitCode: i, Stdout: "out from " + h + "\n"})
	}
	return d, nil
}
func (f *fakeBackend) Vulns(_ context.Context, _ *inventory.Inventory, only []string, _ bool, progress func(host, msg string)) (actions.VulnsReport, error) {
	progress(only[0], "scanning with vulnix")
	// app1 runs /nix/store/sys-b72d6a1 in the fake fleet.
	r := vulns.Report{Host: only[0], System: "/nix/store/sys-b72d6a1", Summary: vulns.Summary{Critical: 1, High: 2},
		Findings: []vulns.Finding{{Package: "glibc", Version: "2.42", CVEs: []vulns.CVE{{ID: "CVE-2026-5450", Score: 9.8}}}}}
	f.mu.Lock()
	f.cached = &r
	f.mu.Unlock()
	return actions.VulnsReport{Hosts: []vulns.Report{r}}, nil
}
func (f *fakeBackend) CachedVulns(host, system string) *vulns.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cached != nil && f.cached.Host == host && f.cached.System == system {
		return f.cached
	}
	return nil
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

// run feeds msg to m and then every message its commands produce, until
// none are left or a command blocks (a tick, an action still running).
func run(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	queue := []tea.Msg{msg}
	for steps := 0; len(queue) > 0 && steps < 100; steps++ {
		next, cmd := m.Update(queue[0])
		m = next.(Model)
		queue = append(queue[1:], drain(cmd)...)
	}
	return m
}

// pending holds commands that were still blocked when drain gave up on
// them, like Bubble Tea's own goroutines; settle delivers what they return.
var pending []chan tea.Msg

// drain runs cmd and any batched commands, skipping ticks. A command that
// doesn't return promptly (it waits on a running action) is parked in
// pending.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return expand(msg)
	case <-time.After(200 * time.Millisecond):
		pending = append(pending, ch)
		return nil
	}
}

func expand(msg tea.Msg) []tea.Msg {
	switch msg := msg.(type) {
	case nil, tickMsg:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// settle feeds m what parked commands return, until none are left.
func settle(t *testing.T, m Model) Model {
	t.Helper()
	for len(pending) > 0 {
		ch := pending[0]
		pending = pending[1:]
		select {
		case msg := <-ch:
			for _, msg := range expand(msg) {
				m = run(t, m, msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a command never returned")
		}
	}
	return m
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m = run(t, m, keyMsg(k))
	}
	return m
}

func screen(m Model) string { return ansi.Strip(m.render()) }

func loaded(t *testing.T, be Backend) Model {
	t.Helper()
	pending = nil
	m := New(be, 30*time.Second)
	m.width, m.height = 140, 40
	return run(t, m, m.refresh()())
}

// The tick after Init mustn't start a second load while the first runs.
func TestStartLoadsOnce(t *testing.T) {
	pending = nil
	f := newFake()
	m := New(f, 30*time.Second)
	m = run(t, m, tickMsg(time.Now()))
	m = run(t, m, m.refresh()())
	if f.statuses != 1 {
		t.Errorf("Status called %d times, want 1", f.statuses)
	}
	if !strings.Contains(screen(m), "updated") {
		t.Errorf("header after load:\n%s", screen(m))
	}
}

// The last fleet shows while the refresh runs, but never replaces it.
func TestStartShowsLastFleet(t *testing.T) {
	pending = nil
	f := newFake()
	old := *f.inv
	old.Flake.Revision = "b72d6a1"
	f.last = &old
	m := New(f, 30*time.Second)
	m.width, m.height = 140, 40
	lastMsg := func() tea.Msg {
		inv, rep, _ := f.LastStatus(context.Background())
		return statusMsg{inv: inv, rep: rep, last: true}
	}

	shown := run(t, m, lastMsg())
	if s := screen(shown); !strings.Contains(s, "@ b72d6a1") || !strings.Contains(s, "refreshing…") {
		t.Errorf("last fleet:\n%s", s)
	}
	shown = run(t, shown, m.refresh()())
	if s := screen(shown); !strings.Contains(s, "@ e9f1c07") || strings.Contains(s, "refreshing…") {
		t.Errorf("after refresh:\n%s", s)
	}
	late := run(t, shown, lastMsg())
	if s := screen(late); !strings.Contains(s, "@ e9f1c07") {
		t.Errorf("late last fleet replaced the refresh:\n%s", s)
	}
}

func TestTableAndDetail(t *testing.T) {
	m := loaded(t, newFake())
	s := screen(m)
	for _, want := range []string{"flake git+ssh://x/nix @ e9f1c07", "app1", "b72d6a1", "differs", "unreachable: no route to host", "HOST app1", "failed     none"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	m = press(t, m, "down")
	if s := screen(m); !strings.Contains(s, "HOST mgr") || !strings.Contains(s, "nginx.service") || !strings.Contains(s, "needed: kernel") {
		t.Errorf("mgr detail:\n%s", s)
	}
}

func TestMarksKeepOrderAndSurviveRefresh(t *testing.T) {
	be := newFake()
	m := loaded(t, be)
	m = press(t, m, "down", "down", "space", "up", "up", "space")
	if !slices.Equal(m.marks, []string{"app3", "app1"}) {
		t.Fatalf("marks = %v", m.marks)
	}
	if !slices.Equal(m.targets(), []string{"app3", "app1"}) {
		t.Errorf("targets = %v", m.targets())
	}
	// A refresh where app3 left the inventory drops its mark and keeps
	// the cursor on app1.
	be.rep.Hosts = slices.DeleteFunc(slices.Clone(be.rep.Hosts), func(r status.Row) bool { return r.Host == "app3" })
	m = run(t, m, m.refresh()())
	if !slices.Equal(m.marks, []string{"app1"}) || m.cursorHost != "app1" {
		t.Errorf("after refresh: marks=%v cursor=%s", m.marks, m.cursorHost)
	}
	m = press(t, m, "A")
	if len(m.marks) != 0 {
		t.Errorf("A left marks %v", m.marks)
	}
}

func TestDeployDialogAndRun(t *testing.T) {
	be := newFake()
	be.release = make(chan struct{})
	m := loaded(t, be)
	m = press(t, m, "down", "space", "up", "space", "d")
	s := screen(m)
	if !strings.Contains(s, "DEPLOY") || !strings.Contains(s, "mgr") || !strings.Contains(s, "this machine") && !strings.Contains(s, "build local") {
		t.Fatalf("dialog:\n%s", s)
	}
	if m.confirm.reboot != "" {
		t.Errorf("default reboot mode = %q", m.confirm.reboot)
	}
	m = press(t, m, "p")
	if m.confirm.reboot != reboot.Policy || m.confirm.deploy.Reboot != reboot.Policy {
		t.Errorf("p: reboot = %q / plan %q", m.confirm.reboot, m.confirm.deploy.Reboot)
	}

	m = press(t, m, "y")
	if !m.running || m.mode != modeNormal {
		t.Fatalf("not running after y: mode=%v", m.mode)
	}
	be.mu.Lock()
	got := be.deployed
	be.mu.Unlock()
	// mgr was named (marked), so it deploys despite deploy = false, in mark order.
	if len(got) != 1 || !slices.Equal(got[0], []string{"mgr", "app1"}) {
		t.Errorf("deployed = %v", got)
	}
	if s := screen(m); !strings.Contains(s, "building here") || !strings.Contains(s, "deploy running") {
		t.Errorf("activity:\n%s", s)
	}
	// Other actions wait while one runs.
	m = press(t, m, "r")
	if m.mode != modeNormal || !strings.Contains(m.flash, "running") {
		t.Errorf("r while running: mode=%v flash=%q", m.mode, m.flash)
	}
	// Quitting asks first.
	m = press(t, m, "q")
	if m.mode != modeQuit {
		t.Errorf("q while running: mode=%v", m.mode)
	}
	m = press(t, m, "n")

	close(be.release)
	m = settle(t, m)
	if m.running || !strings.Contains(m.flash, "mgr switched") {
		t.Errorf("after done: running=%v flash=%q", m.running, m.flash)
	}
}

func TestLogsOfFailedUnit(t *testing.T) {
	m := loaded(t, newFake())
	m = press(t, m, "down", "tab", "l")
	if m.mode != modeLogs || m.logs.unit != "nginx.service" || m.logs.host != "mgr" {
		t.Fatalf("logs: mode=%v %+v", m.mode, m.logs)
	}
	if s := screen(m); !strings.Contains(s, "LOGS mgr nginx.service") || !strings.Contains(s, "bind() failed") {
		t.Errorf("logs screen:\n%s", s)
	}
	m = press(t, m, "f")
	if m.logs.follow {
		t.Error("f didn't pause")
	}
	m = press(t, m, "esc")
	if m.mode != modeNormal {
		t.Errorf("esc: mode=%v", m.mode)
	}
}

func TestStaleMarker(t *testing.T) {
	m := loaded(t, newFake())
	at := m.refreshed
	m.now = func() time.Time { return at.Add(2*m.interval + time.Second) }
	if s := screen(m); !strings.Contains(s, "stale: last update") {
		t.Errorf("no stale marker:\n%s", s)
	}
}

func TestExecPromptAndOutput(t *testing.T) {
	be := newFake()
	m := loaded(t, be)
	m = press(t, m, "space", "down", "space", "x")
	if m.mode != modeInput {
		t.Fatalf("x: mode=%v", m.mode)
	}
	for _, r := range "df -h" {
		m = run(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = press(t, m, "tab")
	if !m.extra.sudo || !strings.Contains(screen(m), "EXEC on app1, mgr") {
		t.Fatalf("prompt: sudo=%v\n%s", m.extra.sudo, screen(m))
	}
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != modeConfirm || m.confirm.execPlan == nil || m.confirm.execPlan.Command != "df -h" || !m.confirm.execPlan.Sudo {
		t.Fatalf("confirm: mode=%v plan=%+v err=%v", m.mode, m.confirm.execPlan, m.confirm.err)
	}
	if s := screen(m); !strings.Contains(s, "as root") || !strings.Contains(s, "$ df -h") {
		t.Errorf("exec dialog:\n%s", s)
	}
	m = press(t, m, "y")
	m = settle(t, m)
	if m.mode != modeExecOut || m.extra.execDone == nil || len(be.execs) != 1 {
		t.Fatalf("after run: mode=%v execs=%v", m.mode, be.execs)
	}
	if s := screen(m); !strings.Contains(s, "out from app1") || !strings.Contains(s, "exit 0") {
		t.Errorf("output view:\n%s", s)
	}
	m = press(t, m, "tab")
	if s := screen(m); !strings.Contains(s, "out from mgr") || !strings.Contains(s, "exit 1") {
		t.Errorf("second host:\n%s", s)
	}
	m = press(t, m, "esc")
	if m.mode != modeNormal || !strings.Contains(m.flash, "1 ok, 1 failed") {
		t.Errorf("back: mode=%v flash=%q", m.mode, m.flash)
	}
}

func TestGCDialog(t *testing.T) {
	m := loaded(t, newFake())
	m = press(t, m, "G")
	if s := screen(m); !strings.Contains(s, "GC") || !strings.Contains(s, "11 generations") || !strings.Contains(s, "collect on 1 host") {
		t.Errorf("gc dialog:\n%s", s)
	}
	m = press(t, m, "y")
	m = settle(t, m)
	if !strings.Contains(m.flash, "gc: freed 1.0 MiB") {
		t.Errorf("flash = %q", m.flash)
	}
}

func TestVulnsViewAndColumn(t *testing.T) {
	m := loaded(t, newFake())
	if strings.Contains(screen(m), "1C 2H") {
		t.Fatal("VULNS shown before any scan")
	}
	m = press(t, m, "v")
	m = settle(t, m)
	if m.mode != modeVulns {
		t.Fatalf("mode = %v", m.mode)
	}
	s := screen(m)
	if !strings.Contains(s, "1C 2H") || !strings.Contains(s, "glibc 2.42") || !strings.Contains(s, "CVE-2026-5450") {
		t.Errorf("vulns view:\n%s", s)
	}
	// The fake scanned system /nix/store/sys-b72d6a1, app1's.
	m = press(t, m, "esc")
	if s := screen(m); !strings.Contains(s, "1C 2H") || !strings.Contains(s, "vulns      1C 2H") {
		t.Errorf("column and detail after scan:\n%s", s)
	}
}

// --- onboarding, keys and secrets ---

type onboardCalls struct {
	registered []string
	set        [][3]string
	added      []string
}

var obCalls onboardCalls

func (f *fakeBackend) Keys(context.Context) (actions.KeysReport, error) {
	return actions.KeysReport{Hosts: []actions.KeyState{
		{Host: "app1", Anchor: "app1_host", Recorded: "age1aaaaaaaaaaaaaaaaaaaaaaaa", Live: "age1aaaaaaaaaaaaaaaaaaaaaaaa", State: actions.KeyOK},
		{Host: "mgr", Anchor: "mgr_host", Recorded: "age1REPLACEME_MGR", Live: "age1bbbbbbbbbbbbbbbbbbbbbbbb", State: actions.KeyPlaceholder},
	}}, nil
}
func (f *fakeBackend) MasterKey(context.Context) error { return nil }
func (f *fakeBackend) RegisterKey(_ context.Context, host string, _ bool, sink func(contract.Event)) error {
	sink(contract.Event{Event: contract.EventProgress, Host: host, Step: "key", Message: "recorded"})
	obCalls.registered = append(obCalls.registered, host)
	return nil
}
func (f *fakeBackend) RekeyHost(context.Context, string, func(contract.Event)) error { return nil }
func (f *fakeBackend) Secrets(context.Context) (actions.SecretsReport, error) {
	return actions.SecretsReport{Hosts: []actions.HostSecrets{
		{Host: "app3", Files: []actions.SecretFile{{Path: "secrets/hosts/app3/secrets.yaml", Names: []string{"app3_token"}, Missing: []string{}}}},
	}}, nil
}
func (f *fakeBackend) SetSecret(_ context.Context, host, file, name, value string) (bool, error) {
	obCalls.set = append(obCalls.set, [3]string{host, name, value})
	return false, nil
}
func (f *fakeBackend) UnsetSecret(context.Context, string, string, string) error { return nil }
func (f *fakeBackend) EditSecrets(context.Context, string, string) (*exec.Cmd, error) {
	return exec.Command("true"), nil
}
func (f *fakeBackend) AddStatus(_ context.Context, v repo.HostVars) (actions.AddStatus, error) {
	return actions.AddStatus{Host: v.Name, Steps: []actions.AddStep{{ID: actions.StepModule, Title: "host module", State: actions.StepTodo}}}, nil
}
func (f *fakeBackend) PreviewAdd(_ context.Context, v repo.HostVars) (actions.AddPreview, error) {
	return actions.AddPreview{Module: "# " + v.Name + " at " + v.Address + "\n", SopsLines: []string{"  - &" + v.Name + "_host age1REPLACEME_WEB1"}}, nil
}
func (f *fakeBackend) Add(_ context.Context, v repo.HostVars, sink func(contract.Event)) (actions.AddStatus, error) {
	obCalls.added = append(obCalls.added, v.Name+"@"+v.Address)
	sink(contract.Event{Event: contract.EventProgress, Host: v.Name, Step: "module", Message: "writing"})
	return actions.AddStatus{Host: v.Name, Steps: []actions.AddStep{{ID: actions.StepModule, Title: "host module", State: actions.StepDone}}}, nil
}
func (f *fakeBackend) ScanKey(context.Context, string) (string, error) { return "age1livekey", nil }
func (f *fakeBackend) PlanDeployWorktree(_ context.Context, host, as string) (*actions.DeployPlan, error) {
	inv := f.inv
	return actions.PlanDeploy(inv, inventory.Selector{Only: []string{"app1"}}, actions.DeployOptions{As: as, AllowDirty: true})
}
func (f *fakeBackend) GitDiff() *exec.Cmd { return exec.Command("true") }

func typeText(t *testing.T, m Model, s string) Model {
	for _, r := range s {
		m = run(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func TestKeysViewRegisters(t *testing.T) {
	obCalls = onboardCalls{}
	m := loaded(t, newFake())
	m = press(t, m, "K")
	s := screen(m)
	if m.mode != modeKeys || !strings.Contains(s, "placeholder") || !strings.Contains(s, "master key: available") {
		t.Fatalf("keys view:\n%s", s)
	}
	m = press(t, m, "down")
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != modeConfirm || m.confirm.kind != actRegister || !strings.Contains(screen(m), "REGISTER KEY") {
		t.Fatalf("confirm: mode=%v\n%s", m.mode, screen(m))
	}
	m = press(t, m, "y")
	m = settle(t, m)
	if m.mode != modeKeys || len(obCalls.registered) != 1 || obCalls.registered[0] != "mgr" {
		t.Errorf("after: mode=%v registered=%v", m.mode, obCalls.registered)
	}
}

func TestSecretsViewSetsWithMaskedValue(t *testing.T) {
	obCalls = onboardCalls{}
	m := loaded(t, newFake())
	m = press(t, m, "S")
	if s := screen(m); !strings.Contains(s, "app3_token") || !strings.Contains(s, "names only") {
		t.Fatalf("secrets view:\n%s", s)
	}
	m = press(t, m, "n")
	m = typeText(t, m, "api_key")
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeText(t, m, "hunter2")
	if strings.Contains(screen(m), "hunter2") {
		t.Fatal("the value was echoed")
	}
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = settle(t, m)
	if len(obCalls.set) != 1 || obCalls.set[0] != [3]string{"app3", "api_key", "hunter2"} {
		t.Errorf("set = %v", obCalls.set)
	}
}

func TestAddWizard(t *testing.T) {
	obCalls = onboardCalls{}
	m := loaded(t, newFake())
	m = press(t, m, "+")
	m = typeText(t, m, "Web1")
	m = press(t, m, "tab")
	if !strings.Contains(screen(m), "lower-case letters") {
		t.Errorf("no name problem shown:\n%s", screen(m))
	}
	m = press(t, m, "up")
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = typeText(t, m, "web1")
	m = press(t, m, "tab")
	m = typeText(t, m, "10.0.0.20")
	m = press(t, m, "tab")
	m = settle(t, m)
	if !strings.Contains(screen(m), "host key age1livekey") {
		t.Errorf("key check:\n%s", screen(m))
	}
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = settle(t, m)
	s := screen(m)
	if m.onboard.wiz.stage != wizPreview || !strings.Contains(s, "&web1_host") || !strings.Contains(s, "# web1 at 10.0.0.20") {
		t.Fatalf("preview (stage %d):\n%s", m.onboard.wiz.stage, s)
	}
	m = press(t, m, "y")
	m = settle(t, m)
	if m.onboard.wiz.stage != wizFinished || len(obCalls.added) != 1 || obCalls.added[0] != "web1@10.0.0.20" {
		t.Fatalf("after run: stage=%d added=%v\n%s", m.onboard.wiz.stage, obCalls.added, screen(m))
	}
	// d asks who to connect as, then opens the deploy dialog for the
	// working tree.
	m = press(t, m, "d")
	m = typeText(t, m, "mgr")
	m = run(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = settle(t, m)
	if m.mode != modeConfirm || m.confirm.kind != actDeploy || m.confirm.returnMode != modeAdd {
		t.Errorf("deploy: mode=%v kind=%v", m.mode, m.confirm.kind)
	}
}

func TestRebootSelfToggle(t *testing.T) {
	defer func(old string) { actions.ThisHost = old }(actions.ThisHost)
	actions.ThisHost = "mgr"
	m := loaded(t, newFake())
	m = press(t, m, "down", "space", "d", "p")
	s := screen(m)
	if !strings.Contains(s, "also reboot this machine") || m.confirm.self || m.confirm.deploy.RebootSelf {
		t.Fatalf("default: self=%v\n%s", m.confirm.self, s)
	}
	m = press(t, m, "s")
	if !m.confirm.self || !m.confirm.deploy.RebootSelf || !strings.Contains(screen(m), "this machine will reboot last") {
		t.Errorf("after s: self=%v plan=%v", m.confirm.self, m.confirm.deploy.RebootSelf)
	}
}
