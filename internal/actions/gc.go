package actions

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ryanmwright/careen/internal/activate"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
)

const GCPlanSchema = "careen.gc.plan/v1"

type GCOptions struct {
	OlderThan string // a nix-collect-garbage age, e.g. 14d
	Parallel  int
}

// GCPlan is what GC will do; its JSON form is careen.gc.plan/v1.
type GCPlan struct {
	contract.Header
	OlderThan string   `json:"olderThan"`
	Hosts     []GCHost `json:"hosts"`

	targets []inventory.Host
	opts    GCOptions
}

// GCHost is a host as the plan sees it, before collecting.
type GCHost struct {
	Host        string          `json:"host"`
	Generations int             `json:"generations"`
	StoreUsed   int64           `json:"storeUsedBytes"`
	StoreSize   int64           `json:"storeSizeBytes"`
	Error       *contract.Error `json:"error,omitempty"`
}

func (p *GCPlan) Targets() []inventory.Host { return p.targets }

var ageRE = regexp.MustCompile(`^[0-9]+d$`)

// PlanGC probes the hosts sel selects for their generations and store use.
// Hosts that can't be reached stay in the plan with their error and are
// left out of the run.
func PlanGC(ctx context.Context, inv *inventory.Inventory, sel inventory.Selector, opts GCOptions) (*GCPlan, error) {
	if !ageRE.MatchString(opts.OlderThan) {
		return nil, contract.Usage("--older-than is a number of days such as 14d, not %q", opts.OlderThan)
	}
	hosts, err := inv.Select(sel)
	if err != nil {
		return nil, err
	}
	return PlanGCFrom(probe.New().ProbeAll(ctx, InOnlyOrder(hosts, sel.Only)), opts), nil
}

// PlanGCFrom builds the plan from probes already made.
func PlanGCFrom(outcomes []probe.Outcome, opts GCOptions) *GCPlan {
	p := &GCPlan{Header: contract.NewHeader(GCPlanSchema), OlderThan: opts.OlderThan, Hosts: []GCHost{}, opts: opts}
	for _, o := range outcomes {
		gh := GCHost{Host: o.Host.Name, Error: o.Err}
		if o.Result != nil {
			gh.Generations, gh.StoreUsed, gh.StoreSize = o.Result.Generations, o.Result.StoreUsed, o.Result.StoreSize
			p.targets = append(p.targets, o.Host)
		}
		p.Hosts = append(p.Hosts, gh)
	}
	return p
}

// GCResult is one host's collection.
type GCResult struct {
	Host              string          `json:"host"`
	PathsDeleted      int             `json:"pathsDeleted"`
	Freed             int64           `json:"freedBytes"`
	GenerationsBefore int             `json:"generationsBefore"`
	GenerationsAfter  int             `json:"generationsAfter"`
	Error             *contract.Error `json:"error,omitempty"`
}

type GCDone struct {
	Hosts []GCResult `json:"hosts"`
	Freed int64      `json:"freedBytes"`
}

// GC collects garbage on the plan's hosts in parallel.
func GC(ctx context.Context, em *contract.Emitter, p *GCPlan) (GCDone, error) {
	em.Started("gc", p)
	results := make([]GCResult, len(p.targets))
	sem := make(chan struct{}, max(p.opts.Parallel, 1))
	var wg sync.WaitGroup
	for i, h := range p.targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			em.Progress(h.Name, "gc", "deleting generations older than %s and unreachable paths", p.OlderThan)
			r, err := gcOne(ctx, h, p.OlderThan)
			if err != nil {
				e := contract.ErrorObject(contract.Failed("gc", "%v", err))
				e.Host = h.Name
				r.Error = &e
				em.Error(h.Name, "gc", err)
			} else {
				em.Progress(h.Name, "done", "%d paths deleted, %s freed (generations %d → %d)", r.PathsDeleted, Bytes(r.Freed), r.GenerationsBefore, r.GenerationsAfter)
			}
			results[i] = r
		})
	}
	wg.Wait()

	done := GCDone{Hosts: results}
	failed := 0
	for _, r := range results {
		done.Freed += r.Freed
		if r.Error != nil {
			failed++
		}
	}
	em.Done(done)
	if failed > 0 {
		return done, contract.Failed("gc", "%d of %d host(s) failed", failed, len(results))
	}
	return done, nil
}

func gcOne(ctx context.Context, h inventory.Host, olderThan string) (GCResult, error) {
	r := GCResult{Host: h.Name}
	out, err := activate.Run(ctx, Conn(h, false), "gc", olderThan)
	if err != nil {
		return r, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "generations_before":
			r.GenerationsBefore, _ = strconv.Atoi(strings.TrimSpace(v))
		case "generations_after":
			r.GenerationsAfter, _ = strconv.Atoi(strings.TrimSpace(v))
		case "gc_output":
			if n, freed, ok := ParseGCSummary(v); ok {
				r.PathsDeleted, r.Freed = n, freed
			}
		}
	}
	return r, nil
}

var gcSummaryRE = regexp.MustCompile(`(\d+) store paths deleted, ([\d.]+) (B|KiB|MiB|GiB|TiB) freed`)

// ParseGCSummary reads nix-collect-garbage's last line, e.g. "1234 store
// paths deleted, 567.89 MiB freed".
func ParseGCSummary(line string) (paths int, freed int64, ok bool) {
	m := gcSummaryRE.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	paths, _ = strconv.Atoi(m[1])
	f, _ := strconv.ParseFloat(m[2], 64)
	unit := map[string]float64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[m[3]]
	return paths, int64(f * unit), true
}

// Bytes is a size in the largest binary unit that keeps it at least 1.
func Bytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
