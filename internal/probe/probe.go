// Package probe collects a host's state over one SSH call: it pipes an
// embedded shell script to `sh -s` on the host and parses the key=value
// lines it prints.
package probe

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshx"
)

//go:embed probe.sh
var script string

// BootParts are the parts of a system that only change on reboot.
var BootParts = []string{"kernel", "initrd", "kernel-modules"}

// Result is what the script reports about one host.
type Result struct {
	Hostname       string
	NixOSVersion   string
	ConfigRevision string
	CurrentSystem  string            // /run/current-system
	ProfileSystem  string            // the system profile, which the next boot uses
	Booted         map[string]string // BootParts → store path
	Current        map[string]string
	SystemState    string
	FailedUnits    []string
	Uptime         time.Duration
	StoreSize      int64
	StoreUsed      int64
	Generations    int
	LastSwitch     time.Time // zero if unknown
}

// RebootReasons lists the boot parts that differ between the booted and
// the current system, plus "staged" when the system profile holds a system
// installed for the next boot. A part missing from either side is ignored.
func (r *Result) RebootReasons() []string {
	var out []string
	if r.ProfileSystem != "" && r.CurrentSystem != "" && r.ProfileSystem != r.CurrentSystem {
		out = append(out, "staged")
	}
	for _, p := range BootParts {
		b, c := r.Booted[p], r.Current[p]
		if b != "" && c != "" && b != c {
			out = append(out, p)
		}
	}
	return out
}

// Parse reads the script's output.
func Parse(out []byte) (*Result, error) {
	r := &Result{Booted: map[string]string{}, Current: map[string]string{}, FailedUnits: []string{}}
	done := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch {
		case key == "hostname":
			r.Hostname = val
		case key == "nixos_version":
			r.NixOSVersion = val
		case key == "config_revision":
			r.ConfigRevision = val
		case key == "current_system":
			r.CurrentSystem = val
		case key == "profile_system":
			r.ProfileSystem = val
		case strings.HasPrefix(key, "booted_"):
			r.Booted[strings.TrimPrefix(key, "booted_")] = val
		case strings.HasPrefix(key, "current_"):
			r.Current[strings.TrimPrefix(key, "current_")] = val
		case key == "system_state":
			r.SystemState = val
		case key == "failed_unit":
			r.FailedUnits = append(r.FailedUnits, val)
		case key == "uptime":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				r.Uptime = time.Duration(f * float64(time.Second)).Truncate(time.Second)
			}
		case key == "store_size":
			r.StoreSize, _ = strconv.ParseInt(val, 10, 64)
		case key == "store_used":
			r.StoreUsed, _ = strconv.ParseInt(val, 10, 64)
		case key == "generations":
			r.Generations, _ = strconv.Atoi(val)
		case key == "last_switch":
			if s, err := strconv.ParseInt(val, 10, 64); err == nil && s > 0 {
				r.LastSwitch = time.Unix(s, 0).UTC()
			}
		case key == "probe_done":
			done = true
		}
	}
	if !done {
		return nil, errors.New("probe output ended early")
	}
	return r, nil
}

// Prober runs the script on hosts.
type Prober struct {
	// Timeout bounds each host's probe, connection included.
	Timeout time.Duration
	// Parallel is how many hosts are probed at once.
	Parallel int
	// SSHArgs are extra ssh arguments; they take precedence over careen's
	// defaults. Tests use them to pass -F.
	SSHArgs []string
}

func New() *Prober {
	return &Prober{Timeout: 15 * time.Second, Parallel: 16}
}

// Outcome is one host's probe: a Result or an error.
type Outcome struct {
	Host   inventory.Host
	Result *Result
	Err    *contract.Error
}

// ProbeAll probes hosts in parallel and returns their outcomes in the same
// order.
func (p *Prober) ProbeAll(ctx context.Context, hosts []inventory.Host) []Outcome {
	out := make([]Outcome, len(hosts))
	sem := make(chan struct{}, max(p.Parallel, 1))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = Outcome{Host: h}
			r, err := p.Probe(ctx, h)
			if err != nil {
				e := contract.ErrorObject(err)
				e.Host = h.Name
				out[i].Err = &e
				return
			}
			out[i].Result = r
		})
	}
	wg.Wait()
	return out
}

// Probe runs the script on one host.
func (p *Prober) Probe(ctx context.Context, h inventory.Host) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()

	r := sshx.Remote{User: h.User, Address: h.Address, Args: p.SSHArgs}
	out, err := r.Run(ctx, script)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, contract.Failed("timeout", "no answer within %s", p.Timeout)
	case errors.Is(err, sshx.ErrUnreachable):
		return nil, contract.Failed("unreachable", "%s", strings.TrimPrefix(err.Error(), sshx.ErrUnreachable.Error()+": "))
	case err != nil:
		return nil, contract.Failed("probe", "%v", err)
	}
	res, err := Parse(out)
	if err != nil {
		return nil, contract.Failed("probe", "%v", err)
	}
	return res, nil
}
