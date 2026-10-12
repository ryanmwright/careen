package status

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden.json")

func TestDrift(t *testing.T) {
	cases := []struct{ want, have, drift string }{
		{"abc", "abc", DriftCurrent},
		{"abc", "def", DriftDiffers},
		{"abc", "", DriftUnknown},
		{"", "abc", DriftUnknown},
	}
	for _, c := range cases {
		if got := drift(c.want, c.have); got != c.drift {
			t.Errorf("drift(%q, %q) = %q, want %q", c.want, c.have, got, c.drift)
		}
	}
}

func TestBuildGolden(t *testing.T) {
	flake := inventory.Flake{Ref: "git+ssh://example/nix?ref=main", Revision: "3c1e9a0b7d2f4e6a8c0b1d3f5a7c9e1b2d4f6a8c"}
	switched := time.Date(2026, 10, 8, 14, 2, 0, 0, time.UTC)
	outcomes := []probe.Outcome{
		{
			Host: inventory.Host{Name: "app1", Address: "10.0.0.40"},
			Result: &probe.Result{
				NixOSVersion:   "26.05",
				ConfigRevision: "7a2b4c6d8e0f1a3b5c7d9e1f3a5b7c9d1e3f5a7b-dirty",
				Booted:         map[string]string{"kernel": "/nix/store/a", "initrd": "/nix/store/i", "kernel-modules": "/nix/store/m1"},
				Current:        map[string]string{"kernel": "/nix/store/b", "initrd": "/nix/store/i", "kernel-modules": "/nix/store/m2"},
				SystemState:    "degraded",
				FailedUnits:    []string{"nginx.service"},
				Uptime:         76 * time.Hour,
				StoreSize:      100 << 30,
				StoreUsed:      40 << 30,
				Generations:    14,
				LastSwitch:     switched,
			},
		},
		{
			Host: inventory.Host{Name: "app2", Address: "app2.example"},
			Result: &probe.Result{
				ConfigRevision: "3c1e9a0b7d2f4e6a8c0b1d3f5a7c9e1b2d4f6a8c",
				Booted:         map[string]string{"kernel": "/nix/store/a"},
				Current:        map[string]string{"kernel": "/nix/store/a"},
				SystemState:    "running",
				FailedUnits:    []string{},
				Uptime:         time.Hour,
			},
		},
		{
			Host: inventory.Host{Name: "mgr", Address: "mgr.example"},
			Err:  &contract.Error{Code: "unreachable", Message: "ssh: connect to host mgr.example port 22: No route to host", Host: "mgr"},
		},
	}
	rep := Build(flake, outcomes)
	rep.GeneratedAt = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	var got bytes.Buffer
	if err := contract.WriteJSON(&got, rep); err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/status.golden.json"
	if *update {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("report differs from %s (run go test ./internal/status -update to accept):\n%s", golden, got.String())
	}

	var back Report
	if err := json.Unmarshal(got.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	b := back.Hosts[0]
	if b.State == nil || b.Drift != DriftDiffers || !b.Dirty || !b.RebootPending || len(b.RebootReasons) != 2 {
		t.Errorf("app1 = %+v", b)
	}
	if d := back.Hosts[1]; d.State == nil || d.Drift != DriftCurrent || d.Dirty || d.RebootPending {
		t.Errorf("app2 = %+v", d)
	}
	if o := back.Hosts[2]; o.Error == nil || o.Error.Code != "unreachable" || o.State != nil {
		t.Errorf("mgr = %+v", o)
	}
}
