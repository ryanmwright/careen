// Package logs reads a host's systemd journal.
package logs

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ryanmwright/careen/internal/sshx"
)

//go:embed logs.sh
var script string

// Entry is one journal entry.
type Entry struct {
	Time     time.Time `json:"time"`
	Unit     string    `json:"unit,omitempty"`
	Priority int       `json:"priority"` // syslog: 0 emerg … 3 err, 4 warning … 7 debug
	Message  string    `json:"message"`
}

// ParseLine parses one line of `journalctl -o json`.
func ParseLine(line []byte) (Entry, error) {
	var raw struct {
		Realtime   string          `json:"__REALTIME_TIMESTAMP"`
		Unit       string          `json:"_SYSTEMD_UNIT"`
		UserUnit   string          `json:"_SYSTEMD_USER_UNIT"`
		Identifier string          `json:"SYSLOG_IDENTIFIER"`
		Priority   string          `json:"PRIORITY"`
		Message    json.RawMessage `json:"MESSAGE"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return Entry{}, err
	}
	e := Entry{Unit: firstOf(raw.Unit, raw.UserUnit, raw.Identifier), Priority: 6}
	if us, err := strconv.ParseInt(raw.Realtime, 10, 64); err == nil {
		e.Time = time.UnixMicro(us).UTC()
	}
	if p, err := strconv.Atoi(raw.Priority); err == nil {
		e.Priority = p
	}
	e.Message = message(raw.Message)
	return e, nil
}

// message decodes MESSAGE, which journalctl prints as a string, or as an
// array of bytes when it isn't valid UTF-8.
func message(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []byte
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		for _, i := range ints {
			b = append(b, byte(i))
		}
		return string(b)
	}
	return ""
}

// Read returns the last n entries, of unit if set.
func Read(ctx context.Context, h sshx.Host, unit string, n int) ([]Entry, error) {
	out := []Entry{}
	err := sshx.Stream(ctx, h, script, func(line []byte) {
		if e, err := ParseLine(line); err == nil {
			out = append(out, e)
		}
	}, strconv.Itoa(n), unit, "0")
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	return out, nil
}

// Follow calls fn with the last n entries and then each new one until ctx
// is done.
func Follow(ctx context.Context, h sshx.Host, unit string, n int, fn func(Entry)) error {
	err := sshx.Stream(ctx, h, script, func(line []byte) {
		if e, err := ParseLine(line); err == nil {
			fn(e)
		}
	}, strconv.Itoa(n), unit, "1")
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	return nil
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
