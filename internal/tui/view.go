package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/format"
	"github.com/ryanmwright/careen/internal/reboot"
	"github.com/ryanmwright/careen/internal/status"
)

var (
	styleTitle    = lipgloss.NewStyle().Bold(true)
	styleFaint    = lipgloss.NewStyle().Faint(true)
	styleSection  = lipgloss.NewStyle().Bold(true).Faint(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleErr      = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleKey      = lipgloss.NewStyle().Bold(true)
)

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "careen"
	return v
}

func (m Model) render() string {
	var lines []string
	switch m.mode {
	case modeLogs:
		lines = m.logsView()
	case modeHelp:
		lines = helpView()
	case modeExecOut:
		lines = m.execOutView()
	case modeVulns:
		lines = m.vulnsView()
	case modeKeys:
		lines = m.keysView()
	case modeSecrets:
		lines = m.secretsView()
	case modeAdd:
		lines = m.wizardView()
	default:
		lines = m.mainView()
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return strings.Join(lines, "\n")
}

func (m Model) mainView() []string {
	lines := []string{m.header()}
	if m.rep == nil {
		if m.refreshErr != nil {
			return append(lines, "", styleErr.Render(m.refreshErr.Error()))
		}
		return append(lines, "", "probing the fleet…")
	}
	lines = append(lines, "")
	lines = append(lines, m.table()...)
	lines = append(lines, "")
	lines = append(lines, m.detail()...)

	var bottom []string
	switch m.mode {
	case modeConfirm:
		bottom = m.confirmView()
	case modeInput:
		bottom = m.inputView()
	case modeAsk:
		bottom = append(m.activityView(m.height-len(lines)-8), m.askView()...)
	case modeQuit:
		bottom = []string{"", styleWarn.Render("A " + m.runKind.String() + " is running. Quitting stops it; a host that is mid-switch rolls back by itself."),
			"Quit anyway? " + styleKey.Render("y") + "/" + styleKey.Render("n")}
	default:
		bottom = append([]string{""}, m.activityView(m.height-len(lines)-4)...)
	}
	lines = append(lines, bottom...)
	// Keep the key hints on the bottom row.
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	return append(lines, m.footer())
}

func (m Model) header() string {
	if m.rep == nil {
		return styleTitle.Render("careen")
	}
	s := styleTitle.Render("careen") + "  " + format.FlakeLine(m.rep.Flake)
	age := m.now().Sub(m.refreshed).Truncate(time.Second)
	switch {
	case m.refreshing:
		s += styleFaint.Render("  refreshing…")
	case age > 2*m.interval:
		s += "  " + styleWarn.Render(fmt.Sprintf("stale: last update %s ago", age))
	default:
		s += styleFaint.Render(fmt.Sprintf("  updated %s ago", age))
	}
	if m.refreshErr != nil {
		s += "  " + styleErr.Render("refresh failed: "+m.refreshErr.Error())
	}
	return s
}

func (m Model) table() []string {
	header := []string{"", "HOST", "REVISION", "DRIFT", "REBOOT", "STATE", "FAILED", "UPTIME", "STORE", "SWITCHED", "VULNS"}
	rows := [][]string{header}
	styles := [][]lipgloss.Style{nil}
	now := m.now()
	for _, r := range m.rows() {
		mark := " "
		if i := slices.Index(m.marks, r.Host); i >= 0 {
			mark = fmt.Sprint(i + 1)
		}
		if r.Error != nil {
			rows = append(rows, []string{mark, r.Host, r.Error.Code + ": " + r.Error.Message})
			styles = append(styles, []lipgloss.Style{{}, {}, styleErr})
			continue
		}
		rows = append(rows, []string{
			mark, r.Host, format.Revision(r.State), r.Drift, format.Reboot(r.State), format.Dash(r.SystemState),
			fmt.Sprint(len(r.FailedUnits)), format.Duration(time.Duration(r.UptimeSeconds) * time.Second),
			format.Store(r.StoreUsed, r.StoreSize), format.Ago(r.LastSwitch, now), m.vulnsCell(r.Host),
		})
		st := make([]lipgloss.Style, len(header))
		st[3] = map[bool]lipgloss.Style{true: styleOK, false: styleWarn}[r.Drift == status.DriftCurrent]
		if r.RebootPending {
			st[4] = styleWarn
		}
		st[5] = map[bool]lipgloss.Style{true: styleOK, false: styleWarn}[r.SystemState == "running"]
		if len(r.FailedUnits) > 0 {
			st[6] = styleErr
		}
		if c := m.extra.vulnCache[r.Host]; c != nil {
			st[10] = map[bool]lipgloss.Style{true: styleErr, false: lipgloss.Style{}}[c.Summary.Critical > 0]
		}
		styles = append(styles, st)
	}

	// Column widths from the plain text; an error row's message spans the
	// rest of the line.
	widths := make([]int, len(header))
	for _, row := range rows {
		if len(row) < len(header) {
			continue
		}
		for i, c := range row {
			widths[i] = max(widths[i], len(c))
		}
	}
	cur := m.cursor()
	out := make([]string, len(rows))
	for ri, row := range rows {
		var cells []string
		for i, c := range row {
			cell := c
			if i < len(row)-1 {
				cell = fmt.Sprintf("%-*s", widths[i], c)
			}
			if ri == 0 {
				cell = styleSection.Render(cell)
			} else if styles[ri] != nil && i < len(styles[ri]) {
				cell = styles[ri][i].Render(cell)
			}
			cells = append(cells, cell)
		}
		line := strings.Join(cells, "  ")
		if ri > 0 && ri-1 == cur && !m.inUnits {
			line = styleSelected.Render(ansi.Strip(line))
		}
		out[ri] = line
	}
	return out
}

func (m Model) detail() []string {
	r, ok := m.current()
	if !ok {
		return nil
	}
	h, _ := m.host(r.Host)
	lines := []string{styleSection.Render("HOST "+r.Host) + styleFaint.Render("  "+r.Address)}
	if r.Error != nil {
		return append(lines, styleErr.Render(r.Error.Code+": "+r.Error.Message))
	}
	rev := format.Revision(r.State) + " (" + r.Drift
	if m.rep != nil && r.Drift != status.DriftCurrent {
		rev += "; flake is at " + format.Short(m.rep.Flake.Revision)
	}
	rev += ")"
	lines = append(lines,
		"revision   "+rev,
		"reboot     "+map[bool]string{true: styleWarn.Render("needed: " + format.Reboot(r.State)), false: "not needed"}[r.RebootPending],
		fmt.Sprintf("system     %s, NixOS %s, %d generations, last switch %s", format.Dash(r.SystemState), format.Dash(r.NixOSVersion), r.Generations, format.Ago(r.LastSwitch, m.now())),
		fmt.Sprintf("policy     reboot %s, deploy %s, build %s", format.Dash(h.RebootPolicy), format.YesNo(h.Deploy), format.Dash(h.BuildOn)),
	)
	if c := m.extra.vulnCache[r.Host]; c != nil {
		lines = append(lines, fmt.Sprintf("vulns      %s in %d packages, scanned %s (v for details)", c.Summary.Short(), len(c.Findings), format.Ago(&c.ScannedAt, m.now())))
	} else {
		lines = append(lines, "vulns      "+styleFaint.Render("not scanned for this system (v to scan)"))
	}
	if len(r.FailedUnits) == 0 {
		return append(lines, "failed     none")
	}
	lines = append(lines, "failed     "+styleFaint.Render("(tab to pick one, l for its logs)"))
	for i, u := range r.FailedUnits {
		l := "  " + styleErr.Render(u)
		if m.inUnits && i == m.unitIdx {
			l = "  " + styleSelected.Render(u)
		}
		lines = append(lines, l)
	}
	return lines
}

// activityView is the last n lines of action events, plus the flash.
func (m Model) activityView(n int) []string {
	title := "ACTIVITY"
	if m.running {
		title += styleWarn.Render("  " + m.runKind.String() + " running")
	}
	lines := []string{styleSection.Render(title)}
	var flash []string
	if m.flash != "" {
		style := map[bool]lipgloss.Style{true: styleErr, false: styleOK}[m.flashErr]
		for _, l := range wrapLines(m.flash, m.width, 4) {
			flash = append(flash, style.Render(l))
		}
	}
	act := m.activity
	if n = max(n-1-max(len(flash), 1), 1); len(act) > n {
		act = act[len(act)-n:]
	}
	if len(act) == 0 {
		act = []string{styleFaint.Render("nothing yet: mark hosts with space, then d to deploy or r to reboot")}
	}
	lines = append(lines, act...)
	return append(lines, flash...)
}

// wrapLines wraps s to width, keeping its last n lines, so a long summary
// (an error, then the command to run by hand) isn't cut at the screen edge.
func wrapLines(s string, width, n int) []string {
	if width <= 0 {
		return []string{s}
	}
	ls := strings.Split(ansi.Wrap(s, width-1, " "), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
		ls[0] = "…" + ls[0]
	}
	return ls
}

func eventLine(ev contract.Event) string {
	host := ev.Host
	if host == "" {
		host = "-"
	}
	msg := ev.Message
	if ev.Error != nil {
		msg = ev.Error.Message
	}
	step := ev.Step
	if step == "" {
		step = ev.Event
	}
	line := fmt.Sprintf("%s  %-10s %-9s %s", ev.Time.Local().Format("15:04:05"), host, step, msg)
	switch ev.Event {
	case contract.EventError:
		return styleErr.Render(line)
	case contract.EventDecision:
		return styleWarn.Render(line)
	case contract.EventDone, contract.EventStarted:
		return styleFaint.Render(line)
	}
	return line
}

func (m Model) confirmView() []string {
	c := m.confirm
	lines := []string{""}
	switch {
	case c.planning:
		return append(lines, styleSection.Render(strings.ToUpper(c.kind.String())), "planning…")
	case c.err != nil:
		return append(lines, styleSection.Render(strings.ToUpper(c.kind.String())), styleErr.Render(c.err.Error()), "", styleKey.Render("esc")+" close")
	}
	switch c.kind {
	case actDeploy:
		lines = append(lines, deployPlanLines(c.deploy)...)
		lines = append(lines, "", "reboot afterwards: "+choice("n", "none", c.reboot == "")+"  "+choice("p", "per policy", c.reboot == reboot.Policy)+"  "+choice("f", "force", c.reboot == reboot.Force))
		if slices.ContainsFunc(c.deploy.Hosts, func(h actions.PlanHost) bool { return h.Local }) {
			lines = append(lines, selfChoice(c.self))
		}
		if len(c.deploy.Targets()) == 0 {
			return append(lines, "", styleWarn.Render("nothing to deploy"), styleKey.Render("esc")+" close")
		}
		lines = append(lines, "", styleKey.Render("y")+fmt.Sprintf(" deploy %d host(s)   ", len(c.deploy.Targets()))+styleKey.Render("esc")+" cancel")
	case actReboot:
		lines = append(lines, rebootPlanLines(c.rbPlan)...)
		lines = append(lines, "", choice("f", "force (reboot even if not needed)", c.reboot == reboot.Force), selfChoice(c.self))
		if c.rbPlan.Count() == 0 {
			return append(lines, "", styleWarn.Render("nothing to reboot"), styleKey.Render("esc")+" close")
		}
		lines = append(lines, "", styleKey.Render("y")+fmt.Sprintf(" reboot %d host(s)   ", c.rbPlan.Count())+styleKey.Render("esc")+" cancel")
	case actGC:
		lines = append(lines, gcPlanLines(c.gcPlan)...)
		if len(c.gcPlan.Targets()) == 0 {
			return append(lines, "", styleWarn.Render("no reachable hosts"), styleKey.Render("esc")+" close")
		}
		lines = append(lines, "", styleKey.Render("y")+fmt.Sprintf(" collect on %d host(s)   ", len(c.gcPlan.Targets()))+styleKey.Render("esc")+" cancel")
	case actExec:
		lines = append(lines, execPlanLines(c.execPlan)...)
		lines = append(lines, "", styleKey.Render("y")+fmt.Sprintf(" run on %d host(s)   ", len(c.execPlan.Hosts))+styleKey.Render("esc")+" cancel")
	case actRegister:
		what := "Record " + c.only[0] + "'s live host key in .sops.yaml and re-key its files."
		if c.replace {
			what = styleErr.Render("Replace "+c.only[0]+"'s recorded key with its live one (it was reinstalled)") + ", then re-key its files."
		}
		lines = append(lines, styleSection.Render("REGISTER KEY"), what, styleFaint.Render("Re-keying needs the master key. Changes stay uncommitted."),
			"", styleKey.Render("y")+" go   "+styleKey.Render("esc")+" cancel")
	case actRekey:
		lines = append(lines, styleSection.Render("RE-KEY"), "Re-encrypt "+c.only[0]+"'s files and the shared files to the keys .sops.yaml gives them.",
			styleFaint.Render("Needs the master key. Changes stay uncommitted."), "", styleKey.Render("y")+" go   "+styleKey.Render("esc")+" cancel")
	case actUnset:
		lines = append(lines, styleSection.Render("REMOVE SECRET"), "Remove "+c.secret+" from "+c.only[0]+"'s "+c.file+".",
			"", styleKey.Render("y")+" remove   "+styleKey.Render("esc")+" cancel")
	case actRollback:
		lines = append(lines, styleSection.Render("ROLLBACK"),
			"Switch "+c.host.Name+" back to its previous system generation, then check its health.",
			"", styleKey.Render("y")+" roll back   "+styleKey.Render("esc")+" cancel")
	}
	return lines
}

// selfChoice is the toggle for rebooting the machine careen runs on.
func selfChoice(on bool) string {
	s := choice("s", "also reboot this machine (careen exits)", on)
	if on {
		s += "  " + styleWarn.Render("this machine will reboot last")
	}
	return s
}

func choice(key, label string, on bool) string {
	s := "[" + key + "] " + label
	if on {
		return styleSelected.Render(s)
	}
	return s
}

func deployPlanLines(p *actions.DeployPlan) []string {
	lines := []string{styleSection.Render("DEPLOY") + "  " + format.FlakeLine(p.Flake)}
	for _, h := range p.Hosts {
		where := h.BuildOn
		if h.Builder != "" {
			where += " on " + h.Builder
		}
		note := ""
		if h.Local {
			note = styleWarn.Render("  this machine, deployed last")
		}
		lines = append(lines, fmt.Sprintf("  %-10s build %-8s %s", h.Host, where, h.Operation)+note)
		if hooks := p.RebootHooks(h.Host); p.Reboot != "" && len(hooks) > 0 {
			lines = append(lines, styleFaint.Render("             reboot hooks: "+strings.Join(hooks, ", ")))
		}
	}
	for _, s := range p.Skipped {
		lines = append(lines, styleFaint.Render("  skipping "+s.Host+": "+s.Reason))
	}
	return lines
}

func rebootPlanLines(p *actions.RebootPlan) []string {
	lines := []string{styleSection.Render("REBOOT") + "  one at a time, in this order"}
	for _, h := range p.Hosts {
		why := "-"
		if len(h.Reasons) > 0 {
			why = strings.Join(h.Reasons, ",")
		}
		what := styleWarn.Render("reboot")
		if h.Skip != "" {
			what = styleFaint.Render("skip: " + h.Skip)
		}
		lines = append(lines, fmt.Sprintf("  %-10s needs: %-28s ", h.Host, why)+what)
		if len(h.Hooks) > 0 && h.Skip == "" {
			hooks := "hooks: " + strings.Join(h.Hooks, ", ")
			if p.NoHooks {
				hooks += " (off: --no-hooks)"
			}
			lines = append(lines, styleFaint.Render("             "+hooks))
		}
	}
	return lines
}

func (m Model) logsView() []string {
	lv := m.logs
	title := "LOGS " + lv.host
	if lv.unit != "" {
		title += " " + lv.unit
	}
	state := "following"
	if !lv.follow {
		state = "paused"
	}
	lines := []string{styleSection.Render(title) + styleFaint.Render("  "+state)}
	n := max(m.height-3, 1)
	entries := lv.entries
	end := len(entries)
	if !lv.follow {
		end = max(len(entries)-lv.offset, 0)
	}
	start := max(end-n, 0)
	for _, e := range entries[start:end] {
		l := e.Time.Local().Format("Jan 02 15:04:05") + " " + e.Unit + ": " + e.Message
		switch {
		case e.Priority <= 3:
			l = styleErr.Render(l)
		case e.Priority == 4:
			l = styleWarn.Render(l)
		}
		lines = append(lines, l)
	}
	switch {
	case lv.loading:
		lines = append(lines, styleFaint.Render("reading the journal…"))
	case lv.err != nil:
		lines = append(lines, styleErr.Render(lv.err.Error()))
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	return append(lines, styleFaint.Render("↑/↓ pgup/pgdn scroll   f pause/follow   end newest   esc back"))
}

func (m Model) footer() string {
	keys := []string{"space mark", "d deploy", "r reboot", "b rollback", "G gc", "x exec", "v vulns", "+ add", "K keys", "S secrets", "l logs", "s ssh", "? help", "q quit"}
	return styleFaint.Render(strings.Join(keys, "   "))
}

func helpView() []string {
	rows := [][2]string{
		{"↑/↓ j/k", "move (or pick a failed unit after tab)"},
		{"space", "mark a host; actions use marked hosts in the order marked, else the cursor host"},
		{"a / A", "mark all / clear marks"},
		{"d", "deploy; choose the reboot mode in the dialog (none, per policy, force)"},
		{"r", "reboot hosts that need it, per rebootPolicy; f in the dialog forces"},
		{"b", "roll the cursor host back to its previous generation"},
		{"tab", "move between the host table and the host's failed units"},
		{"l", "follow the logs of the picked failed unit, or of the whole host"},
		{"s", "open a shell on the host (ssh); exit it to come back"},
		{"G", "collect garbage: generations older than 14d and unreachable store paths"},
		{"x", "run a shell command on the hosts; tab in the prompt runs it as root"},
		{"o", "show the last exec's output again"},
		{"v", "scan the hosts for known CVEs with vulnix (cached per system); r rescans"},
		{"+", "add a host: module, sops entries, secrets dir, host key (careen add)"},
		{"K", "sops keys: recorded vs live, files not re-keyed; register and re-key"},
		{"S", "secrets by host (names only): edit, add, change, remove, re-key"},
		{"g", "refresh now"},
		{"q", "quit"},
	}
	lines := []string{styleSection.Render("KEYS"), ""}
	for _, r := range rows {
		lines = append(lines, "  "+styleKey.Render(r[0]+strings.Repeat(" ", max(10-ansi.StringWidth(r[0]), 1)))+r[1])
	}
	return append(lines, "", styleFaint.Render("Every action is also a CLI command: careen deploy/reboot/rollback/logs --only HOST."), styleFaint.Render("any key to close"))
}
