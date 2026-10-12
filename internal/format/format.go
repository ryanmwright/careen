// Package format turns status values into the short strings the CLI tables
// and the TUI show.
package format

import (
	"fmt"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/status"
)

// Short is a revision's first 7 characters.
func Short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// Dash is s, or "-" when it's empty.
func Dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func YesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// Duration is a short, coarse duration: 3d4h, 5h12m, 7m.
func Duration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", d/(24*time.Hour), d%(24*time.Hour)/time.Hour)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", d/time.Hour, d%time.Hour/time.Minute)
	default:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
}

// Store is used/size of the store's filesystem, in GiB.
func Store(used, size int64) string {
	if size == 0 {
		return "-"
	}
	const gib = 1 << 30
	return fmt.Sprintf("%.1f/%.0fG", float64(used)/gib, float64(size)/gib)
}

// FlakeLine is "flake REF @ REV".
func FlakeLine(f inventory.Flake) string {
	rev := Short(f.Revision)
	if f.Dirty {
		rev += "-dirty"
	}
	return fmt.Sprintf("flake %s @ %s", f.Ref, Dash(rev))
}

// Revision is a row's short revision, marked -dirty.
func Revision(st *status.State) string {
	rev := Short(st.Revision)
	if st.Dirty {
		rev += "-dirty"
	}
	return Dash(rev)
}

// Reboot is "no" or the reasons a reboot is pending.
func Reboot(st *status.State) string {
	if !st.RebootPending {
		return "no"
	}
	return strings.Join(st.RebootReasons, ",")
}

// Ago is how long before now t was, or "-".
func Ago(t *time.Time, now time.Time) string {
	if t == nil {
		return "-"
	}
	return Duration(now.Sub(*t)) + " ago"
}
