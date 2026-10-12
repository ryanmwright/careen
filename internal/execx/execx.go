// Package execx runs local commands, behind a type tests can replace.
package execx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs a command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Run is the default Runner. Its errors carry the end of the command's
// stderr.
func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := Summary(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Tail is the last n non-empty lines of s, joined with "; ".
func Tail(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// Summary picks the useful part of a command's stderr. Nix prints an error
// as a chain of "… while evaluating" context lines ending in the innermost
// "error: …" line, followed by hints; that innermost error and the line
// after it say what went wrong. Without an "error:" line it's the last
// three lines.
func Summary(stderr string) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	last := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "error:") && strings.TrimSpace(strings.TrimPrefix(l, "error:")) != "" {
			last = i
		}
	}
	if last < 0 {
		return Tail(stderr, 3)
	}
	end := min(last+2, len(lines))
	msg := strings.Join(lines[last:end], "; ")
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}
