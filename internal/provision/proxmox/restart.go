package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/provision"
)

var _ provision.Restarter = (*Proxmox)(nil)

// PendingRestart lists the VM settings Proxmox holds back until the VM is
// restarted from outside, such as an OpenTofu change applied with
// reboot_after_update off. It asks the VM's node over SSH, like template
// build, so it needs no API token.
func (p *Proxmox) PendingRestart(ctx context.Context, h inventory.Host) ([]string, error) {
	vm, node, err := p.vmNode(ctx, h)
	if err != nil {
		return nil, err
	}
	out, err := p.ssh(ctx, node.Address, nil, fmt.Sprintf("pvesh get /nodes/%s/qemu/%d/pending --output-format json", vm.Node, vm.VMID))
	if err != nil {
		return nil, fmt.Errorf("reading VM %d's pending changes: %w", vm.VMID, err)
	}
	return parsePending(out)
}

// Restart restarts the VM from its node: a shutdown and a fresh start,
// which applies the pending changes. careen then waits for the host to
// come back as after any reboot.
//
// With a guest agent that's qm reboot. Proxmox shuts down a VM whose agent
// option is on through the agent, so for a host whose config doesn't run
// one, qm reboot would hang until Proxmox gives up; it gets an ACPI power
// off from the VM's monitor instead, and is started once it has stopped.
func (p *Proxmox) Restart(ctx context.Context, h inventory.Host) error {
	vm, node, err := p.vmNode(ctx, h)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("qm reboot %d", vm.VMID)
	if !p.hasAgent(ctx, h) {
		cmd = powerCycle(vm)
	}
	if _, err := p.ssh(ctx, node.Address, nil, cmd); err != nil {
		return fmt.Errorf("restarting VM %d: %w", vm.VMID, err)
	}
	return nil
}

// powerCycleTimeout is how long powerCycle waits for the guest to power
// off.
const powerCycleTimeout = 3 * time.Minute

// powerCycle is the node's script restarting vm without its guest agent:
// an ACPI power off, then a start once the VM has stopped.
func powerCycle(vm VM) string {
	tries := int(powerCycleTimeout / (2 * time.Second))
	return fmt.Sprintf(`pvesh create /nodes/%[1]s/qemu/%[2]d/monitor --command system_powerdown >/dev/null && `+
		`for i in $(seq %[3]d); do if qm status %[2]d | grep -q stopped; then exec qm start %[2]d; fi; sleep 2; done; `+
		`echo "VM %[2]d didn't power off within %[4]s (does its system handle the ACPI power button?)" >&2; exit 1`,
		vm.Node, vm.VMID, tries, powerCycleTimeout)
}

// vmNode is h's VM and the Proxmox node it lives on.
func (p *Proxmox) vmNode(ctx context.Context, h inventory.Host) (VM, Node, error) {
	cfg, err := p.Config(ctx)
	if err != nil {
		return VM{}, Node{}, err
	}
	vm, ok := cfg.Hosts[h.Name]
	if !ok {
		return VM{}, Node{}, fmt.Errorf("%s has no VM in the flake's pve output (pve.hosts.%s)", h.Name, h.Name)
	}
	node, ok := cfg.Nodes[vm.Node]
	if !ok {
		return VM{}, Node{}, fmt.Errorf("%s: Proxmox node %q isn't in the flake's pve.nodes", h.Name, vm.Node)
	}
	return vm, node, nil
}

// parsePending reads `pvesh get .../pending`: every setting, with a
// "pending" value when a new one waits and a non-zero "delete" when its
// removal does.
func parsePending(out []byte) ([]string, error) {
	var entries []struct {
		Key     string          `json:"key"`
		Pending json.RawMessage `json:"pending"`
		Delete  json.RawMessage `json:"delete"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parsing the VM's pending changes: %w", err)
	}
	set := func(r json.RawMessage) bool {
		s := string(r)
		return s != "" && s != "null" && s != "0" && s != `"0"`
	}
	keys := []string{}
	for _, e := range entries {
		if set(e.Pending) || set(e.Delete) {
			keys = append(keys, e.Key)
		}
	}
	slices.Sort(keys)
	return keys, nil
}
