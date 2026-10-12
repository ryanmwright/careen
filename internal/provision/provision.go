// Package provision is the seam between careen's core and whatever makes a
// machine exist. A Provisioner gets a host from the inventory and hands
// back a running machine; configuring the guest (host keys, deploys) stays
// in careen's core, which never learns where the machine came from.
//
// "existing" is today's behaviour: the machine is already there. The
// Proxmox provisioner (package proxmox) creates VMs from the flake's pve
// inventory. Others (nixos-anywhere, say) fit the same interface.
package provision

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/inventory"
)

// Machine is what Ensure hands back.
type Machine struct {
	// Address is where careen reaches the machine over SSH.
	Address string `json:"address"`
	// BootHostKey is the SSH host key the machine has right now, as
	// "ssh-ed25519 AAAA...", read out of band where the provisioner can
	// (Proxmox: the QEMU guest agent). careen up pins it for its first
	// connection. Empty if unknown.
	BootHostKey string `json:"bootHostKey,omitempty"`
	// Created is set when Ensure made the machine just now.
	Created bool `json:"created"`
}

// Change actions, per host.
const (
	Create  = "create"
	Update  = "update"
	Replace = "replace"
	Delete  = "delete"
	NoOp    = "no-op"
)

// Change is what a plan does to one host's machine.
type Change struct {
	Host   string `json:"host"`
	Action string `json:"action"`
}

// Plan is what Apply or Destroy would do.
type Plan struct {
	Provisioner string   `json:"provisioner"`
	Changes     []Change `json:"changes"`
}

// Pending reports whether the plan changes anything.
func (p Plan) Pending() bool {
	for _, c := range p.Changes {
		if c.Action != NoOp {
			return true
		}
	}
	return false
}

// Progress reports a step for a host.
type Progress func(host, step, msg string)

type Provisioner interface {
	Name() string
	// Plan shows what Apply (or, with destroy, Destroy) would do to hosts'
	// machines. It changes nothing.
	Plan(ctx context.Context, hosts []inventory.Host, destroy bool) (Plan, error)
	// Apply makes hosts' machines match the inventory (create, resize,
	// start), without waiting for them to boot.
	Apply(ctx context.Context, hosts []inventory.Host, progress Progress) (Plan, error)
	// Ensure makes h's machine exist and run, and waits until it answers.
	Ensure(ctx context.Context, h inventory.Host, progress Progress) (Machine, error)
	// Destroy removes hosts' machines, disks included.
	Destroy(ctx context.Context, hosts []inventory.Host, progress Progress) (Plan, error)
}

// Restarter is a provisioner that can restart a machine from outside it.
// Some hardware changes (a disk's IO thread, the boot order) wait for that:
// a reboot inside the guest keeps the same virtual machine running, so they
// never apply.
type Restarter interface {
	// PendingRestart lists the changes waiting for a restart from outside;
	// empty when there are none.
	PendingRestart(ctx context.Context, h inventory.Host) ([]string, error)
	// Restart restarts h's machine from outside, applying them, and
	// returns once it is starting again.
	Restart(ctx context.Context, h inventory.Host) error
}

// Existing is the provisioner for machines that are already there: it
// only checks that the host answers and reads its key.
type Existing struct {
	// Scan reads a host's key line; agekey.ScanLine when nil.
	Scan func(ctx context.Context, address string, port int) (string, error)
}

func (Existing) Name() string { return inventory.Existing }

func (e Existing) Plan(_ context.Context, hosts []inventory.Host, destroy bool) (Plan, error) {
	if destroy {
		return Plan{}, fmt.Errorf("careen can't remove %s: its machine isn't provisioned by careen", names(hosts))
	}
	p := Plan{Provisioner: inventory.Existing}
	for _, h := range hosts {
		p.Changes = append(p.Changes, Change{Host: h.Name, Action: NoOp})
	}
	return p, nil
}

func (e Existing) Apply(ctx context.Context, hosts []inventory.Host, _ Progress) (Plan, error) {
	return e.Plan(ctx, hosts, false)
}

func (e Existing) Ensure(ctx context.Context, h inventory.Host, progress Progress) (Machine, error) {
	scan := e.Scan
	if scan == nil {
		scan = agekey.ScanLine
	}
	progress(h.Name, "provision", "existing machine; reading its host key from "+h.Address)
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	line, err := scan(sctx, h.Address, 0)
	if err != nil {
		return Machine{}, err
	}
	key := line
	if f := strings.Fields(line); len(f) >= 3 { // "host ssh-ed25519 AAAA"
		key = f[1] + " " + f[2]
	}
	return Machine{Address: h.Address, BootHostKey: key}, nil
}

func (e Existing) Destroy(ctx context.Context, hosts []inventory.Host, _ Progress) (Plan, error) {
	return e.Plan(ctx, hosts, true)
}

func names(hosts []inventory.Host) string {
	n := make([]string, len(hosts))
	for i, h := range hosts {
		n[i] = h.Name
	}
	return strings.Join(n, ", ")
}
