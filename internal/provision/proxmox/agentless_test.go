package proxmox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/inventory"
)

const noChange = `{"resource_changes":[]}`

func ptr[T any](v T) *T { return &v }

var node1 = inventory.Host{Name: "node1", Address: "10.0.0.31", User: "root"}

// existingVM is a provisioner for a VM that already exists. probe is what
// the guest says about its agent unit; every remote command is recorded.
func existingVM(t *testing.T, guestAgent *bool, probe string, api *httptest.Server) (*Proxmox, *[]string) {
	t.Helper()
	f := &fakeEnv{showJSON: noChange, guestAgent: guestAgent}
	p := f.provisioner(t, api)
	p.TrustedKeys = func(inventory.Host) []string { return []string{bootKey} }
	inner := p.Cmd
	var remote []string
	p.Cmd = func(ctx context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
		if name == "ssh" {
			cmd := args[len(args)-1]
			remote = append(remote, cmd)
			if strings.Contains(cmd, "systemctl cat") {
				return []byte(probe + "\n"), nil
			}
		}
		return inner(ctx, dir, env, stdin, name, args...)
	}
	return p, &remote
}

func ensure(p *Proxmox) (string, string, string) {
	var said []string
	m, err := p.Ensure(context.Background(), node1, func(_, _, msg string) { said = append(said, msg) })
	e := ""
	if err != nil {
		e = err.Error()
	}
	return m.BootHostKey, e, strings.Join(said, "\n")
}

func TestEnsureConfigWithoutAgentUsesSSH(t *testing.T) {
	p, _ := existingVM(t, ptr(false), "", nil)
	key, err, said := ensure(p)
	if err != "" || key != bootKey {
		t.Fatalf("key = %q, err = %q", key, err)
	}
	if !strings.Contains(said, "node1's config doesn't run the QEMU guest agent; checking its SSH key") || strings.Contains(said, "waiting") {
		t.Errorf("progress:\n%s", said)
	}
}

// Without guestAgent in the pve entry, careen reads the host's own config.
func TestEnsureReadsQemuGuestFromConfig(t *testing.T) {
	f := &fakeEnv{showJSON: noChange, qemuGuest: "false"}
	p := f.provisioner(t, nil)
	p.TrustedKeys = func(inventory.Host) []string { return []string{bootKey} }
	var said []string
	m, err := p.Ensure(context.Background(), node1, func(_, _, msg string) { said = append(said, msg) })
	if err != nil || m.BootHostKey != bootKey {
		t.Fatalf("m = %+v, err = %v", m, err)
	}
	if !strings.Contains(strings.Join(said, "\n"), "doesn't run the QEMU guest agent") {
		t.Errorf("progress:\n%s", strings.Join(said, "\n"))
	}
	if !f.has("nix eval --json flake?rev=abc#nixosConfigurations.node1.config.services.qemuGuest.enable") {
		t.Errorf("calls: %v", f.calls)
	}
	// Asked once per run, however often it's needed.
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, "qemuGuest") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("evaluated services.qemuGuest.enable %d times", n)
	}
}

// A pve entry's guestAgent wins over the config, without an eval.
func TestHasAgentPrefersGuestAgent(t *testing.T) {
	f := &fakeEnv{guestAgent: ptr(true), qemuGuest: "false"}
	p := f.provisioner(t, nil)
	if _, err := p.Config(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.hasAgent(context.Background(), node1) || f.has("qemuGuest") {
		t.Errorf("hasAgent ignored guestAgent; calls: %v", f.calls)
	}
}

func TestEnsureNoAgentWaitFlag(t *testing.T) {
	p, _ := existingVM(t, nil, "", nil)
	p.NoAgentWait = true
	if key, err, _ := ensure(p); err != "" || key != bootKey {
		t.Fatalf("key = %q, err = %q", key, err)
	}
}

func TestEnsureWithoutAgentRefusesUnknownKey(t *testing.T) {
	p, _ := existingVM(t, ptr(false), "", nil)
	p.TrustedKeys = func(inventory.Host) []string { return []string{oldKey} }
	if _, err, _ := ensure(p); !strings.Contains(err, "can't verify") {
		t.Fatalf("err = %q", err)
	}
	p.TrustedKeys = nil
	if _, err, _ := ensure(p); !strings.Contains(err, "can't verify") {
		t.Fatalf("err without trusted keys = %q", err)
	}
}

func TestEnsureWithoutAgentSSHUnreachable(t *testing.T) {
	p, _ := existingVM(t, ptr(false), "", nil)
	p.Scan = func(context.Context, string, int) (string, error) { return "", io.ErrClosedPipe }
	if _, err, _ := ensure(p); !strings.Contains(err, "unreachable") {
		t.Fatalf("err = %q", err)
	}
}

// A VM careen just made boots the template, which has an agent.
func TestEnsureCreatedIgnoresGuestAgentFalse(t *testing.T) {
	api := agentServer(t, 0, 0)
	defer api.Close()
	f := &fakeEnv{showJSON: createPlan, guestAgent: ptr(false)}
	p := f.provisioner(t, api)
	m, err := p.Ensure(context.Background(), node1, func(_, _, _ string) {})
	if err != nil || m.BootHostKey != bootKey || !m.Created {
		t.Fatalf("m = %+v, err = %v", m, err)
	}
}

// silent is an existing VM whose agent never answers.
func silent(t *testing.T, guestAgent *bool, probe string) (*Proxmox, *[]string) {
	t.Helper()
	api := agentServer(t, 1<<30, 0)
	t.Cleanup(api.Close)
	p, remote := existingVM(t, guestAgent, probe, api)
	p.AgentTimeout = 40 * time.Millisecond
	return p, remote
}

func TestEnsureSilentAgentMissingUnit(t *testing.T) {
	p, _ := silent(t, ptr(true), "missing")
	key, err, said := ensure(p)
	if err != "" || key != bootKey {
		t.Fatalf("key = %q, err = %q", key, err)
	}
	if !strings.Contains(said, "the running system has no guest agent, though node1's config has one") {
		t.Errorf("progress:\n%s", said)
	}
	if !strings.Contains(said, "left)") {
		t.Errorf("no countdown in:\n%s", said)
	}
}

func TestEnsureSilentAgentStartsStoppedUnit(t *testing.T) {
	p, remote := silent(t, nil, "inactive")
	if key, err, _ := ensure(p); err != "" || key != bootKey {
		t.Fatalf("key = %q, err = %q", key, err)
	}
	if !strings.Contains(strings.Join(*remote, "\n"), "systemctl start qemu-guest-agent.service") {
		t.Errorf("ssh calls = %v", *remote)
	}
}

func TestEnsureSilentAgentRunningInGuest(t *testing.T) {
	p, _ := silent(t, ptr(true), "active")
	if _, err, _ := ensure(p); !strings.Contains(err, "Proxmox can't reach it") {
		t.Fatalf("err = %q", err)
	}
}

func TestEnsureSilentAgentSSHUnreachable(t *testing.T) {
	p, _ := silent(t, ptr(true), "missing")
	p.Scan = func(context.Context, string, int) (string, error) { return "", io.ErrClosedPipe }
	if _, err, _ := ensure(p); !strings.Contains(err, "unreachable") || !strings.Contains(err, "guest agent") {
		t.Fatalf("err = %q", err)
	}
}

// A refused token says nothing about the VM; don't go looking over SSH.
func TestEnsureForbiddenDoesNotFallBack(t *testing.T) {
	api := agentServer(t, 0, http.StatusForbidden)
	defer api.Close()
	f := &fakeEnv{showJSON: noChange}
	p := f.provisioner(t, api)
	p.TrustedKeys = func(inventory.Host) []string { return []string{bootKey} }
	if _, err, _ := ensure(p); !strings.Contains(err, "VM.GuestAgent") {
		t.Fatalf("err = %q", err)
	}
}
