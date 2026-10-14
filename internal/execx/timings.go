package execx

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Timing is one timed command or phase.
type Timing struct {
	What     string
	Duration time.Duration
}

// Timings records how long commands take, to show where a slow run went.
// It is safe for concurrent use, and a nil *Timings records nothing.
type Timings struct {
	mu    sync.Mutex
	items []Timing
}

// Wrap returns a Runner that records the time each command takes.
func (t *Timings) Wrap(run Runner) Runner {
	if t == nil {
		return run
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		start := time.Now()
		out, err := run(ctx, name, args...)
		t.Add(strings.TrimSpace(name+" "+strings.Join(args, " ")), time.Since(start))
		return out, err
	}
}

// Add records a phase that took d.
func (t *Timings) Add(what string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items = append(t.items, Timing{what, d})
}

// Slow lists what took at least min, in the order it finished.
func (t *Timings) Slow(min time.Duration) []Timing {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Timing
	for _, it := range t.items {
		if it.Duration >= min {
			out = append(out, it)
		}
	}
	return out
}
