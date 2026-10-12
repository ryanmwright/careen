package vulns

import (
	"os"
	"testing"
	"time"
)

func TestParseVulnixOutput(t *testing.T) {
	b, err := os.ReadFile("testdata/example.json")
	if err != nil {
		t.Fatal(err)
	}
	fs, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 5 {
		t.Fatalf("findings = %d", len(fs))
	}
	// Highest score first; ties by package name.
	if fs[0].MaxScore() != 9.8 || fs[0].Package != "glibc" {
		t.Errorf("first = %s %v", fs[0].Package, fs[0].MaxScore())
	}
	for i := 1; i < len(fs); i++ {
		if fs[i].MaxScore() > fs[i-1].MaxScore() {
			t.Errorf("not sorted at %d: %v > %v", i, fs[i].MaxScore(), fs[i-1].MaxScore())
		}
	}
	for _, f := range fs {
		for i := 1; i < len(f.CVEs); i++ {
			if f.CVEs[i].Score > f.CVEs[i-1].Score {
				t.Errorf("%s CVEs not sorted", f.Package)
			}
		}
	}
	s := Summarize(fs)
	if s.Total() != 8 || s.Critical != 2 || s.High != 3 || s.Medium != 1 || s.Low != 2 {
		t.Errorf("summary = %+v (total %d)", s, s.Total())
	}
}

func TestSummaryShort(t *testing.T) {
	if got := (Summary{Critical: 2, High: 5, Low: 1}).Short(); got != "2C 5H 1L" {
		t.Errorf("short = %q", got)
	}
	if got := (Summary{}).Short(); got != "none" {
		t.Errorf("empty = %q", got)
	}
}

func TestCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	Save(Report{Host: "web", System: "/nix/store/a-system", ScannedAt: time.Now().Add(-2 * time.Hour), Summary: Summary{High: 1}})
	if r := Cached("web", "/nix/store/a-system", 24*time.Hour); r == nil || r.Summary.High != 1 {
		t.Errorf("fresh: %+v", r)
	}
	if Cached("web", "/nix/store/b-system", 0) != nil {
		t.Error("a report for another system counted")
	}
	if Cached("web", "/nix/store/a-system", time.Hour) != nil {
		t.Error("an old report counted")
	}
	if Cached("db", "/nix/store/a-system", 0) != nil {
		t.Error("another host's report counted")
	}
}
