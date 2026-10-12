// Package contract implements the shared CLI and TUI contract: the JSON
// envelope, error objects, exit codes and --watch streaming. Keep it free of
// anything careen-specific.
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Exit codes.
const (
	ExitOK           = 0 // success, or nothing to do
	ExitFailed       = 1 // the command ran and failed
	ExitUsage        = 2 // bad flags or arguments
	ExitPrecondition = 3 // e.g. a target unreachable, or a plan needing confirmation
)

// Header starts every JSON object a command prints.
type Header struct {
	Schema      string    `json:"schema"`
	GeneratedAt time.Time `json:"generatedAt"`
}

func NewHeader(schema string) Header {
	return Header{Schema: schema, GeneratedAt: time.Now().UTC().Truncate(time.Second)}
}

// Error is the error object in JSON output, both for a whole command and
// for one failed host or source inside a report.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Host    string `json:"host,omitempty"`
}

// ExitError is an error that carries its exit code and JSON error object.
type ExitError struct {
	Exit int
	Err  Error
}

func (e *ExitError) Error() string { return e.Err.Message }

func Failed(code, format string, args ...any) error {
	return &ExitError{Exit: ExitFailed, Err: Error{Code: code, Message: fmt.Sprintf(format, args...)}}
}

func Usage(format string, args ...any) error {
	return &ExitError{Exit: ExitUsage, Err: Error{Code: "usage", Message: fmt.Sprintf(format, args...)}}
}

func Precondition(code, format string, args ...any) error {
	return &ExitError{Exit: ExitPrecondition, Err: Error{Code: code, Message: fmt.Sprintf(format, args...)}}
}

// ExitCode maps err to its exit code; any error that isn't an ExitError is
// ExitFailed.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if e, ok := errors.AsType[*ExitError](err); ok {
		return e.Exit
	}
	return ExitFailed
}

// ErrorObject is err as a JSON error object.
func ErrorObject(err error) Error {
	if e, ok := errors.AsType[*ExitError](err); ok {
		return e.Err
	}
	return Error{Code: "failed", Message: err.Error()}
}

// errorLine is what a command prints under --json when it fails, and what
// a --watch stream prints for a tick that failed.
type errorLine struct {
	Header
	Error Error `json:"error"`
}

// WriteError prints err as one JSON object under the given schema.
func WriteError(w io.Writer, schema string, err error) error {
	return writeLine(w, errorLine{Header: NewHeader(schema), Error: ErrorObject(err)})
}

// WriteJSON prints one JSON object, indented, for a one-shot read.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeLine(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}

// Watch calls tick right away and then every interval until ctx is done,
// printing each result as one line of JSON. Each line is a full snapshot.
// A failed tick prints an error line under schema and the stream carries
// on. It returns nil when ctx is cancelled, which is how a client stops it.
func Watch(ctx context.Context, w io.Writer, schema string, interval time.Duration, tick func(context.Context) (any, error)) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		v, err := tick(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			if werr := WriteError(w, schema, err); werr != nil {
				return werr
			}
		} else if werr := writeLine(w, v); werr != nil {
			return werr
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
