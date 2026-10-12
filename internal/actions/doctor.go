package actions

import (
	"context"
	"errors"
	"os/exec"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
	"github.com/ryanmwright/careen/internal/repo"
)

const DoctorSchema = "careen.doctor/v1"

const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
)

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type DoctorReport struct {
	contract.Header
	OK     bool    `json:"ok"` // no check failed
	Checks []Check `json:"checks"`
}

// Doctor checks what careen needs: its tools, the flake, the writable
// checkout and its settings, the master key, and SSH to every host.
func Doctor(ctx context.Context, flake, repoDir string) DoctorReport {
	var checks []Check
	add := func(name, status, detail string) { checks = append(checks, Check{name, status, detail}) }

	for _, t := range []struct {
		name     string
		required bool
		why      string
	}{
		{"nix", true, "builds and evaluation"},
		{"ssh", true, "reaching hosts"},
		{"ssh-keyscan", true, "host keys"},
		{"git", true, "the checkout"},
		{"sops", true, "secrets"},
		{"gpg", false, "PGP master keys"},
		{"age-keygen", false, "checking age master keys"},
		{"vulnix", false, "careen vulns"},
	} {
		if p, err := exec.LookPath(t.name); err == nil {
			add("tool "+t.name, CheckOK, p)
		} else if t.required {
			add("tool "+t.name, CheckFail, "not installed; needed for "+t.why)
		} else {
			add("tool "+t.name, CheckWarn, "not installed; needed for "+t.why)
		}
	}

	inv, err := inventory.Load(ctx, execx.Run, flake, false)
	if err != nil {
		add("flake", CheckFail, err.Error())
	} else {
		add("flake", CheckOK, inv.Flake.Ref+" @ "+inv.Flake.Revision)
		for _, o := range probe.New().ProbeAll(ctx, inv.Hosts) {
			if o.Err != nil {
				add("ssh "+o.Host.Name, CheckWarn, o.Err.Message)
			} else {
				add("ssh "+o.Host.Name, CheckOK, o.Host.Address)
			}
		}
	}

	dir, err := repo.Find(repoDir)
	if err != nil {
		add("checkout", CheckWarn, err.Error()+" (needed for add, keys and secrets)")
	} else if rp, err := repo.Open(ctx, execx.Run, dir); errors.Is(err, repo.ErrNoSettings) {
		add("checkout", CheckWarn, dir+": "+err.Error())
	} else if err != nil {
		add("checkout", CheckFail, err.Error())
	} else {
		add("checkout", CheckOK, rp.Dir)
		if _, err := rp.Sops(); err != nil {
			add("sops config", CheckFail, err.Error())
		} else {
			add("sops config", CheckOK, rp.Settings.SopsConfig)
		}
		if err := MasterKey(ctx, rp); err != nil {
			add("master key", CheckWarn, contract.ErrorObject(err).Message)
		} else {
			add("master key", CheckOK, "available")
		}
	}

	rep := DoctorReport{Header: contract.NewHeader(DoctorSchema), OK: true, Checks: checks}
	for _, c := range checks {
		if c.Status == CheckFail {
			rep.OK = false
		}
	}
	return rep
}
