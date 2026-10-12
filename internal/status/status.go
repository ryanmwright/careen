// Package status builds the careen.status/v1 report from the inventory and
// the probe outcomes.
package status

import (
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
)

const Schema = "careen.status/v1"

// Drift compares a host's running revision with the flake's.
const (
	DriftCurrent = "current" // built from the flake's revision
	DriftDiffers = "differs" // built from another revision
	DriftUnknown = "unknown" // the host reports no revision
)

type Report struct {
	contract.Header
	Flake inventory.Flake `json:"flake"`
	Hosts []Row           `json:"hosts"`
}

// Row is one host: Error when it couldn't be probed, State otherwise.
type Row struct {
	Host    string          `json:"host"`
	Address string          `json:"address"`
	Error   *contract.Error `json:"error,omitempty"`
	*State
}

// State is what the probe found. Its fields sit directly in the row's JSON.
type State struct {
	Revision string `json:"revision,omitempty"`
	// System is the running system's store path.
	System string `json:"system,omitempty"`
	// Dirty: the host was built from a working tree with uncommitted
	// changes on top of Revision.
	Dirty         bool       `json:"dirty"`
	Drift         string     `json:"drift"`
	RebootPending bool       `json:"rebootPending"`
	RebootReasons []string   `json:"rebootReasons"`
	SystemState   string     `json:"systemState,omitempty"`
	FailedUnits   []string   `json:"failedUnits"`
	UptimeSeconds int64      `json:"uptimeSeconds"`
	StoreUsed     int64      `json:"storeUsedBytes"`
	StoreSize     int64      `json:"storeSizeBytes"`
	Generations   int        `json:"generations"`
	LastSwitch    *time.Time `json:"lastSwitch,omitempty"`
	NixOSVersion  string     `json:"nixosVersion,omitempty"`
}

// Build assembles the report. outcomes are in host order.
func Build(flake inventory.Flake, outcomes []probe.Outcome) Report {
	rep := Report{Header: contract.NewHeader(Schema), Flake: flake, Hosts: []Row{}}
	for _, o := range outcomes {
		rep.Hosts = append(rep.Hosts, row(flake, o))
	}
	return rep
}

func row(flake inventory.Flake, o probe.Outcome) Row {
	r := Row{Host: o.Host.Name, Address: o.Host.Address}
	if o.Err != nil {
		r.Error = o.Err
		return r
	}
	res := o.Result
	st := &State{
		System:        res.CurrentSystem,
		RebootReasons: res.RebootReasons(),
		SystemState:   res.SystemState,
		FailedUnits:   res.FailedUnits,
		UptimeSeconds: int64(res.Uptime / time.Second),
		StoreUsed:     res.StoreUsed,
		StoreSize:     res.StoreSize,
		Generations:   res.Generations,
		NixOSVersion:  res.NixOSVersion,
	}
	st.Revision, st.Dirty = splitRevision(res.ConfigRevision)
	st.Drift = drift(flake.Revision, st.Revision)
	if st.RebootReasons == nil {
		st.RebootReasons = []string{}
	}
	if st.FailedUnits == nil {
		st.FailedUnits = []string{}
	}
	st.RebootPending = len(st.RebootReasons) > 0
	if !res.LastSwitch.IsZero() {
		t := res.LastSwitch
		st.LastSwitch = &t
	}
	r.State = st
	return r
}

// splitRevision splits a configurationRevision such as "abc123-dirty".
func splitRevision(rev string) (string, bool) {
	base, dirty := strings.CutSuffix(rev, "-dirty")
	return base, dirty
}

func drift(want, have string) string {
	switch {
	case have == "" || want == "":
		return DriftUnknown
	case have == want:
		return DriftCurrent
	default:
		return DriftDiffers
	}
}
