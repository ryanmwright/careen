// Package tui is careen's terminal UI: a live view of the fleet, with keys
// to deploy, reboot, roll back, read logs and open a shell. Everything it
// does goes through the same functions as the CLI commands.
package tui

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/logs"
	"github.com/ryanmwright/careen/internal/reboot"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/status"
)

const (
	maxActivity = 500
	maxLogLines = 2000
)

type mode int

const (
	modeNormal mode = iota
	modeConfirm
	modeLogs
	modeHelp
	modeQuit    // confirming quit while an action runs
	modeInput   // typing a command for exec
	modeExecOut // exec's output, per host
	modeVulns   // vulnix findings
	modeKeys    // sops key drift
	modeSecrets // secrets by host
	modeAdd     // the add-host wizard
	modeAsk     // a running action asks what to do
)

type actionKind int

const (
	actDeploy actionKind = iota + 1
	actReboot
	actRollback
	actGC
	actExec
	actRegister // record a host's live key, re-key its files
	actRekey
	actUnset // remove a secret
	actAdd
)

func (k actionKind) String() string {
	return [...]string{"", "deploy", "reboot", "rollback", "gc", "exec", "key registration", "re-key", "secret removal", "add"}[k]
}

// confirmation is the dialog before an action.
type confirmation struct {
	kind     actionKind
	only     []string // hosts, in order
	host     inventory.Host
	inv      *inventory.Inventory
	reboot   string // deploy: "", policy or force; reboot: policy or force
	planning bool
	deploy   *actions.DeployPlan
	rbPlan   *actions.RebootPlan
	gcPlan   *actions.GCPlan
	execPlan *actions.ExecPlan
	command  string // exec
	sudo     bool   // exec
	replace  bool   // register: overwrite a drifted key
	self     bool   // deploy, reboot: may reboot the machine careen runs on
	file     string // unset
	secret   string // unset
	vars     repo.HostVars
	// returnMode is where the TUI goes once the action starts; the main
	// table unless the action came from another view.
	returnMode mode
	err        error
}

// logView is the full-screen journal of one host.
type logView struct {
	host    string
	unit    string
	entries []logs.Entry
	follow  bool // stay at the newest entry
	offset  int  // lines scrolled up from the bottom, when not following
	err     error
	loading bool
	cancel  context.CancelFunc
	ch      chan tea.Msg
}

type Model struct {
	be       Backend
	ctx      context.Context
	cancel   context.CancelFunc
	interval time.Duration
	now      func() time.Time

	inv        *inventory.Inventory
	rep        *status.Report
	refreshed  time.Time
	refreshing bool
	refreshErr error

	cursorHost string   // the selected host, kept across refreshes
	marks      []string // in the order marked
	inUnits    bool     // focus is on the failed units in the detail pane
	unitIdx    int

	running  bool
	runKind  actionKind
	activity []string
	events   chan tea.Msg
	flash    string
	flashErr bool

	mode    mode
	confirm confirmation
	logs    logView
	extra   extraState
	onboard onboardState
	ask     *pendingAsk

	width, height int
}

// New creates the model. interval is how often the status refreshes.
// It starts out refreshing: Init loads the status.
func New(be Backend, interval time.Duration) Model {
	ctx, cancel := context.WithCancel(context.Background())
	return Model{be: be, ctx: ctx, cancel: cancel, interval: interval, now: time.Now, width: 100, height: 30, extra: newExtraState(), refreshing: true}
}

// Messages.
type (
	statusMsg struct {
		inv *inventory.Inventory
		rep status.Report
		err error
		// last: the fleet careen saw last, shown until the refresh lands.
		last bool
	}
	tickMsg time.Time
	invMsg  struct {
		inv *inventory.Inventory
		err error
	}
	rbPlanMsg struct {
		plan *actions.RebootPlan
		err  error
	}
	eventMsg   contract.Event
	actionDone struct {
		summary string
		err     error
		exec    *actions.ExecDone
		add     *actions.AddStatus
	}
	// askMsg is a running action's question; the answer goes to reply.
	askMsg struct {
		q     contract.Question
		reply chan string
	}
	logEntryMsg logs.Entry
	logEndMsg   struct{ err error }
	sudoMsg     struct{ err error }
	shellMsg    struct{ err error }
)

// Init shows the fleet careen saw last while the flake is fetched and
// evaluated, which can take a while after a new commit.
func (m Model) Init() tea.Cmd {
	be, ctx := m.be, m.ctx
	last := func() tea.Msg {
		inv, rep, ok := be.LastStatus(ctx)
		if !ok {
			return nil
		}
		return statusMsg{inv: inv, rep: rep, last: true}
	}
	return tea.Batch(last, m.refresh(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) refresh() tea.Cmd {
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		inv, rep, err := be.Status(ctx)
		return statusMsg{inv: inv, rep: rep, err: err}
	}
}

// wait reads the next message an action or log stream sent.
func wait(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		var cmd tea.Cmd
		if !m.refreshing && m.now().Sub(m.refreshed) >= m.interval {
			m.refreshing = true
			cmd = m.refresh()
		}
		return m, tea.Batch(cmd, tick())

	case statusMsg:
		if msg.last {
			if m.rep == nil {
				m.inv, m.rep, m.refreshed = msg.inv, &msg.rep, m.now()
				m.keepSelection()
				m.loadVulnCache()
			}
			return m, nil
		}
		m.refreshing = false
		m.refreshErr = msg.err
		if msg.err == nil {
			m.inv, m.rep, m.refreshed = msg.inv, &msg.rep, m.now()
			m.keepSelection()
			m.loadVulnCache()
		}
		return m, nil

	case invMsg:
		return m.planned(msg)

	case rbPlanMsg:
		if m.mode == modeConfirm && m.confirm.kind == actReboot {
			m.confirm.planning = false
			m.confirm.rbPlan, m.confirm.err = msg.plan, msg.err
		}
		return m, nil

	case eventMsg:
		m.activity = append(m.activity, eventLine(contract.Event(msg)))
		if len(m.activity) > maxActivity {
			m.activity = m.activity[len(m.activity)-maxActivity:]
		}
		return m, wait(m.events)

	case askMsg:
		prev := m.mode
		if prev == modeConfirm || prev == modeInput || prev == modeQuit {
			prev = modeNormal
		}
		m.ask = &pendingAsk{q: msg.q, reply: msg.reply, prev: prev}
		m.mode = modeAsk
		return m, wait(m.events)

	case actionDone:
		m.running = false
		m.setFlash(msg.summary, msg.err != nil)
		if msg.exec != nil {
			m.extra.execDone, m.extra.execIdx = msg.exec, 0
			if m.mode == modeNormal {
				m.mode = modeExecOut
			}
		}
		if msg.add != nil {
			w := &m.onboard.wiz
			w.status, w.stage, w.err = msg.add, wizFinished, msg.err
		}
		m.refreshing = true
		switch m.mode {
		case modeKeys:
			return m, tea.Batch(m.refresh(), m.loadKeys())
		case modeSecrets:
			return m, tea.Batch(m.refresh(), m.loadSecrets())
		}
		return m, m.refresh()

	case sudoMsg:
		if msg.err != nil {
			m.setFlash("sudo: "+msg.err.Error(), true)
			return m, nil
		}
		return m.start()

	case shellMsg:
		if msg.err != nil {
			m.setFlash("shell: "+msg.err.Error(), true)
		}
		return m, nil

	case logEntryMsg:
		if m.mode == modeLogs {
			m.logs.loading = false
			m.logs.entries = append(m.logs.entries, logs.Entry(msg))
			if len(m.logs.entries) > maxLogLines {
				m.logs.entries = m.logs.entries[len(m.logs.entries)-maxLogLines:]
			}
			return m, wait(m.logs.ch)
		}
		return m, nil

	case logEndMsg:
		if m.mode == modeLogs {
			m.logs.loading = false
			m.logs.err = msg.err
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.key(msg)
	}
	if nm, cmd, ok := m.updateOnboard(msg); ok {
		return nm, cmd
	}
	if nm, cmd, ok := m.updateExtra(msg); ok {
		return nm, cmd
	}
	return m, nil
}

func (m *Model) setFlash(s string, isErr bool) { m.flash, m.flashErr = s, isErr }

// rows is the hosts in the table, in status order.
func (m Model) rows() []status.Row {
	if m.rep == nil {
		return nil
	}
	return m.rep.Hosts
}

func (m Model) cursor() int {
	i := slices.IndexFunc(m.rows(), func(r status.Row) bool { return r.Host == m.cursorHost })
	return max(i, 0)
}

func (m Model) current() (status.Row, bool) {
	rows := m.rows()
	if len(rows) == 0 {
		return status.Row{}, false
	}
	return rows[m.cursor()], true
}

func (m Model) host(name string) (inventory.Host, bool) {
	if m.inv == nil {
		return inventory.Host{}, false
	}
	i := slices.IndexFunc(m.inv.Hosts, func(h inventory.Host) bool { return h.Name == name })
	if i < 0 {
		return inventory.Host{}, false
	}
	return m.inv.Hosts[i], true
}

// keepSelection holds the cursor and marks on the same hosts across a
// refresh, dropping hosts that left the inventory.
func (m *Model) keepSelection() {
	rows := m.rows()
	has := func(name string) bool {
		return slices.ContainsFunc(rows, func(r status.Row) bool { return r.Host == name })
	}
	if !has(m.cursorHost) && len(rows) > 0 {
		m.cursorHost = rows[0].Host
	}
	m.marks = slices.DeleteFunc(m.marks, func(n string) bool { return !has(n) })
	if r, ok := m.current(); ok && (r.State == nil || m.unitIdx >= len(r.FailedUnits)) {
		m.unitIdx = 0
		if r.State == nil || len(r.FailedUnits) == 0 {
			m.inUnits = false
		}
	}
}

// targets is the marked hosts in mark order, or the cursor host.
func (m Model) targets() []string {
	if len(m.marks) > 0 {
		return slices.Clone(m.marks)
	}
	if r, ok := m.current(); ok {
		return []string{r.Host}
	}
	return nil
}

func (m Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		m.cancel()
		return m, tea.Quit
	}
	switch m.mode {
	case modeHelp:
		m.mode = modeNormal
		return m, nil
	case modeQuit:
		if k == "y" {
			m.cancel()
			return m, tea.Quit
		}
		m.mode = modeNormal
		return m, nil
	case modeConfirm:
		return m.confirmKey(k)
	case modeLogs:
		return m.logsKey(k)
	case modeInput:
		return m.inputKey(msg)
	case modeExecOut:
		return m.execOutKey(k)
	case modeVulns:
		return m.vulnsKey(k)
	case modeKeys:
		return m.keysKey(k)
	case modeSecrets:
		return m.secretsKey(msg)
	case modeAdd:
		return m.wizardKey(msg)
	case modeAsk:
		return m.askKey(k)
	}

	m.flash = ""
	rows := m.rows()
	switch k {
	case "q":
		if m.running {
			m.mode = modeQuit
			return m, nil
		}
		m.cancel()
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
	case "up", "k":
		if m.inUnits {
			m.unitIdx = max(m.unitIdx-1, 0)
		} else if c := m.cursor(); c > 0 {
			m.cursorHost = rows[c-1].Host
			m.unitIdx = 0
		}
	case "down", "j":
		if m.inUnits {
			if r, ok := m.current(); ok && r.State != nil {
				m.unitIdx = min(m.unitIdx+1, len(r.FailedUnits)-1)
			}
		} else if c := m.cursor(); c < len(rows)-1 {
			m.cursorHost = rows[c+1].Host
			m.unitIdx = 0
		}
	case "tab":
		if r, ok := m.current(); ok && r.State != nil && len(r.FailedUnits) > 0 {
			m.inUnits = !m.inUnits
		} else {
			m.inUnits = false
		}
	case "space":
		if r, ok := m.current(); ok {
			if i := slices.Index(m.marks, r.Host); i >= 0 {
				m.marks = slices.Delete(m.marks, i, i+1)
			} else {
				m.marks = append(m.marks, r.Host)
			}
		}
	case "a":
		for _, r := range rows {
			if !slices.Contains(m.marks, r.Host) {
				m.marks = append(m.marks, r.Host)
			}
		}
	case "A":
		m.marks = nil
	case "g":
		if !m.refreshing {
			m.refreshing = true
			return m, m.refresh()
		}
	case "d", "r", "b", "G", "x":
		if m.running {
			m.setFlash("a "+m.runKind.String()+" is running; wait for it to finish", true)
			return m, nil
		}
		if k == "x" {
			return m.openInput()
		}
		return m.openConfirm(map[string]actionKind{"d": actDeploy, "r": actReboot, "b": actRollback, "G": actGC}[k])
	case "v":
		return m.openVulns(false)
	case "+":
		return m.openWizard()
	case "K":
		return m.openKeys()
	case "S":
		return m.openSecrets()
	case "o":
		if m.extra.execDone != nil {
			m.mode = modeExecOut
		}
	case "l":
		return m.openLogs()
	case "s":
		r, ok := m.current()
		if !ok {
			return m, nil
		}
		h, ok := m.host(r.Host)
		if !ok {
			return m, nil
		}
		return m, tea.ExecProcess(m.be.Shell(h), func(err error) tea.Msg { return shellMsg{err} })
	}
	return m, nil
}

// openConfirm loads the flake fresh and plans the action; the dialog shows
// "planning…" until it's ready.
func (m Model) openConfirm(kind actionKind) (tea.Model, tea.Cmd) {
	only := m.targets()
	if len(only) == 0 {
		return m, nil
	}
	c := confirmation{kind: kind, only: only, planning: true}
	switch kind {
	case actReboot:
		c.reboot = reboot.Policy
	case actRollback:
		r, _ := m.current()
		h, ok := m.host(r.Host)
		if !ok {
			return m, nil
		}
		c.host, c.only, c.planning = h, []string{h.Name}, false
	}
	m.mode, m.confirm = modeConfirm, c
	if kind == actRollback {
		return m, nil
	}
	be, ctx := m.be, m.ctx
	return m, func() tea.Msg {
		inv, err := be.Inventory(ctx)
		return invMsg{inv, err}
	}
}

// planned continues openConfirm once the flake is loaded.
func (m Model) planned(msg invMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeConfirm || m.confirm.inv != nil {
		return m, nil
	}
	c := &m.confirm
	if msg.err != nil {
		c.planning, c.err = false, msg.err
		return m, nil
	}
	c.inv = msg.inv
	switch c.kind {
	case actDeploy:
		c.planning = false
		c.deploy, c.err = m.be.PlanDeploy(c.inv, c.only, c.reboot, c.self)
	case actReboot:
		return m, m.planReboot()
	case actGC:
		return m, m.planGC()
	case actExec:
		c.planning = false
		c.execPlan, c.err = m.be.PlanExec(c.inv, c.only, c.command, c.sudo)
	}
	return m, nil
}

func (m Model) planReboot() tea.Cmd {
	be, ctx, c := m.be, m.ctx, m.confirm
	return func() tea.Msg {
		p, err := be.PlanReboot(ctx, c.inv, c.only, c.reboot, c.self)
		return rbPlanMsg{p, err}
	}
}

func (m Model) confirmKey(k string) (tea.Model, tea.Cmd) {
	c := &m.confirm
	switch k {
	case "esc", "q":
		m.mode = c.returnMode
		if m.mode == modeConfirm {
			m.mode = modeNormal
		}
		return m, nil
	case "n", "p", "f", "s":
		if c.planning || c.inv == nil {
			return m, nil
		}
		if k == "s" {
			c.self = !c.self
		}
		switch c.kind {
		case actDeploy:
			if k != "s" {
				c.reboot = map[string]string{"n": "", "p": reboot.Policy, "f": reboot.Force}[k]
			}
			c.deploy, c.err = m.be.PlanDeploy(c.inv, c.only, c.reboot, c.self)
		case actReboot:
			if k == "f" || k == "s" {
				if k == "f" {
					c.reboot = map[bool]string{true: reboot.Policy, false: reboot.Force}[c.reboot == reboot.Force]
				}
				c.planning, c.rbPlan = true, nil
				return m, m.planReboot()
			}
		}
		return m, nil
	case "y":
		if c.planning || c.err != nil {
			return m, nil
		}
		// Fleet actions need at least one host to act on; the checkout
		// actions (keys, secrets, add) name theirs in only.
		hosts := m.confirmTargets()
		if len(hosts) == 0 && c.kind <= actExec {
			return m, nil
		}
		needsRoot := c.kind != actExec || c.sudo
		if needsRoot && m.be.NeedsSudo(m.ctx, hosts) {
			return m, tea.ExecProcess(exec.Command("sudo", "-v"), func(err error) tea.Msg { return sudoMsg{err} })
		}
		return m.start()
	}
	return m, nil
}

// confirmTargets is the hosts the confirmed action will touch.
func (m Model) confirmTargets() []inventory.Host {
	c := m.confirm
	switch c.kind {
	case actDeploy:
		if c.deploy != nil {
			return c.deploy.Targets()
		}
	case actReboot:
		if c.rbPlan != nil {
			return c.rbPlan.Targets()
		}
	case actRollback:
		return []inventory.Host{c.host}
	case actGC:
		if c.gcPlan != nil {
			return c.gcPlan.Targets()
		}
	case actExec:
		if c.execPlan != nil {
			return c.execPlan.Targets()
		}
	}
	return nil
}

// start runs the confirmed action in the background. Its events arrive as
// eventMsg and its end as actionDone.
func (m Model) start() (tea.Model, tea.Cmd) {
	c := m.confirm
	m.mode = c.returnMode
	m.running, m.runKind = true, c.kind
	m.activity = append(m.activity, fmt.Sprintf("── %s %s ──", c.kind, strings.Join(c.only, ", ")))
	ch := make(chan tea.Msg, 256)
	m.events = ch
	sink := func(ev contract.Event) { ch <- eventMsg(ev) }
	ask := askVia(ch)
	switch {
	case c.kind == actDeploy && c.deploy != nil:
		c.deploy.WithAsk(ask)
	case c.kind == actReboot && c.rbPlan != nil:
		c.rbPlan.WithAsk(ask)
	}
	be, ctx := m.be, m.ctx
	go func() {
		defer close(ch)
		var summary string
		var err error
		switch c.kind {
		case actDeploy:
			var done actions.DeployDone
			done, err = be.Deploy(ctx, c.deploy, sink)
			summary = deploySummary(done)
		case actReboot:
			var done actions.RebootDone
			done, err = be.Reboot(ctx, c.rbPlan, sink)
			summary = rebootSummary(done)
			if err != nil {
				// Keep what's left to do by hand (the post command after
				// an abort), which the error alone doesn't say.
				ch <- actionDone{"reboot failed: " + err.Error() + manualNotes(done), err, nil, nil}
				return
			}
		case actRollback:
			var done actions.RollbackDone
			done, err = be.Rollback(ctx, c.host, sink)
			summary = c.host.Name + ": " + done.Result.Status
		case actGC:
			var done actions.GCDone
			done, err = be.GC(ctx, c.gcPlan, sink)
			summary = fmt.Sprintf("gc: freed %s on %d host(s)", actions.Bytes(done.Freed), len(done.Hosts))
		case actExec:
			done, err := be.Exec(ctx, c.execPlan, sink)
			ok := 0
			for _, r := range done.Hosts {
				if r.OK() {
					ok++
				}
			}
			ch <- actionDone{fmt.Sprintf("exec: %d ok, %d failed (o shows the output again)", ok, len(done.Hosts)-ok), err, &done, nil}
			return
		case actRegister:
			err = be.RegisterKey(ctx, c.only[0], c.replace, sink)
			summary = c.only[0] + "'s key registered; review and commit"
		case actRekey:
			err = be.RekeyHost(ctx, c.only[0], sink)
			summary = c.only[0] + "'s files re-keyed; review and commit"
		case actUnset:
			err = be.UnsetSecret(ctx, c.only[0], c.file, c.secret)
			summary = c.secret + " removed from " + c.only[0] + "'s secrets"
		case actAdd:
			st, err := be.Add(ctx, c.vars, sink)
			summary = c.vars.Name + " onboarded; review the diff and commit"
			if err != nil {
				summary = "add " + c.vars.Name + ": " + err.Error()
			}
			ch <- actionDone{summary, err, nil, &st}
			return
		}
		if err != nil {
			summary = c.kind.String() + " failed: " + err.Error()
		}
		ch <- actionDone{summary, err, nil, nil}
	}()
	return m, wait(ch)
}

func deploySummary(d actions.DeployDone) string {
	var parts []string
	for _, h := range d.Hosts {
		s := h.Host + " " + h.Status
		switch {
		case h.Reboot != nil && h.Reboot.Rebooted:
			s += ", rebooted"
		case len(h.RebootPending) > 0:
			s += ", reboot pending"
		}
		parts = append(parts, s)
	}
	return "deploy: " + strings.Join(parts, "; ")
}

func rebootSummary(d actions.RebootDone) string {
	var parts []string
	for _, h := range d.Hosts {
		switch {
		case h.Error != nil:
			parts = append(parts, h.Host+" failed")
		case h.Rebooted:
			parts = append(parts, h.Host+" rebooted")
		case h.Skip != "" && len(h.HookRuns) > 0:
			// Skipped after its hooks ran, e.g. a timed-out pre answered skip.
			parts = append(parts, h.Host+" skipped: "+h.Skip)
		}
	}
	if len(parts) == 0 {
		return "reboot: nothing needed a reboot"
	}
	return "reboot: " + strings.Join(parts, "; ") + manualNotes(d)
}

// manualNotes is what the hosts left to do by hand, as "; host: note".
func manualNotes(d actions.RebootDone) string {
	var s string
	for _, h := range d.Hosts {
		if h.Manual != "" {
			s += "; " + h.Host + ": " + h.Manual
		}
	}
	return s
}

// openLogs follows the journal of the cursor host: the selected failed
// unit's when the focus is on the units, otherwise the whole journal.
func (m Model) openLogs() (tea.Model, tea.Cmd) {
	r, ok := m.current()
	if !ok {
		return m, nil
	}
	h, ok := m.host(r.Host)
	if !ok {
		return m, nil
	}
	unit := ""
	if m.inUnits && r.State != nil && m.unitIdx < len(r.FailedUnits) {
		unit = r.FailedUnits[m.unitIdx]
	}
	ctx, cancel := context.WithCancel(m.ctx)
	ch := make(chan tea.Msg, 256)
	m.mode = modeLogs
	m.logs = logView{host: h.Name, unit: unit, follow: true, loading: true, cancel: cancel, ch: ch}
	be := m.be
	// Sends give up once the view is closed, so nothing blocks on a channel
	// no one reads anymore.
	send := func(msg tea.Msg) {
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(ch)
		err := be.FollowLogs(ctx, h, unit, func(e logs.Entry) { send(logEntryMsg(e)) })
		send(logEndMsg{err})
	}()
	return m, wait(ch)
}

func (m Model) logsKey(k string) (tea.Model, tea.Cmd) {
	lv := &m.logs
	page := max(m.height-3, 1)
	switch k {
	case "esc", "q", "l":
		lv.cancel()
		m.mode = modeNormal
	case "f":
		lv.follow = !lv.follow
		if lv.follow {
			lv.offset = 0
		}
	case "up", "k":
		lv.follow = false
		lv.offset = min(lv.offset+1, max(len(lv.entries)-1, 0))
	case "down", "j":
		lv.offset = max(lv.offset-1, 0)
	case "pgup":
		lv.follow = false
		lv.offset = min(lv.offset+page, max(len(lv.entries)-1, 0))
	case "pgdown":
		lv.offset = max(lv.offset-page, 0)
	case "end":
		lv.follow, lv.offset = true, 0
	}
	return m, nil
}
