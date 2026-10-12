// Package vulns runs vulnix against a system closure in the local store and
// caches the results per host and system.
package vulns

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
)

type CVE struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"` // CVSS v3 base score; 0 when NVD has none
}

// Finding is one package with known CVEs.
type Finding struct {
	Package     string   `json:"package"`
	Version     string   `json:"version"`
	Derivation  string   `json:"derivation,omitempty"`
	CVEs        []CVE    `json:"cves"` // highest score first
	Whitelisted []string `json:"whitelisted,omitempty"`
}

// MaxScore is the finding's highest CVE score.
func (f Finding) MaxScore() float64 {
	if len(f.CVEs) == 0 {
		return 0
	}
	return f.CVEs[0].Score
}

// Summary counts CVEs by severity.
type Summary struct {
	Critical int `json:"critical"` // ≥ 9
	High     int `json:"high"`     // ≥ 7
	Medium   int `json:"medium"`   // ≥ 4
	Low      int `json:"low"`      // < 4, or no score
}

func (s Summary) Total() int { return s.Critical + s.High + s.Medium + s.Low }

// Short is e.g. "2C 5H 9M 1L", or "none".
func (s Summary) Short() string {
	if s.Total() == 0 {
		return "none"
	}
	out := ""
	for _, p := range []struct {
		n int
		l string
	}{{s.Critical, "C"}, {s.High, "H"}, {s.Medium, "M"}, {s.Low, "L"}} {
		if p.n > 0 {
			if out != "" {
				out += " "
			}
			out += fmt.Sprintf("%d%s", p.n, p.l)
		}
	}
	return out
}

func Summarize(fs []Finding) Summary {
	var s Summary
	for _, f := range fs {
		for _, c := range f.CVEs {
			switch {
			case c.Score >= 9:
				s.Critical++
			case c.Score >= 7:
				s.High++
			case c.Score >= 4:
				s.Medium++
			default:
				s.Low++
			}
		}
	}
	return s
}

// Report is one host's scan.
type Report struct {
	Host      string          `json:"host"`
	System    string          `json:"system,omitempty"` // the store path scanned
	ScannedAt time.Time       `json:"scannedAt"`
	Summary   Summary         `json:"summary"`
	Findings  []Finding       `json:"findings"`
	Error     *contract.Error `json:"error,omitempty"`
}

// Parse reads `vulnix --json`. Findings come out highest score first.
func Parse(b []byte) ([]Finding, error) {
	var raw []struct {
		Pname       string             `json:"pname"`
		Version     string             `json:"version"`
		Derivation  string             `json:"derivation"`
		AffectedBy  []string           `json:"affected_by"`
		Whitelisted []string           `json:"whitelisted"`
		Scores      map[string]float64 `json:"cvssv3_basescore"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parsing vulnix output: %w", err)
	}
	out := []Finding{}
	for _, r := range raw {
		f := Finding{Package: r.Pname, Version: r.Version, Derivation: r.Derivation, Whitelisted: r.Whitelisted, CVEs: []CVE{}}
		for _, id := range r.AffectedBy {
			f.CVEs = append(f.CVEs, CVE{ID: id, Score: r.Scores[id]})
		}
		slices.SortFunc(f.CVEs, func(a, b CVE) int { return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.ID, b.ID)) })
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(b.MaxScore(), a.MaxScore()), cmp.Compare(a.Package, b.Package))
	})
	return out, nil
}

// Scan runs vulnix on the runtime closure of path, which must be in the
// local store. whitelist is a vulnix TOML whitelist, or "".
func Scan(ctx context.Context, path, whitelist string) ([]Finding, error) {
	if _, err := exec.LookPath("vulnix"); err != nil {
		return nil, errors.New("vulnix isn't installed here; add pkgs.vulnix where careen runs")
	}
	args := []string{"--closure", "--json"}
	if whitelist != "" {
		args = append(args, "--whitelist", whitelist)
	}
	cmd := exec.CommandContext(ctx, "vulnix", append(args, path)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return result(cmd.Run(), stdout.Bytes(), stderr.String())
}

// result interprets a vulnix run.
func result(err error, stdout []byte, stderr string) ([]Finding, error) {
	// vulnix exits 2 when it found something, which isn't a failure here.
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 2 {
		err = nil
	}
	if err != nil {
		return nil, fmt.Errorf("vulnix: %w: %s", err, execx.Tail(stderr, 3))
	}
	// It also exits 2 when it fails, printing a traceback and no report.
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, fmt.Errorf("vulnix printed no report: %s", execx.Tail(stderr, 1))
	}
	return Parse(stdout)
}

// cachePath is where host's last report is kept.
func cachePath(host string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "careen", "vulns", host+".json"), nil
}

// Cached is host's last report if it scanned system and is younger than
// maxAge (zero means any age).
func Cached(host, system string, maxAge time.Duration) *Report {
	p, err := cachePath(host)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var r Report
	if json.Unmarshal(b, &r) != nil || r.System != system || r.Error != nil {
		return nil
	}
	if maxAge > 0 && time.Since(r.ScannedAt) > maxAge {
		return nil
	}
	return &r
}

// Save keeps r as its host's last report. Best effort.
func Save(r Report) {
	p, err := cachePath(r.Host)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	b, _ := json.Marshal(r)
	if os.WriteFile(p+".tmp", b, 0o600) == nil {
		os.Rename(p+".tmp", p)
	}
}
