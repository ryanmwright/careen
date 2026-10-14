package execx

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// Timing is one timed command or phase.
type Timing struct {
	What     string
	Duration time.Duration
	// Phase marks a step of careen's own (a host's deploy, say) rather than
	// one command; the commands it ran are counted again on their own.
	Phase bool
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
func (t *Timings) Add(what string, d time.Duration) { t.add(Timing{What: what, Duration: d}) }

// AddPhase records a step of careen's own that took d.
func (t *Timings) AddPhase(what string, d time.Duration) {
	t.add(Timing{What: what, Duration: d, Phase: true})
}

func (t *Timings) add(it Timing) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items = append(t.items, it)
}

// Items lists everything recorded, in the order it finished.
func (t *Timings) Items() []Timing {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.items)
}
