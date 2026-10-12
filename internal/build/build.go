// Package build builds a host's system closure and gets it into the host's
// store. Where it builds is the host's BuildOn:
//
//	local   nix build here, then nix copy to the host
//	remote  nix build on the builder's store, then copy builder → host
//	target  nix build directly in the host's store; nothing to copy
//
// Evaluation always happens here (--eval-store auto). Built paths are
// unsigned, so copies pass --no-check-sigs; the destination accepts that
// because careen's user is a trusted Nix user there.
package build

import (
	"context"
	"fmt"
	"strings"

	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
)

const (
	Local  = "local"
	Remote = "remote"
	Target = "target"
)

// Modes are the valid BuildOn values.
var Modes = []string{Local, Remote, Target}

// Job is one host's build.
type Job struct {
	Host    inventory.Host
	Mode    string
	Builder string // store URI, for Remote
	// Dest is the host's store URI, or "" when the host is this machine.
	Dest string
}

// HostStore is the store URI careen copies to for h.
func HostStore(h inventory.Host) string {
	if h.User != "" {
		return "ssh-ng://" + h.User + "@" + h.Address
	}
	return "ssh-ng://" + h.Address
}

// Toplevel is the installable for h's system.
func Toplevel(ref, host string) string {
	return ref + "#nixosConfigurations." + host + ".config.system.build.toplevel"
}

// Build builds j from ref and copies it into the host's store. It returns
// the system's store path. progress reports each step.
func Build(ctx context.Context, run execx.Runner, ref string, j Job, progress func(step, msg string)) (string, error) {
	installable := Toplevel(ref, j.Host.Name)
	mode := j.Mode
	if mode == Target && j.Dest == "" {
		mode = Local // building "on the target" when the target is here
	}

	var buildArgs, copyArgs []string
	where := ""
	switch mode {
	case Local:
		where = "here"
		buildArgs = []string{"build", "--no-link", "--print-out-paths", installable}
		if j.Dest != "" {
			copyArgs = []string{"copy", "--no-check-sigs", "--substitute-on-destination", "--to", j.Dest}
		}
	case Remote:
		if j.Builder == "" {
			return "", fmt.Errorf("buildOn is remote but no builder is set")
		}
		where = "on " + j.Builder
		buildArgs = []string{"build", "--eval-store", "auto", "--store", j.Builder, "--no-link", "--print-out-paths", installable}
		copyArgs = []string{"copy", "--no-check-sigs", "--substitute-on-destination", "--from", j.Builder}
		if j.Dest != "" {
			copyArgs = append(copyArgs, "--to", j.Dest)
		}
	case Target:
		where = "on the host"
		buildArgs = []string{"build", "--eval-store", "auto", "--store", j.Dest, "--no-link", "--print-out-paths", installable}
	default:
		return "", fmt.Errorf("unknown build mode %q (want %s)", j.Mode, strings.Join(Modes, ", "))
	}

	progress("build", "building "+where)
	out, err := run(ctx, "nix", buildArgs...)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if !strings.HasPrefix(path, "/nix/store/") || strings.Contains(path, "\n") {
		return "", fmt.Errorf("build: unexpected output %q", path)
	}
	if copyArgs != nil {
		progress("copy", "copying "+path)
		if _, err := run(ctx, "nix", append(copyArgs, path)...); err != nil {
			return "", fmt.Errorf("copy: %w", err)
		}
	}
	return path, nil
}
