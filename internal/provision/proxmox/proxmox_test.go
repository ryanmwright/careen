package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/hostkey"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/provision"
)

// Real keys: careen compares them, so they must parse.
var bootKey, oldKey, newKey = genKey(), genKey(), genKey()

func genKey() string {
	k, err := hostkey.Generate("")
	if err != nil {
		panic(err)
	}
	return k.Public
}

func TestParsePlan(t *testing.T) {
	b, err := os.ReadFile("testdata/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePlan(b)
	if err != nil {
		t.Fatal(err)
	}
	want := []provision.Change{
		{Host: "cp1", Action: provision.Create},
		{Host: "cp2", Action: provision.Replace},
		{Host: "cp3", Action: provision.NoOp},
		{Host: "old", Action: provision.Delete},
		{Host: "worker1", Action: provision.Update},
	}
	if !slices.Equal(p.Changes, want) {
		t.Errorf("changes = %v\nwant %v", p.Changes, want)
	}
	if !p.Pending() {
		t.Error("plan should be pending")
	}
}

// fakeEnv is a Proxmox with nix, tofu and ssh faked: tofu's plan is
// whatever showJSON says, and calls are recorded.
type fakeEnv struct {
	mu       sync.Mutex
	calls    []string
	envs     [][]string
	showJSON string
	stdin    []string
	// pending is what pvesh reports for a VM's pending changes.
	pending string
	// tfjson is the rendered OpenTofu config; no resources when empty.
	tfjson string
	// guestAgent is the pve entry's guestAgent; nil leaves it out.
	guestAgent *bool
	// planDelay is how long tofu plan takes.
	planDelay time.Duration
	// qemuGuest is what evaluating a host's services.qemuGuest.enable
	// prints; empty fails the eval.
	qemuGuest string
}

func (f *fakeEnv) provisioner(t *testing.T, api *httptest.Server) *Proxmox {
	t.Helper()
	store := t.TempDir()
	tfjson := f.tfjson
	if tfjson == "" {
		tfjson = `{"resource":{}}`
	}
	os.WriteFile(filepath.Join(store, "config.tf.json"), []byte(tfjson), 0o644)
	os.MkdirAll(filepath.Join(store, "tofu", "bin"), 0o755)
	cfg := Config{Endpoint: "https://pve:8006/", Storage: "local-lvm", Nodes: map[string]Node{"pve1": {Address: "10.0.0.1", Template: 9000}},
		Hosts: map[string]VM{"node1": {Node: "pve1", VMID: 131, GuestAgent: f.guestAgent}}}
	cfg.Token.File, cfg.Token.Key = "secrets/pve/secrets.yaml", "proxmox_api_token"
	if api != nil {
		cfg.Endpoint = api.URL
	}
	cfgJSON, _ := json.Marshal(cfg)
	p := &Proxmox{
		Flake: inventory.Flake{Ref: "flake", Locked: "flake?rev=abc", Path: "/store/src", Revision: "abcdef123456"},
		Dir:   t.TempDir(),
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			f.record(name+" "+strings.Join(args, " "), nil, "")
			switch {
			case slices.Contains(args, "eval") && strings.HasSuffix(args[len(args)-1], ".config.services.qemuGuest.enable"):
				if f.qemuGuest == "" {
					return nil, fmt.Errorf("no such attribute")
				}
				return []byte(f.qemuGuest), nil
			case slices.Contains(args, "eval"):
				return cfgJSON, nil
			case slices.Contains(args, "flake?rev=abc#pve-tofu"):
				return []byte(filepath.Join(store, "config.tf.json") + "\n" + filepath.Join(store, "tofu") + "\n"), nil
			case len(args) > 0 && args[0] == "build":
				os.MkdirAll(filepath.Join(store, "img"), 0o755)
				os.WriteFile(filepath.Join(store, "img", "vzdump-qemu-base.vma.zst"), []byte("IMAGE"), 0o644)
				return []byte(filepath.Join(store, "img") + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected nix %v", args)
		},
		Cmd: func(_ context.Context, dir string, env []string, stdin io.Reader, name string, args ...string) ([]byte, error) {
			in := ""
			if stdin != nil {
				b, _ := io.ReadAll(stdin)
				in = string(b)
			}
			f.record(filepath.Base(name)+" "+strings.Join(args, " "), env, in)
			if filepath.Base(name) == "tofu" && args[0] == "plan" {
				time.Sleep(f.planDelay)
			}
			if filepath.Base(name) == "tofu" && args[0] == "show" {
				return []byte(f.showJSON), nil
			}
			if name == "ssh" && strings.Contains(args[len(args)-1], "qm status") {
				return []byte("no\n"), nil
			}
			if name == "ssh" && strings.Contains(args[len(args)-1], "pvesh get") {
				return []byte(f.pending), nil
			}
			return nil, nil
		},
		Token: func(_ context.Context, file, key string) (string, error) {
			if file != "/store/src/secrets/pve/secrets.yaml" || key != "proxmox_api_token" {
				return "", fmt.Errorf("token from %s %s", file, key)
			}
			return "tofu@pve!careen=uuid\n", nil
		},
		Poll: time.Millisecond, AgentTimeout: 5 * time.Second,
		Scan: func(_ context.Context, address string, _ int) (string, error) {
			return address + " " + bootKey, nil
		},
	}
	return p
}

func (f *fakeEnv) record(call string, env []string, stdin string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.envs = append(f.envs, env)
	if stdin != "" {
		f.stdin = append(f.stdin, stdin)
	}
}

func (f *fakeEnv) has(sub string) bool {
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.Contains(c, sub) })
}

// agentServer answers like Proxmox: the agent isn't running for the first
// few pings, then the key file appears.
func agentServer(t *testing.T, notYet int, status int) *httptest.Server {
	var mu sync.Mutex
	pings := 0
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=tofu@pve!careen=uuid" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		if status != 0 {
			http.Error(w, "permission denied", status)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/nodes/pve1/qemu/131/agent/ping") && r.Method == http.MethodPost:
			pings++
			if pings <= notYet {
				http.Error(w, `{"data":null,"message":"QEMU guest agent is not running"}`, http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"data":{}}`)
		case strings.HasSuffix(r.URL.Path, "/nodes/pve1/qemu/131/agent/file-read"):
			if r.URL.Query().Get("file") != HostKeyPath {
				http.Error(w, "wrong file", http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"data":{"content":"%s root@pve-base\n","bytes-read":42}}`, bootKey)
		default:
			http.NotFound(w, r)
		}
	}))
}

const createPlan = `{"resource_changes":[{"type":"proxmox_virtual_environment_vm","name":"node1","change":{"actions":["create"]}}]}`

func TestEnsureCreates(t *testing.T) {
	api := agentServer(t, 3, 0)
	defer api.Close()
	f := &fakeEnv{showJSON: createPlan}
	p := f.provisioner(t, api)
	var steps []string
	m, err := p.Ensure(context.Background(), inventory.Host{Name: "node1", Address: "10.0.0.31"}, func(_, _, msg string) { steps = append(steps, msg) })
	if err != nil {
		t.Fatal(err)
	}
	if m.Address != "10.0.0.31" || m.BootHostKey != bootKey || !m.Created {
		t.Errorf("machine = %+v", m)
	}
	for _, want := range []string{"tofu init -input=false", "tofu plan -input=false -out=careen.tfplan -target=proxmox_virtual_environment_vm.node1", "tofu show -json careen.tfplan", "tofu apply -input=false careen.tfplan"} {
		if !f.has(want) {
			t.Errorf("missing call %q in %v", want, f.calls)
		}
	}
	// The token goes to plan and apply in the environment, never on a command line.
	for i, c := range f.calls {
		if strings.Contains(c, "uuid") {
			t.Errorf("token on a command line: %s", c)
		}
		if strings.HasPrefix(c, "tofu apply") && !slices.Contains(f.envs[i], "PROXMOX_VE_API_TOKEN=tofu@pve!careen=uuid") {
			t.Errorf("apply env = %v", f.envs[i])
		}
	}
	if b, err := os.ReadFile(filepath.Join(p.Dir, "main.tf.json")); err != nil || string(b) != `{"resource":{}}` {
		t.Errorf("rendered config = %q, %v", b, err)
	}
}

func TestPlanSaysItStillRuns(t *testing.T) {
	api := agentServer(t, 0, 0)
	defer api.Close()
	f := &fakeEnv{showJSON: createPlan, planDelay: 50 * time.Millisecond}
	p := f.provisioner(t, api)
	p.PlanNotice = 20 * time.Millisecond
	var mu sync.Mutex
	var steps []string
	_, err := p.Ensure(context.Background(), inventory.Host{Name: "node1", Address: "10.0.0.31"}, func(_, _, msg string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, msg)
	})
	if err != nil {
		t.Fatal(err)
	}
	said := strings.Join(steps, "\n")
	if !strings.HasPrefix(said, "planning with OpenTofu\ntofu plan still running") || !strings.Contains(said, "guest agent doesn't answer") {
		t.Errorf("progress:\n%s", said)
	}

	// Plan, for the preview before confirming, writes to Notice.
	var notice strings.Builder
	p.Notice = &notice
	if _, err := p.Plan(context.Background(), []inventory.Host{{Name: "node1"}}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(notice.String(), "    node1: planning with OpenTofu\n    node1: tofu plan still running") {
		t.Errorf("notice:\n%s", notice.String())
	}
}

func TestEnsureRefusesReplace(t *testing.T) {
	f := &fakeEnv{showJSON: `{"resource_changes":[{"type":"proxmox_virtual_environment_vm","name":"node1","change":{"actions":["delete","create"]}}]}`}
	p := f.provisioner(t, nil)
	_, err := p.Ensure(context.Background(), inventory.Host{Name: "node1"}, func(_, _, _ string) {})
	if !errors.Is(err, ErrReplace) {
		t.Fatalf("err = %v, want ErrReplace", err)
	}
	if f.has("tofu apply") {
		t.Error("applied a replacing plan")
	}
}

func TestEnsureUnknownHost(t *testing.T) {
	f := &fakeEnv{showJSON: createPlan}
	p := f.provisioner(t, nil)
	if _, err := p.Ensure(context.Background(), inventory.Host{Name: "nope"}, func(_, _, _ string) {}); err == nil || !strings.Contains(err.Error(), "pve.hosts.nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitHostKeyForbidden(t *testing.T) {
	api := agentServer(t, 0, http.StatusForbidden)
	defer api.Close()
	a := NewAPI(api.URL, "tofu@pve!careen=uuid")
	_, err := a.WaitHostKey(context.Background(), "pve1", 131, time.Millisecond, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "VM.GuestAgent") {
		t.Fatalf("err = %v", err)
	}
}

func TestDestroyNoChange(t *testing.T) {
	f := &fakeEnv{showJSON: `{"resource_changes":[]}`}
	p := f.provisioner(t, nil)
	pl, err := p.Destroy(context.Background(), []inventory.Host{{Name: "node1"}}, func(_, _, _ string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !f.has("plan -input=false -out=careen.tfplan -destroy") || f.has("tofu apply") {
		t.Errorf("calls = %v", f.calls)
	}
	if !slices.Equal(pl.Changes, []provision.Change{{Host: "node1", Action: provision.NoOp}}) {
		t.Errorf("changes = %v", pl.Changes)
	}
}

func deletePlan(vmid int, node string) string {
	return fmt.Sprintf(`{"resource_changes":[{"type":"proxmox_virtual_environment_vm","name":"node1","change":{"actions":["delete"],"before":{"vm_id":%d,"node_name":%q}}}]}`, vmid, node)
}

func TestDestroyOwnVM(t *testing.T) {
	f := &fakeEnv{showJSON: deletePlan(131, "pve1")}
	p := f.provisioner(t, nil)
	if _, err := p.Destroy(context.Background(), []inventory.Host{{Name: "node1"}}, func(_, _, _ string) {}); err != nil {
		t.Fatal(err)
	}
	if !f.has("tofu apply") {
		t.Errorf("didn't apply: %v", f.calls)
	}
}

func TestDestroyRefusesAnotherVM(t *testing.T) {
	for _, plan := range []string{deletePlan(132, "pve1"), deletePlan(131, "pve2")} {
		f := &fakeEnv{showJSON: plan}
		p := f.provisioner(t, nil)
		_, err := p.Destroy(context.Background(), []inventory.Host{{Name: "node1"}}, func(_, _, _ string) {})
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("%s: err = %v", plan, err)
		}
		if f.has("tofu apply") {
			t.Errorf("%s: applied", plan)
		}
	}
}

func TestBuildTemplates(t *testing.T) {
	f := &fakeEnv{}
	p := f.provisioner(t, nil)
	res, err := p.BuildTemplates(context.Background(), nil, false, func(_, _, _ string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != TemplateCreated || res[0].Name != "pve-base-abcdef1" || res[0].VMID != 9000 {
		t.Errorf("results = %+v", res)
	}
	if !f.has("ssh -o BatchMode=yes -o ConnectTimeout=10 -l root -- 10.0.0.1 zstd -d | qmrestore - 9000 --storage local-lvm --unique 1 && qm set 9000 --name pve-base-abcdef1 && qm template 9000") {
		t.Errorf("calls = %v", f.calls)
	}
	if !slices.Equal(f.stdin, []string{"IMAGE"}) {
		t.Errorf("stdin = %v", f.stdin)
	}
}

// sshd keeps serving a key that's being replaced for a while: careen must
// not pin the first key the agent reported until sshd serves it.
func TestEnsureWaitsForSettledKey(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/ping"):
			fmt.Fprint(w, `{"data":{}}`)
		case strings.HasSuffix(r.URL.Path, "/agent/file-read"):
			reads++
			key := oldKey
			if reads > 2 {
				key = newKey
			}
			fmt.Fprintf(w, `{"data":{"content":"%s root@x\n"}}`, key)
		}
	}))
	defer api.Close()
	f := &fakeEnv{showJSON: createPlan}
	p := f.provisioner(t, api)
	scans := 0
	p.Scan = func(_ context.Context, address string, _ int) (string, error) {
		scans++
		return address + " " + newKey, nil
	}
	m, err := p.Ensure(context.Background(), inventory.Host{Name: "node1", Address: "10.0.0.31"}, func(_, _, _ string) {})
	if err != nil {
		t.Fatal(err)
	}
	if m.BootHostKey != newKey || scans < 2 {
		t.Errorf("boot key %q after %d scans; want the settled key", m.BootHostKey, scans)
	}
}

// sshd loaded its keys before they were replaced and serves the old one
// forever: careen reboots the VM once, after which sshd serves the file's.
func TestEnsureRebootsStuckSSHD(t *testing.T) {
	var mu sync.Mutex
	rebooted := false
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/agent/ping"):
			fmt.Fprint(w, `{"data":{}}`)
		case strings.HasSuffix(r.URL.Path, "/agent/file-read"):
			fmt.Fprintf(w, `{"data":{"content":"%s root@x\n"}}`, newKey)
		case strings.HasSuffix(r.URL.Path, "/nodes/pve1/qemu/131/status/reboot") && r.Method == http.MethodPost:
			rebooted = true
			fmt.Fprint(w, `{"data":"UPID:..."}`)
		}
	}))
	defer api.Close()
	f := &fakeEnv{showJSON: createPlan}
	p := f.provisioner(t, api)
	p.StuckAfter = 5 * time.Millisecond
	p.Scan = func(_ context.Context, address string, _ int) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if rebooted {
			return address + " " + newKey, nil
		}
		return address + " " + oldKey, nil
	}
	m, err := p.Ensure(context.Background(), inventory.Host{Name: "node1", Address: "10.0.0.31"}, func(_, _, _ string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !rebooted || m.BootHostKey != newKey {
		t.Errorf("rebooted=%v key=%q", rebooted, m.BootHostKey)
	}
}
