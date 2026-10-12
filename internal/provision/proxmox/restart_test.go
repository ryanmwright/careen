package proxmox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanmwright/careen/internal/inventory"
)

// What pvesh reported for a cluster node after a disk change applied with
// reboot_after_update off (trimmed).
const pendingJSON = `[{"key":"boot","pending":"order=virtio0","value":" "},
{"key":"cores","value":2},
{"key":"ide2","value":"local-lvm:131/vm-131-cloudinit.qcow2,media=cdrom"},
{"key":"virtio0","pending":"local-lvm:131/vm-131-disk-0.raw,discard=on,iothread=1,size=20G","value":"local-lvm:131/vm-131-disk-0.raw,discard=ignore,iothread=0,size=20G"},
{"key":"scsi1","delete":1,"value":"local-lvm:131/vm-131-disk-1.raw,size=20G"},
{"key":"net0","delete":0,"value":"virtio=BC:24:11:00:00:E6,bridge=vmbr0"}]`

func TestParsePending(t *testing.T) {
	got, err := parsePending([]byte(pendingJSON))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"boot", "scsi1", "virtio0"}; !slices.Equal(got, want) {
		t.Errorf("pending = %v, want %v", got, want)
	}
	got, err = parsePending([]byte(`[{"key":"cores","value":2}]`))
	if err != nil || len(got) != 0 {
		t.Errorf("nothing pending: got %v, %v", got, err)
	}
	if _, err := parsePending([]byte("not json")); err == nil {
		t.Error("expected an error for output that isn't JSON")
	}
}

func TestPendingRestartAndRestart(t *testing.T) {
	f := &fakeEnv{pending: pendingJSON}
	p := f.provisioner(t, nil)
	h := inventory.Host{Name: "node1", Address: "10.0.1.30"}

	got, err := p.PendingRestart(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("pending = %v", got)
	}
	if !f.has("ssh -o BatchMode=yes -o ConnectTimeout=10 -l root -- 10.0.0.1 pvesh get /nodes/pve1/qemu/131/pending --output-format json") {
		t.Errorf("didn't ask the VM's node; calls: %v", f.calls)
	}

	if err := p.Restart(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if !f.has("-- 10.0.0.1 qm reboot 131") {
		t.Errorf("didn't qm reboot the VM on its node; calls: %v", f.calls)
	}
	// Neither needs the API token.
	if f.has("tofu") {
		t.Errorf("ran tofu: %v", f.calls)
	}

	if _, err := p.PendingRestart(context.Background(), inventory.Host{Name: "nope"}); err == nil {
		t.Error("expected an error for a host without a pve entry")
	}
}

func TestNoRebootOverride(t *testing.T) {
	cfg := `{"resource":{
		"proxmox_virtual_environment_vm":{
			"cp1":{"name":"cp1","reboot_after_update":true},
			"cp2":{"name":"cp2"}},
		"proxmox_virtual_environment_pool":{"cluster":{"pool_id":"cluster"}}}}`
	out, err := noRebootOverride([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Resource map[string]map[string]map[string]any `json:"resource"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("override isn't JSON: %v\n%s", err, out)
	}
	vms := got.Resource[Resource]
	if len(got.Resource) != 1 || len(vms) != 2 {
		t.Fatalf("override should cover exactly the two VMs, got %s", out)
	}
	for name, attrs := range vms {
		if len(attrs) != 1 || attrs["reboot_after_update"] != false {
			t.Errorf("%s: override = %v, want only reboot_after_update = false", name, attrs)
		}
	}

	// No VMs: an empty override, which OpenTofu accepts.
	if out, err := noRebootOverride([]byte(`{"resource":{}}`)); err != nil || strings.TrimSpace(string(out)) != "{}" {
		t.Errorf("no VMs: %s, %v", out, err)
	}
	if _, err := noRebootOverride([]byte("not json")); err == nil {
		t.Error("expected an error for a config that isn't JSON")
	}
}

func TestPrepareWritesNoRebootOverride(t *testing.T) {
	f := &fakeEnv{tfjson: `{"resource":{"proxmox_virtual_environment_vm":{"node1":{"reboot_after_update":true}}}}`}
	p := f.provisioner(t, nil)
	if err := p.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(p.Dir, overrideFile))
	if err != nil {
		t.Fatalf("no override file next to main.tf.json: %v", err)
	}
	if !strings.Contains(string(b), `"node1"`) || !strings.Contains(string(b), `"reboot_after_update": false`) {
		t.Errorf("override = %s", b)
	}
	if !strings.HasSuffix(overrideFile, "_override.tf.json") {
		t.Errorf("%s isn't named as an OpenTofu override file", overrideFile)
	}
}
