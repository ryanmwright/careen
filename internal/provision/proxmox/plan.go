package proxmox

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/provision"
)

// Resource is the OpenTofu resource type each host's VM is.
const Resource = "proxmox_virtual_environment_vm"

// ParsePlan reads `tofu show -json <plan>` into per-host changes. Resource
// names are host names.
func ParsePlan(b []byte) (provision.Plan, error) {
	var doc struct {
		ResourceChanges []struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			Change struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return provision.Plan{}, fmt.Errorf("parsing the OpenTofu plan: %w", err)
	}
	p := provision.Plan{Provisioner: inventory.Proxmox, Changes: []provision.Change{}}
	for _, rc := range doc.ResourceChanges {
		if rc.Type != Resource {
			continue
		}
		p.Changes = append(p.Changes, provision.Change{Host: rc.Name, Action: action(rc.Change.Actions)})
	}
	slices.SortFunc(p.Changes, func(a, b provision.Change) int {
		switch {
		case a.Host < b.Host:
			return -1
		case a.Host > b.Host:
			return 1
		}
		return 0
	})
	return p, nil
}

// CheckDeletes refuses a plan that deletes a VM other than the one the
// flake declares for that host. OpenTofu's state is per machine, not per
// flake, and resources are named by host, so a host named like one of
// another flake's would otherwise destroy that flake's VM.
func CheckDeletes(b []byte, hosts map[string]VM) error {
	var doc struct {
		ResourceChanges []struct {
			Type   string `json:"type"`
			Name   string `json:"name"`
			Change struct {
				Actions []string `json:"actions"`
				Before  *struct {
					VMID     int    `json:"vm_id"`
					NodeName string `json:"node_name"`
				} `json:"before"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("parsing the OpenTofu plan: %w", err)
	}
	for _, rc := range doc.ResourceChanges {
		if rc.Type != Resource || !slices.Contains(rc.Change.Actions, "delete") || rc.Change.Before == nil {
			continue
		}
		want, ok := hosts[rc.Name]
		got := rc.Change.Before
		if ok && got.VMID == want.VMID && got.NodeName == want.Node {
			continue
		}
		return fmt.Errorf("%s: the plan deletes VM %d on %s, but the flake declares VM %d on %s; refusing. "+
			"The VM is another flake's (OpenTofu's state is shared by every flake on this machine), "+
			"or its VM ID or node changed in the flake: to remove it, change them back and run careen down first",
			rc.Name, got.VMID, got.NodeName, want.VMID, want.Node)
	}
	return nil
}

func action(actions []string) string {
	switch {
	case slices.Contains(actions, "create") && slices.Contains(actions, "delete"):
		return provision.Replace
	case slices.Contains(actions, "create"):
		return provision.Create
	case slices.Contains(actions, "delete"):
		return provision.Delete
	case slices.Contains(actions, "update"):
		return provision.Update
	}
	return provision.NoOp
}
