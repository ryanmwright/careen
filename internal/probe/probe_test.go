package probe

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshtest"
)

func TestParse(t *testing.T) {
	out, err := os.ReadFile("testdata/reboot-pending.txt")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Hostname != "app1" || r.ConfigRevision != "7a2b4c6d8e0f1a3b5c7d9e1f3a5b7c9d1e3f5a7b-dirty" {
		t.Errorf("identity: %+v", r)
	}
	if r.SystemState != "degraded" || !slices.Equal(r.FailedUnits, []string{"restic-backup.service", "nginx.service"}) {
		t.Errorf("health: state=%q failed=%v", r.SystemState, r.FailedUnits)
	}
	if got := r.RebootReasons(); !slices.Equal(got, []string{"kernel", "kernel-modules"}) {
		t.Errorf("reboot reasons = %v", got)
	}
	if r.Uptime != 76*time.Hour {
		t.Errorf("uptime = %v", r.Uptime)
	}
	if r.StoreSize != 107374182400 || r.StoreUsed != 42949672960 || r.Generations != 14 {
		t.Errorf("disk: size=%d used=%d gens=%d", r.StoreSize, r.StoreUsed, r.Generations)
	}
	if !r.LastSwitch.Equal(time.Unix(1791500000, 0)) {
		t.Errorf("last switch = %v", r.LastSwitch)
	}
}

func TestStagedForBoot(t *testing.T) {
	r := &Result{CurrentSystem: "/nix/store/a", ProfileSystem: "/nix/store/b"}
	if got := r.RebootReasons(); !slices.Equal(got, []string{"staged"}) {
		t.Errorf("reboot reasons = %v", got)
	}
}

func TestParseTruncated(t *testing.T) {
	if _, err := Parse([]byte("hostname=x\nuptime=1\n")); err == nil {
		t.Error("expected an error for output without probe_done")
	}
}

// TestProbeOverSSH runs the real script through a local sshd. On NixOS it
// reports this machine; elsewhere the NixOS fields are empty.
func TestProbeOverSSH(t *testing.T) {
	srv := sshtest.Start(t)
	p := New()
	p.SSHArgs = []string{"-F", srv.ConfigFile, "-o", "ControlMaster=no"}
	host := inventory.Host{Name: "test", Address: sshtest.HostAlias}

	r, err := p.Probe(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.Hostname()
	if r.Hostname != want {
		t.Errorf("hostname = %q, want %q", r.Hostname, want)
	}
	if r.Uptime <= 0 {
		t.Errorf("uptime = %v", r.Uptime)
	}

	srv.Stop()
	outs := p.ProbeAll(context.Background(), []inventory.Host{host})
	if outs[0].Err == nil || outs[0].Err.Code != "unreachable" || outs[0].Err.Host != "test" {
		t.Errorf("after stop: %+v", outs[0])
	}
}
