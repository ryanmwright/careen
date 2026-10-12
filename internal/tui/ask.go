package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ryanmwright/careen/internal/contract"
)

// pendingAsk is a question a running action is waiting on, and the mode
// to go back to once it's answered.
type pendingAsk struct {
	q     contract.Question
	reply chan string
	prev  mode
}

// askVia is an action's contract.Ask: it sends the question to the model
// over the action's message channel and waits for the key pressed. No
// answer (esc) stops the action like a failure would.
func askVia(ch chan tea.Msg) contract.Ask {
	return func(ctx context.Context, q contract.Question) (string, error) {
		reply := make(chan string, 1)
		select {
		case ch <- askMsg{q, reply}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		select {
		case a := <-reply:
			if a == "" {
				return "", contract.Failed("aborted", "no answer to: %s", q.Message)
			}
			return a, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// askKey answers the pending question: an option's first letter picks it,
// esc gives no answer. Other keys do nothing, so a stray key can't answer.
func (m Model) askKey(k string) (tea.Model, tea.Cmd) {
	a := m.ask
	if a == nil {
		m.mode = modeNormal
		return m, nil
	}
	answer, ok := "", k == "esc"
	for _, o := range a.q.Options {
		if k == o[:1] {
			answer, ok = o, true
		}
	}
	if !ok {
		return m, nil
	}
	a.reply <- answer
	m.ask, m.mode = nil, a.prev
	return m, nil
}

func (m Model) askView() []string {
	a := m.ask
	if a == nil {
		return nil
	}
	host := a.q.Host
	if host != "" {
		host += ": "
	}
	keys := make([]string, len(a.q.Options))
	for i, o := range a.q.Options {
		keys[i] = styleKey.Render(o[:1]) + " " + o
	}
	return []string{"", styleWarn.Render(host + a.q.Message), strings.Join(keys, "   ") + "   " + styleKey.Render("esc") + " no answer (stops)"}
}
