package contract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{errors.New("boom"), ExitFailed},
		{Usage("bad flag"), ExitUsage},
		{fmt.Errorf("wrapped: %w", Precondition("unreachable", "no route")), ExitPrecondition},
		{Failed("eval", "nix eval failed"), ExitFailed},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestWriteError(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteError(&buf, "careen.status/v1", Usage("unknown host %q", "x")); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Schema string `json:"schema"`
		Error  Error  `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != "careen.status/v1" || got.Error.Code != "usage" || got.Error.Message != `unknown host "x"` {
		t.Errorf("got %+v", got)
	}
}

func TestWatchKeepsStreamingAfterErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	n := 0
	tick := func(context.Context) (any, error) {
		n++
		if n == 3 {
			cancel()
		}
		if n == 2 {
			return nil, errors.New("source down")
		}
		return map[string]int{"n": n}, nil
	}
	if err := Watch(ctx, &buf, "test/v1", time.Millisecond, tick); err != nil {
		t.Fatal(err)
	}

	var lines []map[string]any
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		lines = append(lines, m)
	}
	// The third tick cancels the context, so its result isn't printed.
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}
	if lines[0]["n"] != float64(1) {
		t.Errorf("line 0 = %v", lines[0])
	}
	if e, ok := lines[1]["error"].(map[string]any); !ok || e["message"] != "source down" || lines[1]["schema"] != "test/v1" {
		t.Errorf("line 1 = %v", lines[1])
	}
}
