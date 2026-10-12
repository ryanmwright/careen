// Package inventory reads the fleet from the flake's `fleet` output: an
// attribute set of hosts, however the flake builds it.
package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
)

// Host is one entry of the flake's `fleet` output.
type Host struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Address      string   `json:"address"`
	User         string   `json:"user"`
	Tags         []string `json:"tags"`
	RebootPolicy string   `json:"rebootPolicy"`
	Deploy       bool     `json:"deploy"`
	BuildOn      string   `json:"buildOn"` // local, remote or target
	Builder      string   `json:"builder"` // store URI used when BuildOn is remote
	Operation    string   `json:"operation"`
	// Provisioner says how the machine comes to exist: "existing" (it's
	// already there; the default) or "proxmox" (careen up creates its VM
	// from the flake's pve inventory).
	Provisioner string `json:"provisioner"`
	// RebootOrder orders reboots: lower first, ties by name. --only's
	// order overrides it, and the machine careen runs on always goes last.
	RebootOrder int `json:"rebootOrder"`
	// Reboot is what careen runs around the host's reboot.
	Reboot RebootHooks `json:"reboot"`
}

// RebootHooks are commands careen runs around a host's reboot, for whatever
// the host is part of (a load balancer pool, a cluster): take it out first,
// put it back after, and wait until everything has settled before the next
// host. careen knows nothing about what the commands do.
type RebootHooks struct {
	// Pre runs before the reboot. If it times out, OnTimeout decides.
	Pre Hook `json:"pre"`
	// Post runs once the host is back and healthy.
	Post Hook `json:"post"`
	// Gate is rerun every Interval until it succeeds, within Timeout,
	// before careen moves on to the next host.
	Gate Hook `json:"gate"`
	// Settle is a further wait after the gate, as a duration ("15s").
	Settle string `json:"settle"`
}

// Hook is one command run around a reboot. Run and Force are Go templates
// with .Name and .Address (the host being rebooted).
type Hook struct {
	Run string `json:"run"`
	// On is where it runs: "self" (the default), "local" (the machine
	// careen runs on) or "tag:<tag>" (the first other host with that tag
	// that answers).
	On      string `json:"on"`
	Timeout string `json:"timeout"` // a duration
	// OnTimeout (pre only) is what to do when it times out: "ask", "force",
	// "skip" or "abort".
	OnTimeout string `json:"onTimeout"`
	// Force (pre only) is run instead when the answer to a timeout is force.
	Force string `json:"force"`
	// Interval (gate only) is the wait between tries, as a duration.
	Interval string `json:"interval"`
}

// HasHooks reports whether h has anything to run around its reboot.
func (h Host) HasHooks() bool {
	r := h.Reboot
	return r.Pre.Run != "" || r.Post.Run != "" || r.Gate.Run != "" || r.Settle != ""
}

// Provisioner names.
const (
	Existing = "existing"
	Proxmox  = "proxmox"
)

// ProvisionerName is h's provisioner, "existing" when the flake doesn't say.
func (h Host) ProvisionerName() string {
	if h.Provisioner == "" {
		return Existing
	}
	return h.Provisioner
}

// Flake identifies the flake the inventory came from.
type Flake struct {
	Ref string `json:"ref"`
	// Revision is the commit the ref resolved to. Hosts deployed from it
	// are current.
	Revision string `json:"revision,omitempty"`
	// Dirty is set when the ref is a working tree with uncommitted
	// changes; Revision is then its HEAD.
	Dirty bool `json:"dirty"`
	// Locked is the ref pinned to Revision, which builds use so every
	// host builds from the same commit. Empty when Dirty.
	Locked string `json:"locked,omitempty"`
	// Path is the flake's source in the local store, for reading files
	// that sit next to it (the vulnix whitelist).
	Path string `json:"-"`
}

// BuildRef is the flake reference builds should use.
func (f Flake) BuildRef() string {
	if f.Locked != "" {
		return f.Locked
	}
	return f.Ref
}

// AtRevision is Locked pinned to another commit of the same repository,
// or "" when there's no locked URL to pin.
func (f Flake) AtRevision(rev string) string {
	u, err := url.Parse(f.Locked)
	if f.Locked == "" || rev == "" || err != nil {
		return ""
	}
	q := u.Query()
	if !q.Has("rev") {
		return ""
	}
	for _, k := range []string{"narHash", "lastModified", "revCount"} {
		q.Del(k)
	}
	q.Set("rev", rev)
	u.RawQuery = q.Encode()
	return u.String()
}

type Inventory struct {
	Flake Flake
	Hosts []Host // sorted by name
}

// Load resolves ref to a revision and reads the fleet at that revision.
// With refresh, Nix re-fetches ref instead of using its cached copy, which
// can be up to an hour old; actions that change hosts use it so they
// deploy the ref's current head, and status uses it so drift is measured
// against that head rather than a stale one.
// Evaluating the fleet means evaluating every host's NixOS configuration,
// which takes seconds, so the result is cached by locked URL; a dirty tree
// is never cached. The last fleet loaded for ref is also kept, for LoadLast.
func Load(ctx context.Context, run execx.Runner, ref string, refresh bool) (*Inventory, error) {
	if ref == "" {
		return nil, contract.Usage("no flake given: pass --flake or set CAREEN_FLAKE")
	}

	metaArgs := []string{"flake", "metadata", "--json"}
	if refresh {
		metaArgs = append(metaArgs, "--refresh")
	}
	metaOut, err := run(ctx, "nix", append(metaArgs, ref)...)
	if err != nil {
		return nil, contract.Failed("inventory", "reading flake metadata for %s: %v", ref, err)
	}
	var meta struct {
		URL           string `json:"url"` // locked when the tree is clean
		Path          string `json:"path"`
		Revision      string `json:"revision"`
		DirtyRevision string `json:"dirtyRevision"`
	}
	if err := json.Unmarshal(metaOut, &meta); err != nil {
		return nil, contract.Failed("inventory", "parsing flake metadata for %s: %v", ref, err)
	}
	inv := &Inventory{Flake: Flake{Ref: ref, Path: meta.Path}}
	locked := ""
	switch {
	case meta.Revision != "":
		inv.Flake.Revision = meta.Revision
		locked = meta.URL
		inv.Flake.Locked = locked
	case meta.DirtyRevision != "":
		inv.Flake.Revision, inv.Flake.Dirty = strings.TrimSuffix(meta.DirtyRevision, "-dirty"), true
	}

	out, cached := readCache(locked)
	if !cached {
		evalRef := ref
		if locked != "" {
			evalRef = locked
		}
		if out, err = run(ctx, "nix", "eval", "--json", evalRef+"#fleet"); err != nil {
			return nil, contract.Failed("inventory", "reading the fleet from %s: %v", ref, err)
		}
	}
	hosts, err := parseFleet(out)
	if err != nil {
		return nil, contract.Failed("inventory", "parsing %s#fleet: %v", ref, err)
	}
	inv.Hosts = hosts
	if !cached {
		writeCache(cachePath(locked), out)
	}
	if locked != "" {
		if b, err := json.Marshal(last{Flake: inv.Flake, Path: inv.Flake.Path, Fleet: out}); err == nil {
			writeCache(lastPath(ref), b)
		}
	}
	return inv, nil
}

// parseFleet reads the `fleet` output into hosts sorted by name.
func parseFleet(out []byte) ([]Host, error) {
	var raw map[string]Host
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	var hosts []Host
	for name, h := range raw {
		h.Name = name
		if h.Tags == nil {
			h.Tags = []string{}
		}
		hosts = append(hosts, h)
	}
	slices.SortFunc(hosts, func(a, b Host) int { return strings.Compare(a.Name, b.Name) })
	return hosts, nil
}

// last is the fleet Load last read for a ref.
type last struct {
	Flake Flake           `json:"flake"`
	Path  string          `json:"path"`
	Fleet json.RawMessage `json:"fleet"`
}

// LoadLast is the inventory Load last returned for ref, without asking
// Nix, so it may be behind the ref's head; false if there is none (or the
// ref was a dirty tree, which isn't kept).
func LoadLast(ref string) (*Inventory, bool) {
	path := lastPath(ref)
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var l last
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, false
	}
	hosts, err := parseFleet(l.Fleet)
	if err != nil {
		return nil, false
	}
	l.Flake.Path = l.Path
	return &Inventory{Flake: l.Flake, Hosts: hosts}, true
}

// cachePath is where the fleet for a locked URL is kept, or "" if it
// shouldn't be cached.
func cachePath(locked string) string {
	if locked == "" {
		return ""
	}
	return cacheFile(locked, "")
}

// lastPath is where the fleet last loaded for ref is kept.
func lastPath(ref string) string {
	if ref == "" {
		return ""
	}
	return cacheFile(ref, "last-")
}

func cacheFile(key, prefix string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(base, "careen", "inventory", prefix+hex.EncodeToString(sum[:])+".json")
}

func readCache(locked string) ([]byte, bool) {
	path := cachePath(locked)
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	return b, err == nil
}

// writeCache is best effort: a failure only costs the next run an eval.
func writeCache(path string, out []byte) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err == nil {
		os.Rename(tmp, path)
	}
}

// Selector picks hosts by name and tag. The zero Selector picks every host.
type Selector struct {
	Only   []string
	Except []string
	Tags   []string // a host matches if it has any of them
}

// Select returns the hosts sel picks, in inventory order. Naming a host
// that isn't in the inventory is a usage error.
func (inv *Inventory) Select(sel Selector) ([]Host, error) {
	for _, name := range slices.Concat(sel.Only, sel.Except) {
		if !slices.ContainsFunc(inv.Hosts, func(h Host) bool { return h.Name == name }) {
			return nil, contract.Usage("unknown host %q (known: %s)", name, strings.Join(inv.names(), ", "))
		}
	}
	var out []Host
	for _, h := range inv.Hosts {
		if len(sel.Only) > 0 && !slices.Contains(sel.Only, h.Name) {
			continue
		}
		if slices.Contains(sel.Except, h.Name) {
			continue
		}
		if len(sel.Tags) > 0 && !slices.ContainsFunc(sel.Tags, func(t string) bool { return slices.Contains(h.Tags, t) }) {
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

func (inv *Inventory) names() []string {
	names := make([]string, len(inv.Hosts))
	for i, h := range inv.Hosts {
		names[i] = h.Name
	}
	return names
}
