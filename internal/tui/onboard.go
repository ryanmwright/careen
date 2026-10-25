package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/repo"
)

// onboardState is the add-host wizard and the keys and secrets views.
type onboardState struct {
	keys        *actions.KeysReport
	keysErr     error
	keysLoading bool
	keyIdx      int
	master      string // "" while checking, "ok", or why not

	secrets    *actions.SecretsReport
	secErr     error
	secLoading bool
	secIdx     int
	prompt     secretPrompt

	wiz wizard
}

// secretRow is one line of the secrets tree: a host, a file or a name.
type secretRow struct {
	host, file, name string
	missing          []string
	// label heads a row with no host: "shared" or "group NAME (hosts)".
	label string
}

// secretPrompt asks for a new secret's name and value, or a new value.
type secretPrompt struct {
	active     bool
	asking     string // "name" or "value"
	host, file string
	name       string
	input      textinput.Model
	update     bool // replacing an existing secret's value
	saving     bool
}

const (
	wizForm = iota
	wizPreview
	wizRunning
	wizFinished
)

var wizLabels = []string{"name", "address", "ip", "gateway", "interface", "vars"}

type wizard struct {
	stage       int
	fields      []textinput.Model
	focus       int
	keyAddr     string // the address keyCheck is about
	keyCheck    string
	keyChecking bool
	preview     *actions.AddPreview
	status      *actions.AddStatus
	err         error
	askingAs    bool
	as          textinput.Model
}

type (
	keysMsg struct {
		rep actions.KeysReport
		err error
	}
	masterMsg  struct{ err error }
	secretsMsg struct {
		rep actions.SecretsReport
		err error
	}
	secretSavedMsg struct {
		created bool
		err     error
	}
	editDoneMsg struct{ err error }
	wizKeyMsg   struct {
		addr, key string
		err       error
	}
	wizPreviewMsg struct {
		p   actions.AddPreview
		st  actions.AddStatus
		err error
	}
	worktreePlanMsg struct {
		plan *actions.DeployPlan
		err  error
	}
)

func newWizard() wizard {
	w := wizard{}
	placeholders := []string{"web1", "where careen reaches it, e.g. 10.0.0.20", "static IP for the template (optional)", "gateway (optional)", "interface (optional, ens18)", "extra template values: key=value, comma-separated"}
	for i := range wizLabels {
		in := newInput()
		in.Prompt = ""
		in.Placeholder = placeholders[i]
		w.fields = append(w.fields, in)
	}
	w.as = newInput()
	w.as.Prompt = "ssh user: "
	w.as.Placeholder = "empty for the inventory's user"
	return w
}

func (w wizard) vars() repo.HostVars {
	v := repo.HostVars{
		Name: strings.TrimSpace(w.fields[0].Value()), Address: strings.TrimSpace(w.fields[1].Value()),
		IP: strings.TrimSpace(w.fields[2].Value()), Gateway: strings.TrimSpace(w.fields[3].Value()),
		Interface: strings.TrimSpace(w.fields[4].Value()), Vars: map[string]string{},
	}
	for _, kv := range strings.Split(w.fields[5].Value(), ",") {
		if k, val, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			v.Vars[k] = val
		}
	}
	return v
}

var wizNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// problems are what stops the form from going on, per field.
func (m Model) wizProblems() map[int]string {
	v := m.onboard.wiz.vars()
	p := map[int]string{}
	switch {
	case v.Name == "":
		p[0] = "required"
	case !wizNameRE.MatchString(v.Name):
		p[0] = "lower-case letters, digits and -, starting with a letter"
	}
	if v.Address == "" {
		p[1] = "required"
	}
	if v.IP != "" && v.Gateway == "" {
		p[3] = "needed with a static IP"
	}
	return p
}

// updateOnboard handles this file's messages.
func (m Model) updateOnboard(msg tea.Msg) (Model, tea.Cmd, bool) {
	o := &m.onboard
	switch msg := msg.(type) {
	case keysMsg:
		o.keysLoading, o.keysErr = false, msg.err
		if msg.err == nil {
			o.keys = &msg.rep
			o.keyIdx = min(o.keyIdx, max(len(msg.rep.Hosts)-1, 0))
		}
		return m, nil, true
	case masterMsg:
		o.master = "ok"
		if msg.err != nil {
			o.master = msg.err.Error()
		}
		return m, nil, true
	case secretsMsg:
		o.secLoading, o.secErr = false, msg.err
		if msg.err == nil {
			o.secrets = &msg.rep
			o.secIdx = min(o.secIdx, max(len(m.secretRows())-1, 0))
		}
		return m, nil, true
	case secretSavedMsg:
		o.prompt = secretPrompt{}
		if msg.err != nil {
			m.setFlash(msg.err.Error(), true)
		} else {
			m.setFlash("saved; review and commit the change", false)
		}
		return m, m.loadSecrets(), true
	case editDoneMsg:
		if msg.err != nil {
			m.setFlash("sops: "+msg.err.Error(), true)
		}
		return m, m.loadSecrets(), true
	case wizKeyMsg:
		w := &o.wiz
		if msg.addr == w.keyAddr {
			w.keyChecking = false
			if msg.err != nil {
				w.keyCheck = "not answering on SSH (fine if the VM isn't up yet)"
			} else {
				w.keyCheck = "host key " + msg.key
			}
		}
		return m, nil, true
	case wizPreviewMsg:
		w := &o.wiz
		w.err = msg.err
		if msg.err == nil {
			w.preview, w.status, w.stage = &msg.p, &msg.st, wizPreview
		}
		return m, nil, true
	case worktreePlanMsg:
		if msg.err != nil {
			m.setFlash(msg.err.Error(), true)
			return m, nil, true
		}
		m.mode = modeConfirm
		m.confirm = confirmation{kind: actDeploy, only: []string{o.wiz.vars().Name}, deploy: msg.plan, returnMode: modeAdd}
		return m, nil, true
	}
	if o.prompt.active {
		var cmd tea.Cmd
		o.prompt.input, cmd = o.prompt.input.Update(msg)
		return m, cmd, true
	}
	if m.mode == modeAdd && o.wiz.stage == wizForm {
		var cmd tea.Cmd
		o.wiz.fields[o.wiz.focus], cmd = o.wiz.fields[o.wiz.focus].Update(msg)
		return m, cmd, true
	}
	return m, nil, false
}

func (m Model) loadKeys() tea.Cmd {
	be, ctx := m.be, m.ctx
	return tea.Batch(
		func() tea.Msg { rep, err := be.Keys(ctx); return keysMsg{rep, err} },
		func() tea.Msg { return masterMsg{be.MasterKey(ctx)} },
	)
}

func (m Model) loadSecrets() tea.Cmd {
	be, ctx := m.be, m.ctx
	return tea.Batch(
		func() tea.Msg { rep, err := be.Secrets(ctx); return secretsMsg{rep, err} },
		func() tea.Msg { return masterMsg{be.MasterKey(ctx)} },
	)
}

func (m Model) openKeys() (tea.Model, tea.Cmd) {
	m.mode = modeKeys
	m.onboard.keysLoading = true
	return m, m.loadKeys()
}

func (m Model) openSecrets() (tea.Model, tea.Cmd) {
	m.mode = modeSecrets
	m.onboard.secLoading = true
	return m, m.loadSecrets()
}

func (m Model) openWizard() (tea.Model, tea.Cmd) {
	m.mode = modeAdd
	m.onboard.wiz = newWizard()
	return m, m.onboard.wiz.fields[0].Focus()
}

// actionFrom starts a one-step action from a view, returning to it.
func (m Model) confirmFrom(c confirmation) (tea.Model, tea.Cmd) {
	if m.running {
		m.setFlash("a "+m.runKind.String()+" is running; wait for it to finish", true)
		return m, nil
	}
	c.returnMode = m.mode
	m.mode, m.confirm = modeConfirm, c
	return m, nil
}

func (m Model) keysKey(k string) (tea.Model, tea.Cmd) {
	o := &m.onboard
	var hosts []actions.KeyState
	if o.keys != nil {
		hosts = o.keys.Hosts
	}
	switch k {
	case "esc", "q", "K":
		m.mode = modeNormal
	case "up", "k":
		o.keyIdx = max(o.keyIdx-1, 0)
	case "down", "j":
		o.keyIdx = min(o.keyIdx+1, max(len(hosts)-1, 0))
	case "r", "g":
		o.keysLoading = true
		return m, m.loadKeys()
	case "enter":
		if len(hosts) == 0 {
			return m, nil
		}
		h := hosts[o.keyIdx]
		if h.Anchor == "" {
			m.setFlash(h.Host+" has no key in .sops.yaml; onboard it with + (careen add)", true)
			return m, nil
		}
		return m.confirmFrom(confirmation{kind: actRegister, only: []string{h.Host}, replace: h.State == actions.KeyDrift})
	case "R":
		if len(hosts) == 0 {
			return m, nil
		}
		return m.confirmFrom(confirmation{kind: actRekey, only: []string{hosts[o.keyIdx].Host}})
	}
	return m, nil
}

// secretRows flattens the secrets report into the lines of the tree.
func (m Model) secretRows() []secretRow {
	rep := m.onboard.secrets
	if rep == nil {
		return nil
	}
	var rows []secretRow
	add := func(host, label string, files []actions.SecretFile) {
		rows = append(rows, secretRow{host: host, label: label})
		for _, f := range files {
			rows = append(rows, secretRow{host: host, file: f.Path, missing: f.Missing})
			for _, n := range f.Names {
				rows = append(rows, secretRow{host: host, file: f.Path, name: n})
			}
		}
	}
	for _, h := range rep.Hosts {
		add(h.Host, "", h.Files)
	}
	for _, g := range rep.Groups {
		add("", "group "+g.Group+" ("+cmpOrStr(strings.Join(g.Hosts, ", "), "no hosts")+")", g.Files)
	}
	if len(rep.Shared) > 0 {
		add("", "shared", rep.Shared)
	}
	return rows
}

func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (m Model) secretsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	o := &m.onboard
	k := msg.String()
	if o.prompt.active {
		return m.secretPromptKey(msg)
	}
	rows := m.secretRows()
	var r secretRow
	if len(rows) > 0 {
		r = rows[o.secIdx]
	}
	switch k {
	case "esc", "q", "S":
		m.mode = modeNormal
	case "up", "k":
		o.secIdx = max(o.secIdx-1, 0)
	case "down", "j":
		o.secIdx = min(o.secIdx+1, max(len(rows)-1, 0))
	case "r", "g":
		o.secLoading = true
		return m, m.loadSecrets()
	case "e":
		if r.host == "" {
			m.setFlash("shared and group files: use careen secrets edit --group, or sops", true)
			return m, nil
		}
		c, err := m.be.EditSecrets(m.ctx, r.host, baseName(r.file))
		if err != nil {
			m.setFlash(err.Error(), true)
			return m, nil
		}
		return m, tea.ExecProcess(c, func(err error) tea.Msg { return editDoneMsg{err} })
	case "n":
		if r.host == "" {
			return m, nil
		}
		return m.startPrompt(secretPrompt{asking: "name", host: r.host, file: baseName(r.file)})
	case "u":
		if r.host == "" {
			return m, nil
		}
		if r.name == "" {
			m.setFlash("pick a secret's name first", true)
			return m, nil
		}
		return m.startPrompt(secretPrompt{asking: "value", host: r.host, file: baseName(r.file), name: r.name, update: true})
	case "x":
		if r.name == "" || r.host == "" {
			return m, nil
		}
		return m.confirmFrom(confirmation{kind: actUnset, only: []string{r.host}, file: baseName(r.file), secret: r.name})
	case "R":
		if r.host == "" {
			return m, nil
		}
		return m.confirmFrom(confirmation{kind: actRekey, only: []string{r.host}})
	}
	return m, nil
}

func (m Model) startPrompt(p secretPrompt) (tea.Model, tea.Cmd) {
	p.active = true
	p.input = newInput()
	if p.asking == "name" {
		p.input.Prompt = "name: "
		p.input.Placeholder = "e.g. api_token"
	} else {
		p.input.Prompt = "value: "
		p.input.EchoMode = textinput.EchoPassword
	}
	m.onboard.prompt = p
	return m, m.onboard.prompt.input.Focus()
}

func (m Model) secretPromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := &m.onboard.prompt
	switch msg.String() {
	case "esc":
		*p = secretPrompt{}
		return m, nil
	case "enter":
		if p.saving {
			return m, nil
		}
		v := p.input.Value()
		if p.asking == "name" {
			v = strings.TrimSpace(v)
			if v == "" {
				return m, nil
			}
			name, host, file := v, p.host, p.file
			nm, cmd := m.startPrompt(secretPrompt{asking: "value", host: host, file: file, name: name})
			return nm, cmd
		}
		p.saving = true
		be, ctx := m.be, m.ctx
		host, file, name := p.host, p.file, p.name
		return m, func() tea.Msg {
			created, err := be.SetSecret(ctx, host, file, name, v)
			return secretSavedMsg{created, err}
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return m, cmd
}

func (m Model) wizardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := &m.onboard.wiz
	k := msg.String()
	if w.askingAs {
		switch k {
		case "esc":
			w.askingAs = false
			return m, nil
		case "enter":
			w.askingAs = false
			be, ctx, host, as := m.be, m.ctx, w.vars().Name, strings.TrimSpace(w.as.Value())
			return m, func() tea.Msg {
				p, err := be.PlanDeployWorktree(ctx, host, as)
				return worktreePlanMsg{p, err}
			}
		}
		var cmd tea.Cmd
		w.as, cmd = w.as.Update(msg)
		return m, cmd
	}
	switch w.stage {
	case wizForm:
		switch k {
		case "esc":
			m.mode = modeNormal
			return m, nil
		case "tab", "down", "shift+tab", "up":
			cmd := m.checkWizKey()
			w.fields[w.focus].Blur()
			if k == "tab" || k == "down" {
				w.focus = (w.focus + 1) % len(w.fields)
			} else {
				w.focus = (w.focus + len(w.fields) - 1) % len(w.fields)
			}
			return m, tea.Batch(cmd, w.fields[w.focus].Focus())
		case "enter":
			if len(m.wizProblems()) > 0 {
				w.err = fmt.Errorf("fix the marked fields first")
				return m, nil
			}
			w.err = nil
			be, ctx, v := m.be, m.ctx, w.vars()
			return m, tea.Batch(m.checkWizKey(), func() tea.Msg {
				p, err := be.PreviewAdd(ctx, v)
				if err != nil {
					return wizPreviewMsg{err: err}
				}
				st, err := be.AddStatus(ctx, v)
				return wizPreviewMsg{p, st, err}
			})
		}
		var cmd tea.Cmd
		w.fields[w.focus], cmd = w.fields[w.focus].Update(msg)
		return m, cmd
	case wizPreview:
		switch k {
		case "esc":
			w.stage = wizForm
			return m, w.fields[w.focus].Focus()
		case "y":
			if m.running {
				m.setFlash("a "+m.runKind.String()+" is running; wait for it to finish", true)
				return m, nil
			}
			w.stage = wizRunning
			m.confirm = confirmation{kind: actAdd, only: []string{w.vars().Name}, vars: w.vars(), returnMode: modeAdd}
			return m.start()
		}
	case wizFinished:
		switch k {
		case "esc", "q":
			m.mode = modeNormal
		case "d":
			w.askingAs = true
			return m, w.as.Focus()
		case "g":
			return m, tea.ExecProcess(m.be.GitDiff(), func(err error) tea.Msg { return editDoneMsg{err} })
		case "r":
			// Run again, e.g. once the host is up.
			w.stage = wizRunning
			m.confirm = confirmation{kind: actAdd, only: []string{w.vars().Name}, vars: w.vars(), returnMode: modeAdd}
			return m.start()
		}
	}
	return m, nil
}

// checkWizKey fetches the address's host key in the background when it
// changed.
func (m *Model) checkWizKey() tea.Cmd {
	w := &m.onboard.wiz
	addr := strings.TrimSpace(w.fields[1].Value())
	if addr == "" || addr == w.keyAddr {
		return nil
	}
	w.keyAddr, w.keyChecking, w.keyCheck = addr, true, ""
	be, ctx := m.be, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		key, err := be.ScanKey(cctx, addr)
		return wizKeyMsg{addr, key, err}
	}
}

// --- views ---

func (m Model) masterLine() string {
	switch m.onboard.master {
	case "":
		return styleFaint.Render("master key: checking…")
	case "ok":
		return styleOK.Render("master key: available")
	}
	return styleWarn.Render("master key: " + m.onboard.master)
}

func (m Model) keysView() []string {
	o := m.onboard
	title := styleSection.Render("KEYS") + "  " + m.masterLine()
	var body []string
	switch {
	case o.keysLoading && o.keys == nil:
		body = append(body, "checking each host's key…")
	case o.keysErr != nil:
		body = append(body, styleErr.Render(o.keysErr.Error()))
	case o.keys != nil:
		body = append(body, styleSection.Render(fmt.Sprintf("  %-10s %-12s %-22s %-22s %s", "HOST", "STATE", "RECORDED", "LIVE", "NOT RE-KEYED")))
		for i, h := range o.keys.Hosts {
			line := fmt.Sprintf("  %-10s %-12s %-22s %-22s %s", h.Host, h.State, shortAge(h.Recorded), shortAge(h.Live), strings.Join(h.NeedsRekey, ", "))
			switch h.State {
			case actions.KeyOK:
				line = styleOK.Render(line)
			case actions.KeyDrift, actions.KeyNoAnchor:
				line = styleErr.Render(line)
			case actions.KeyPlaceholder, actions.KeyNeedsRekey:
				line = styleWarn.Render(line)
			}
			if i == o.keyIdx {
				line = styleSelected.Render(ansi.Strip(line))
			}
			body = append(body, line)
		}
		if h := o.keys.Hosts; len(h) > 0 {
			body = append(body, "", keyHint(h[o.keyIdx]))
		}
	}
	body = append(body, m.activityTail(6)...)
	return m.window(body, title, "↑/↓ host   enter register key   R re-key files   r refresh   esc back")
}

func keyHint(h actions.KeyState) string {
	switch h.State {
	case actions.KeyPlaceholder:
		return "enter records " + h.Host + "'s live key and re-keys its files"
	case actions.KeyDrift:
		return styleErr.Render(h.Host + " was reinstalled: enter replaces the recorded key (needs the master key)")
	case actions.KeyNeedsRekey:
		return "R re-encrypts " + strings.Join(h.NeedsRekey, ", ") + " to " + h.Host + "'s key"
	case actions.KeyNoAnchor:
		return h.Host + " isn't in .sops.yaml: onboard it with + (careen add)"
	case actions.KeyUnreachable:
		return h.Host + " didn't answer; its recorded key can't be checked"
	}
	return h.Host + "'s key is recorded, live and every file is encrypted to it"
}

func shortAge(k string) string {
	if agekey.IsRecipient(k) {
		return k[:10] + "…" + k[len(k)-6:]
	}
	if k == "" {
		return "-"
	}
	return k
}

func (m Model) secretsView() []string {
	o := m.onboard
	title := styleSection.Render("SECRETS") + styleFaint.Render("  names only; nothing is decrypted") + "  " + m.masterLine()
	var body []string
	switch {
	case o.secLoading && o.secrets == nil:
		body = append(body, "reading…")
	case o.secErr != nil:
		body = append(body, styleErr.Render(o.secErr.Error()))
	default:
		for i, r := range m.secretRows() {
			var line string
			switch {
			case r.file == "":
				line = styleTitle.Render(cmpOrStr(r.host, r.label))
			case r.name == "":
				line = "  " + r.file
				if len(r.missing) > 0 {
					line += styleWarn.Render("  not encrypted to " + strings.Join(r.missing, ", ") + " (R re-keys)")
				}
			default:
				line = "      " + r.name
			}
			if i == o.secIdx {
				line = styleSelected.Render(ansi.Strip(line))
			}
			body = append(body, line)
		}
	}
	if p := o.prompt; p.active {
		what := "new secret for " + p.host
		if p.update {
			what = "new value for " + p.name
		}
		body = append(body, "", styleSection.Render(strings.ToUpper(what)), p.input.View())
		if p.saving {
			body = append(body, styleFaint.Render("encrypting…"))
		}
	}
	body = append(body, m.activityTail(4)...)
	return m.window(body, title, "↑/↓ move   e edit file   n new   u new value   x remove   R re-key   r refresh   esc back")
}

func (m Model) wizardView() []string {
	w := m.onboard.wiz
	title := styleSection.Render("ADD HOST") + styleFaint.Render("  changes are left uncommitted in the checkout")
	var body []string
	switch w.stage {
	case wizForm:
		probs := m.wizProblems()
		for i, f := range w.fields {
			label := fmt.Sprintf("%-10s", wizLabels[i])
			if i == w.focus {
				label = styleKey.Render(label)
			}
			line := "  " + label + " " + f.View()
			// A field's problem shows once it has text or the cursor has moved past it.
			if p, ok := probs[i]; ok && (strings.TrimSpace(f.Value()) != "" || i < w.focus) {
				line += "  " + styleErr.Render(p)
			}
			if i == 1 {
				switch {
				case w.keyChecking:
					line += styleFaint.Render("  checking…")
				case w.keyCheck != "":
					line += "  " + styleFaint.Render(w.keyCheck)
				}
			}
			body = append(body, line)
		}
		if w.err != nil {
			body = append(body, "", styleErr.Render(w.err.Error()))
		}
		body = append(body, "", styleFaint.Render("The VM can be created before or after this: run it again once the host is up to register its key."))
		return m.window(body, title, "tab/↑/↓ field   enter preview   esc cancel")
	case wizPreview:
		v := w.vars()
		body = append(body, styleSection.Render("STEPS"))
		for _, s := range w.status.Steps {
			body = append(body, "  "+stepMark(s.State)+" "+s.Title+styleFaint.Render("  "+s.Detail))
		}
		body = append(body, "", styleSection.Render(".SOPS.YAML CHANGES"))
		for _, l := range w.preview.SopsLines {
			st := styleOK
			if strings.HasPrefix(l, "-") {
				st = styleErr
			}
			body = append(body, st.Render("  "+l))
		}
		body = append(body, "", styleSection.Render("HOST MODULE ("+v.Name+")"))
		for _, l := range strings.Split(strings.TrimRight(w.preview.Module, "\n"), "\n") {
			body = append(body, "  "+l)
		}
		return m.window(body, title, "y write it   ↑/↓ scroll   esc back to the form")
	default:
		if w.status != nil {
			body = append(body, styleSection.Render("STEPS"))
			for _, s := range w.status.Steps {
				body = append(body, "  "+stepMark(s.State)+" "+s.Title+styleFaint.Render("  "+s.Detail))
			}
		}
		if w.err != nil {
			body = append(body, "", styleErr.Render(w.err.Error()))
		}
		body = append(body, m.activityTail(8)...)
		if w.stage == wizFinished {
			body = append(body, "", "Next: review the diff (g), commit and push, then deploy; or deploy from the working tree now (d).")
			if w.askingAs {
				body = append(body, "", w.as.View())
			}
			return m.window(body, title, "d deploy from the working tree   g git diff   r run again   esc close")
		}
		return m.window(body, title, "running…")
	}
}

func stepMark(state string) string {
	switch state {
	case actions.StepDone:
		return styleOK.Render("✓")
	case actions.StepWaiting:
		return styleWarn.Render("…")
	case actions.StepFailed:
		return styleErr.Render("✗")
	case actions.StepUnknown:
		return styleFaint.Render("?")
	}
	return "·"
}

// activityTail is the last n activity lines, for views that show actions
// they started.
func (m Model) activityTail(n int) []string {
	if len(m.activity) == 0 {
		return nil
	}
	act := m.activity[max(len(m.activity)-n, 0):]
	out := []string{"", styleSection.Render("ACTIVITY")}
	out = append(out, act...)
	if m.flash != "" {
		out = append(out, map[bool]string{true: styleErr.Render(m.flash), false: styleOK.Render(m.flash)}[m.flashErr])
	}
	return out
}

func cmpOrStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
