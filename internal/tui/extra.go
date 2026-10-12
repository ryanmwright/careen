package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/format"
	"github.com/ryanmwright/careen/internal/vulns"
)

// extraState is the gc, exec and vulns parts of the model.
type extraState struct {
	input    textinput.Model
	sudo     bool // the exec prompt's sudo toggle
	execDone *actions.ExecDone
	execIdx  int // host shown in the exec output view
	scroll   int // lines scrolled in the exec and vulns views

	vulnCache map[string]*vulns.Report // last scans matching the running systems
	vulns     vulnsView
}

type vulnsView struct {
	only     []string
	scanning bool
	progress []string
	rep      *actions.VulnsReport
	err      error
	ch       chan tea.Msg
	cancel   context.CancelFunc
}

// newInput is a text input with a steady cursor: a blinking one keeps
// scheduling redraws for nothing.
func newInput() textinput.Model {
	in := textinput.New()
	st := in.Styles()
	st.Cursor.Blink = false
	in.SetStyles(st)
	in.SetWidth(60) // without a width the placeholder shows one character
	return in
}

func newExtraState() extraState {
	in := newInput()
	in.Prompt = "$ "
	in.Placeholder = "a shell command, e.g. df -h /nix"
	return extraState{input: in, vulnCache: map[string]*vulns.Report{}}
}

type (
	gcPlanMsg struct {
		plan *actions.GCPlan
		err  error
	}
	vulnsProgressMsg string
	vulnsDoneMsg     struct {
		rep actions.VulnsReport
		err error
	}
)

// updateExtra handles the messages of this file; ok reports whether msg
// was one of them.
func (m Model) updateExtra(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case gcPlanMsg:
		if m.mode == modeConfirm && m.confirm.kind == actGC {
			m.confirm.planning = false
			m.confirm.gcPlan, m.confirm.err = msg.plan, msg.err
		}
		return m, nil, true
	case vulnsProgressMsg:
		m.extra.vulns.progress = append(m.extra.vulns.progress, string(msg))
		return m, wait(m.extra.vulns.ch), true
	case vulnsDoneMsg:
		v := &m.extra.vulns
		v.scanning, v.err = false, msg.err
		if msg.err == nil {
			v.rep = &msg.rep
		}
		m.loadVulnCache()
		return m, nil, true
	}
	if m.mode == modeInput {
		var cmd tea.Cmd
		m.extra.input, cmd = m.extra.input.Update(msg)
		return m, cmd, true
	}
	return m, nil, false
}

func (m Model) planGC() tea.Cmd {
	be, ctx, c := m.be, m.ctx, m.confirm
	return func() tea.Msg {
		p, err := be.PlanGC(ctx, c.inv, c.only)
		return gcPlanMsg{p, err}
	}
}

// loadVulnCache refreshes the VULNS column from cached scans that match
// each host's running system.
func (m *Model) loadVulnCache() {
	cache := map[string]*vulns.Report{}
	for _, r := range m.rows() {
		if r.State != nil && r.System != "" {
			if c := m.be.CachedVulns(r.Host, r.System); c != nil {
				cache[r.Host] = c
			}
		}
	}
	m.extra.vulnCache = cache
}

// openInput asks for the command exec will run.
func (m Model) openInput() (tea.Model, tea.Cmd) {
	if len(m.targets()) == 0 {
		return m, nil
	}
	m.mode = modeInput
	m.extra.input.SetValue("")
	return m, m.extra.input.Focus()
}

func (m Model) inputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.extra.input.Blur()
		m.mode = modeNormal
		return m, nil
	case "tab":
		m.extra.sudo = !m.extra.sudo
		return m, nil
	case "enter":
		command := strings.TrimSpace(m.extra.input.Value())
		if command == "" {
			return m, nil
		}
		m.extra.input.Blur()
		nm, cmd := m.openConfirm(actExec)
		mm := nm.(Model)
		mm.confirm.command, mm.confirm.sudo = command, m.extra.sudo
		return mm, cmd
	}
	var cmd tea.Cmd
	m.extra.input, cmd = m.extra.input.Update(msg)
	return m, cmd
}

func (m Model) execOutKey(k string) (tea.Model, tea.Cmd) {
	d := m.extra.execDone
	switch k {
	case "esc", "q", "o":
		m.mode = modeNormal
	case "tab", "right", "l":
		m.extra.execIdx, m.extra.scroll = (m.extra.execIdx+1)%len(d.Hosts), 0
	case "shift+tab", "left", "h":
		m.extra.execIdx, m.extra.scroll = (m.extra.execIdx+len(d.Hosts)-1)%len(d.Hosts), 0
	case "down", "j":
		m.extra.scroll++
	case "up", "k":
		m.extra.scroll = max(m.extra.scroll-1, 0)
	}
	return m, nil
}

// openVulns scans the marked hosts, or the cursor host, in the background.
func (m Model) openVulns(refresh bool) (tea.Model, tea.Cmd) {
	only := m.extra.vulns.only
	if !refresh || len(only) == 0 {
		only = m.targets()
	}
	if len(only) == 0 || m.inv == nil {
		return m, nil
	}
	if m.extra.vulns.cancel != nil {
		m.extra.vulns.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	ch := make(chan tea.Msg, 64)
	m.mode, m.extra.scroll = modeVulns, 0
	m.extra.vulns = vulnsView{only: only, scanning: true, ch: ch, cancel: cancel}
	send := func(msg tea.Msg) {
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
	be, inv := m.be, m.inv
	go func() {
		defer close(ch)
		rep, err := be.Vulns(ctx, inv, only, refresh, func(host, msg string) { send(vulnsProgressMsg(host + ": " + msg)) })
		send(vulnsDoneMsg{rep, err})
	}()
	return m, wait(ch)
}

func (m Model) vulnsKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "esc", "q", "v":
		m.mode = modeNormal
	case "r":
		if !m.extra.vulns.scanning {
			return m.openVulns(true)
		}
	case "down", "j":
		m.extra.scroll++
	case "up", "k":
		m.extra.scroll = max(m.extra.scroll-1, 0)
	case "pgdown":
		m.extra.scroll += max(m.height-4, 1)
	case "pgup":
		m.extra.scroll = max(m.extra.scroll-max(m.height-4, 1), 0)
	}
	return m, nil
}

// vulnsCell is the VULNS column for a host.
func (m Model) vulnsCell(host string) string {
	if c := m.extra.vulnCache[host]; c != nil {
		return c.Summary.Short()
	}
	return "-"
}

func (m Model) inputView() []string {
	sudo := choice("tab", "as root", m.extra.sudo)
	return []string{"", styleSection.Render("EXEC on " + strings.Join(m.targets(), ", ")),
		m.extra.input.View(), "", sudo + "   " + styleKey.Render("enter") + " review   " + styleKey.Render("esc") + " cancel"}
}

func execPlanLines(p *actions.ExecPlan) []string {
	as := ""
	if p.Sudo {
		as = styleWarn.Render(" as root")
	}
	return []string{styleSection.Render("EXEC") + as + "  on " + strings.Join(p.Hosts, ", "), "  $ " + p.Command}
}

func gcPlanLines(p *actions.GCPlan) []string {
	lines := []string{styleSection.Render("GC") + "  delete generations older than " + p.OlderThan + " and unreachable store paths"}
	for _, h := range p.Hosts {
		if h.Error != nil {
			lines = append(lines, styleFaint.Render(fmt.Sprintf("  %-10s skip: %s", h.Host, h.Error.Message)))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %-10s %3d generations   store %s", h.Host, h.Generations, format.Store(h.StoreUsed, h.StoreSize)))
	}
	return lines
}

// window returns the lines that fit below a title, after scrolling.
func (m Model) window(body []string, title string, help string) []string {
	n := max(m.height-3, 1)
	scroll := min(m.extra.scroll, max(len(body)-n, 0))
	end := min(scroll+n, len(body))
	lines := append([]string{title}, body[scroll:end]...)
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	return append(lines, styleFaint.Render(help))
}

func (m Model) execOutView() []string {
	d := m.extra.execDone
	r := d.Hosts[m.extra.execIdx]
	var tabs []string
	for i, h := range d.Hosts {
		t := h.Host
		switch {
		case !h.OK():
			t = styleErr.Render(t)
		default:
			t = styleOK.Render(t)
		}
		if i == m.extra.execIdx {
			t = styleSelected.Render(h.Host)
		}
		tabs = append(tabs, t)
	}
	status := fmt.Sprintf("exit %d, %s", r.ExitCode, time.Duration(r.DurationMs)*time.Millisecond)
	if r.Error != nil {
		status = r.Error.Message
	}
	title := styleSection.Render("EXEC OUTPUT") + "  " + strings.Join(tabs, "  ") + styleFaint.Render("  "+status)
	var body []string
	body = append(body, strings.Split(strings.TrimRight(r.Stdout, "\n"), "\n")...)
	if r.Stderr != "" {
		for _, l := range strings.Split(strings.TrimRight(r.Stderr, "\n"), "\n") {
			body = append(body, styleWarn.Render(l))
		}
	}
	if r.Truncated {
		body = append(body, styleFaint.Render("[output truncated]"))
	}
	return m.window(body, title, "tab/←→ host   ↑/↓ scroll   esc back")
}

func (m Model) vulnsView() []string {
	v := m.extra.vulns
	title := styleSection.Render("VULNS") + "  " + strings.Join(v.only, ", ")
	var body []string
	switch {
	case v.scanning:
		title += styleWarn.Render("  scanning…")
		body = append(body, v.progress...)
		body = append(body, styleFaint.Render("a first scan downloads NVD data and can take minutes; later ones take seconds"))
	case v.err != nil:
		body = append(body, styleErr.Render(v.err.Error()))
	case v.rep != nil:
		if v.rep.Whitelist == "" {
			body = append(body, styleFaint.Render("no "+actions.WhitelistFile+" in the flake"))
		}
		for _, r := range v.rep.Hosts {
			body = append(body, "")
			if r.Error != nil {
				body = append(body, styleErr.Render(r.Host+": "+r.Error.Message))
				continue
			}
			body = append(body, styleTitle.Render(r.Host)+"  "+severityStyle(r.Summary)+styleFaint.Render(fmt.Sprintf("  %d packages, scanned %s", len(r.Findings), r.ScannedAt.Local().Format("Jan 02 15:04"))))
			for _, f := range r.Findings {
				line := fmt.Sprintf("  %5.1f  %s %s  ", f.MaxScore(), f.Package, f.Version)
				var ids []string
				for _, c := range f.CVEs {
					ids = append(ids, c.ID)
				}
				line += strings.Join(ids, " ")
				switch {
				case f.MaxScore() >= 9:
					line = styleErr.Render(line)
				case f.MaxScore() >= 7:
					line = styleWarn.Render(line)
				}
				body = append(body, line)
			}
		}
	}
	return m.window(body, title, "↑/↓ pgup/pgdn scroll   r rescan   esc back")
}

func severityStyle(s vulns.Summary) string {
	switch {
	case s.Critical > 0:
		return styleErr.Render(s.Short())
	case s.High > 0:
		return styleWarn.Render(s.Short())
	}
	return styleOK.Render(s.Short())
}
