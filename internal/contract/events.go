package contract

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Event types an action emits.
const (
	EventStarted  = "started"
	EventProgress = "progress"
	EventDecision = "decision"
	EventDone     = "done"
	EventError    = "error"
)

// Event is one line of an action's --json output.
type Event struct {
	Schema  string    `json:"schema"`
	Time    time.Time `json:"time"`
	Event   string    `json:"event"`
	Host    string    `json:"host,omitempty"`
	Step    string    `json:"step,omitempty"`
	Message string    `json:"message,omitempty"`
	// Options are the choices of a decision event.
	Options []string `json:"options,omitempty"`
	Error   *Error   `json:"error,omitempty"`
	Data    any      `json:"data,omitempty"`
}

// Emitter writes an action's events: NDJSON under --json, otherwise one
// human line per progress step. It is safe for concurrent use.
type Emitter struct {
	w      io.Writer
	json   bool
	schema string
	sink   func(Event)
	mu     sync.Mutex
}

func NewEmitter(w io.Writer, asJSON bool, schema string) *Emitter {
	return &Emitter{w: w, json: asJSON, schema: schema}
}

// NewSinkEmitter passes each event to sink instead of writing it, for
// in-process consumers such as the TUI. sink is called from whichever
// goroutine emits.
func NewSinkEmitter(schema string, sink func(Event)) *Emitter {
	return &Emitter{schema: schema, sink: sink}
}

// JSON reports whether events are written as JSON. A sink emitter counts
// as JSON: nothing is printed for a person and nothing may prompt.
func (e *Emitter) JSON() bool { return e.json || e.sink != nil }

func (e *Emitter) emit(ev Event) {
	ev.Schema = e.schema
	ev.Time = time.Now().UTC().Truncate(time.Millisecond)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sink != nil {
		e.sink(ev)
		return
	}
	if e.json {
		json.NewEncoder(e.w).Encode(ev)
		return
	}
	switch ev.Event {
	case EventProgress, EventError, EventDecision:
		host := ev.Host
		if host == "" {
			host = "-"
		}
		msg := ev.Message
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		if len(ev.Options) > 0 {
			msg += " (" + strings.Join(ev.Options, ", ") + ")"
		}
		fmt.Fprintf(e.w, "%s  %-10s %-9s %s\n", ev.Time.Local().Format("15:04:05"), host, ev.Step, msg)
	}
}

func (e *Emitter) Started(message string, data any) {
	e.emit(Event{Event: EventStarted, Message: message, Data: data})
}

func (e *Emitter) Progress(host, step, format string, args ...any) {
	e.emit(Event{Event: EventProgress, Host: host, Step: step, Message: fmt.Sprintf(format, args...)})
}

// Decision reports a choice the action can't make alone. The caller then
// stops with ExitPrecondition.
func (e *Emitter) Decision(host, step, message string, options ...string) {
	e.emit(Event{Event: EventDecision, Host: host, Step: step, Message: message, Options: options})
}

// Done is the final event, carrying the result. Human output prints its
// own summary instead, so Done writes nothing then.
func (e *Emitter) Done(data any) {
	e.emit(Event{Event: EventDone, Data: data})
}

func (e *Emitter) Error(host, step string, err error) {
	obj := ErrorObject(err)
	if obj.Host == "" {
		obj.Host = host
	}
	e.emit(Event{Event: EventError, Host: host, Step: step, Error: &obj})
}

// Interactive reports whether the user can answer a prompt.
func Interactive() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Confirm asks a yes/no question on the terminal. It answers false for
// anything but y or yes.
func Confirm(w io.Writer, question string) bool {
	fmt.Fprintf(w, "%s [y/N] ", question)
	var answer string
	fmt.Fscanln(os.Stdin, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// Question is something an action needs a person to decide while it runs,
// such as what to do about a step that timed out.
type Question struct {
	Host    string
	Step    string
	Message string
	Options []string
}

// Ask answers q with one of its Options. Each front end has its own: the
// CLI asks on the terminal (or emits a decision event and exits 3), the TUI
// shows a prompt.
type Ask func(ctx context.Context, q Question) (string, error)

// Choose asks q on the terminal until the answer is one of its options, by
// name or first letter. It reports false when there's no answer (EOF).
func Choose(w io.Writer, q Question) (string, bool) {
	keys := make([]string, len(q.Options))
	for i, o := range q.Options {
		keys[i] = "[" + o[:1] + "]" + o[1:]
	}
	in := bufio.NewReader(os.Stdin)
	for {
		host := q.Host
		if host != "" {
			host += ": "
		}
		fmt.Fprintf(w, "\a%s%s\n  %s? ", host, q.Message, strings.Join(keys, ", "))
		line, err := in.ReadString('\n')
		a := strings.ToLower(strings.TrimSpace(line))
		for _, o := range q.Options {
			if a != "" && (a == o || a == o[:1]) {
				return o, true
			}
		}
		if err != nil {
			return "", false
		}
	}
}
