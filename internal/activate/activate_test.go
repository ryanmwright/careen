package activate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/sshx"
)

const (
	oldSys = "/nix/store/old-system"
	newSys = "/nix/store/new-system"
)

// fakeHost plays a host's side of activate.sh.
type fakeHost struct {
	current, profile string
	failedBefore     []string
	switchErr        error  // what "switch" returns
	afterState       string // system_state after the switch
	failedAfter      []string
	unreachable      int // health checks that fail before one succeeds
	calls            []string
}

func (f *fakeHost) Local() bool { return false }

func (f *fakeHost) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	units := func(us []string) string {
		var b strings.Builder
		for _, u := range us {
			b.WriteString("failed_unit=" + u + "\n")
		}
		return b.String()
	}
	switch args[0] {
	case "state":
		return []byte("current=" + f.current + "\nprofile=" + f.profile + "\n" + units(f.failedBefore)), nil
	case "switch":
		f.profile = args[1]
		if args[3] == "switch" {
			f.current = args[1]
		}
		return nil, f.switchErr
	case "health":
		if f.unreachable > 0 {
			f.unreachable--
			return nil, fmt.Errorf("%w: timed out", sshx.ErrUnreachable)
		}
		return []byte("system_state=" + f.afterState + "\ncurrent=" + f.current + "\n" + units(f.failedAfter)), nil
	case "rollback":
		f.current, f.profile = args[1], args[1]
		return nil, nil
	case "confirm":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected action %v", args)
}

func (f *fakeHost) did(action string) bool {
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, action+" ") || c == action })
}

var opts = Options{Operation: "switch", Watchdog: 5 * time.Minute, HealthTimeout: time.Second}

func noProgress(string, string) {}

func TestActivateHealthy(t *testing.T) {
	h := &fakeHost{current: oldSys, profile: oldSys, failedBefore: []string{"flaky.service"}, afterState: "degraded", failedAfter: []string{"flaky.service"}}
	res, err := Activate(context.Background(), h, h, newSys, opts, noProgress)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != Switched || res.From != oldSys || res.To != newSys {
		t.Errorf("result = %+v", res)
	}
	if !h.did("confirm") || h.did("rollback") {
		t.Errorf("calls = %v", h.calls)
	}
	// The watchdog is armed with the previous system and the timeout.
	sw := h.calls[slices.IndexFunc(h.calls, func(c string) bool { return strings.HasPrefix(c, "switch ") })]
	if !strings.HasPrefix(sw, "switch "+newSys+" "+oldSys+" switch 300 careen-rollback-") {
		t.Errorf("switch call = %q", sw)
	}
}

func TestActivateRollsBackOnNewFailedUnit(t *testing.T) {
	h := &fakeHost{current: oldSys, profile: oldSys, afterState: "degraded", failedAfter: []string{"nginx.service"}}
	res, err := Activate(context.Background(), h, h, newSys, opts, noProgress)
	if err == nil || res.Status != RolledBack || h.current != oldSys {
		t.Errorf("err=%v result=%+v current=%s", err, res, h.current)
	}
	if h.did("confirm") {
		t.Error("confirmed a failed switch")
	}
}

func TestActivateRollsBackWhenSwitchFails(t *testing.T) {
	h := &fakeHost{current: oldSys, profile: oldSys, switchErr: fmt.Errorf("exit status 4: warning: the following units failed: x.service")}
	res, err := Activate(context.Background(), h, h, newSys, opts, noProgress)
	if err == nil || res.Status != RolledBack || h.current != oldSys {
		t.Errorf("err=%v result=%+v current=%s", err, res, h.current)
	}
}

func TestActivateSurvivesLostConnection(t *testing.T) {
	h := &fakeHost{current: oldSys, profile: oldSys, afterState: "running", unreachable: 1,
		switchErr: fmt.Errorf("%w: broken pipe", sshx.ErrUnreachable)}
	o := opts
	o.Poll = time.Millisecond
	res, err := Activate(context.Background(), h, h, newSys, o, noProgress)
	if err != nil || res.Status != Switched {
		t.Errorf("err=%v result=%+v", err, res)
	}
}

func TestActivateUnchangedAndBoot(t *testing.T) {
	h := &fakeHost{current: newSys, profile: newSys}
	if res, err := Activate(context.Background(), h, h, newSys, opts, noProgress); err != nil || res.Status != Unchanged {
		t.Errorf("unchanged: err=%v result=%+v", err, res)
	}

	h = &fakeHost{current: oldSys, profile: oldSys}
	boot := opts
	boot.Operation = "boot"
	res, err := Activate(context.Background(), h, h, newSys, boot, noProgress)
	if err != nil || res.Status != Staged || h.profile != newSys || h.current != oldSys {
		t.Errorf("boot: err=%v result=%+v host=%+v", err, res, h)
	}
	if h.did("health") || h.did("confirm") {
		t.Errorf("boot ran a health check: %v", h.calls)
	}
}
