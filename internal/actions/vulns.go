package actions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ryanmwright/careen/internal/build"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
	"github.com/ryanmwright/careen/internal/vulns"
)

const (
	VulnsSchema = "careen.vulns/v1"
	// WhitelistFile is the vulnix whitelist at the flake's root.
	WhitelistFile = "vulnix-whitelist.toml"
)

type VulnsOptions struct {
	// Refresh rescans even when a cached report matches.
	Refresh bool
	// MaxAge is how old a cached report may be.
	MaxAge time.Duration
}

// VulnsReport is careen.vulns/v1.
type VulnsReport struct {
	contract.Header
	Flake     inventory.Flake `json:"flake"`
	Whitelist string          `json:"whitelist,omitempty"`
	Hosts     []vulns.Report  `json:"hosts"`
}

// Vulns scans each selected host's running system with vulnix, here, one
// at a time: closures not already in this machine's store are copied in
// first. Reports are cached per host and system. progress reports what
// it's doing; it may be nil.
func Vulns(ctx context.Context, inv *inventory.Inventory, sel inventory.Selector, opts VulnsOptions, progress func(host, msg string)) (VulnsReport, error) {
	if progress == nil {
		progress = func(string, string) {}
	}
	hosts, err := inv.Select(sel)
	if err != nil {
		return VulnsReport{}, err
	}
	rep := VulnsReport{Header: contract.NewHeader(VulnsSchema), Flake: inv.Flake, Hosts: []vulns.Report{}}
	if inv.Flake.Path != "" {
		if wl := filepath.Join(inv.Flake.Path, WhitelistFile); fileExists(wl) {
			rep.Whitelist = wl
		}
	}

	scanned := map[string]vulns.Report{} // by system path
	for _, o := range probe.New().ProbeAll(ctx, InOnlyOrder(hosts, sel.Only)) {
		h := o.Host
		r := vulns.Report{Host: h.Name, Findings: []vulns.Finding{}}
		fail := func(code string, err error) {
			e := contract.ErrorObject(contract.Failed(code, "%v", err))
			e.Host = h.Name
			r.Error = &e
		}
		switch {
		case o.Err != nil:
			r.Error = o.Err
		case o.Result.CurrentSystem == "":
			fail("vulns", errNoSystem)
		default:
			sys := o.Result.CurrentSystem
			r.System = sys
			if c := vulns.Cached(h.Name, sys, opts.MaxAge); c != nil && !opts.Refresh {
				progress(h.Name, "cached scan from "+c.ScannedAt.Local().Format("15:04"))
				r = *c
				break
			}
			if prev, ok := scanned[sys]; ok {
				r.ScannedAt, r.Summary, r.Findings = prev.ScannedAt, prev.Summary, prev.Findings
				vulns.Save(r)
				break
			}
			if !IsLocal(h) && !inLocalStore(ctx, sys) {
				progress(h.Name, "copying its system closure here")
				if _, err := execx.Run(ctx, "nix", "copy", "--no-check-sigs", "--from", build.HostStore(h), sys); err != nil {
					fail("copy", err)
					break
				}
			}
			progress(h.Name, "scanning with vulnix")
			fs, err := vulns.Scan(ctx, sys, rep.Whitelist)
			if err != nil {
				fail("vulnix", err)
				break
			}
			r.ScannedAt, r.Findings, r.Summary = time.Now().UTC().Truncate(time.Second), fs, vulns.Summarize(fs)
			scanned[sys] = r
			vulns.Save(r)
		}
		rep.Hosts = append(rep.Hosts, r)
	}
	return rep, nil
}

var errNoSystem = errors.New("the host reports no current system")

func inLocalStore(ctx context.Context, path string) bool {
	_, err := execx.Run(ctx, "nix", "path-info", path)
	return err == nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
