package actions

import (
	"bytes"
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/sopsfile"
	"github.com/ryanmwright/careen/internal/sshtest"
)

// The test flake: careen settings, and one fake nixosConfiguration per
// directory under hosts/ that git knows about.
const testFlake = `{
  outputs = { self }: {
    careen = {
      hostTemplate = "templates/host.nix.tmpl";
      hostDir = "hosts";
      secretsDir = "secrets/hosts";
      sopsConfig = ".sops.yaml";
      masterKeys = { pgp = [ ]; age = [ "admin" ]; };
      sharedRules = [ "secrets/common/.*\\.yaml$" ];
    };
    nixosConfigurations = builtins.mapAttrs
      (n: _: { config.system.build.toplevel.drvPath = builtins.readFile (./hosts + "/${n}/${n}.nix"); })
      (builtins.readDir ./hosts);
    fleet = builtins.mapAttrs (n: _: { kind = "nixos"; address = "127.0.0.1"; }) (builtins.readDir ./hosts);
  };
}
`

const testTemplate = `# {{.Name}} at {{.Address}}{{if .IP}} ({{.IP}}){{end}}
`

// testRepo builds a git checkout with the test flake, an admin age key and
// one shared secret encrypted to it.
func testRepo(t *testing.T) *repo.Repo {
	t.Helper()
	for _, tool := range []string{"nix", "git", "sops", "age-keygen", "ssh-keyscan"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	dir := t.TempDir()
	keyFile := filepath.Join(t.TempDir(), "admin.txt")
	out, err := exec.Command("age-keygen", "-o", keyFile).CombinedOutput()
	if err != nil {
		t.Fatalf("age-keygen: %v: %s", err, out)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)
	admin := strings.TrimSpace(strings.TrimPrefix(string(out), "Public key:"))
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("flake.nix", testFlake)
	write("templates/host.nix.tmpl", testTemplate)
	write("hosts/.keep", "")
	write(".sops.yaml", "keys:\n  # the admin key\n  - &admin "+admin+"\n\ncreation_rules:\n  - path_regex: secrets/common/.*\\.yaml$\n    key_groups:\n      - age: [*admin]\n")
	git := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	if err := (sopsfile.Sops{Dir: dir}).Create(context.Background(), "secrets/common/shared.yaml", "wifi", "pw"); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")

	rp, err := repo.Open(context.Background(), execx.Run, dir)
	if err != nil {
		t.Fatal(err)
	}
	return rp
}

func states(st AddStatus) map[string]string {
	m := map[string]string{}
	for _, s := range st.Steps {
		m[s.ID] = s.State
	}
	return m
}

func TestAddResumes(t *testing.T) {
	rp := testRepo(t)
	ctx := context.Background()
	em := contract.NewSinkEmitter("test", func(contract.Event) {})
	vars := func(addr string) AddOptions {
		return AddOptions{Vars: repo.HostVars{Name: "web1", Address: addr, IP: "10.0.0.5"}}
	}

	// 1. The host isn't up yet: everything but the key gets done.
	st, err := Add(ctx, em, rp, vars("127.0.0.1:1"))
	if contract.ExitCode(err) != contract.ExitPrecondition {
		t.Fatalf("first run: err = %v, status %v", err, states(st))
	}
	want := map[string]string{StepModule: StepDone, StepSops: StepDone, StepSecrets: StepDone, StepEval: StepDone, StepKey: StepWaiting}
	if got := states(st); !maps.Equal(got, want) {
		t.Errorf("first run: %v, want %v", got, want)
	}
	mod, _ := os.ReadFile(rp.Path("hosts/web1/web1.nix"))
	if string(mod) != "# web1 at 127.0.0.1:1 (10.0.0.5)\n" {
		t.Errorf("module = %q", mod)
	}
	conf, _ := rp.Sops()
	if k, _ := conf.Key("web1_host"); !strings.HasPrefix(k.Value, repo.DefaultPlaceholderPrefix) {
		t.Errorf("web1_host = %+v", k)
	}
	if r, _ := conf.Rule(`secrets/common/.*\.yaml$`); !slices.Equal(r.Age, []string{"admin", "web1_host"}) {
		t.Errorf("shared rule = %+v", r)
	}
	if r, _ := conf.Rule(`secrets/hosts/web1/.*\.yaml$`); !slices.Equal(r.Age, []string{"admin", "web1_host"}) {
		t.Errorf("host rule = %+v", r)
	}

	// 2. The host is up: its key gets recorded and the shared file re-keyed.
	srv := sshtest.Start(t)
	pub, _ := os.ReadFile(filepath.Join(srv.Dir, "hostkey.pub"))
	live, _ := agekey.FromSSH(string(pub))
	addr := "127.0.0.1:" + itoa(srv.Port)
	st, err = Add(ctx, em, rp, vars(addr))
	if err != nil || !st.Done() {
		t.Fatalf("second run: err = %v, status %v", err, states(st))
	}
	conf, _ = rp.Sops()
	if k, _ := conf.Key("web1_host"); k.Value != live {
		t.Errorf("web1_host = %s, want %s", k.Value, live)
	}
	m, _ := sopsfile.ReadMeta(rp.Path("secrets/common/shared.yaml"))
	if !slices.Contains(m.AgeRecipients, live) {
		t.Errorf("shared file recipients = %v", m.AgeRecipients)
	}

	// 3. Again: nothing left to do, nothing changes.
	before, _ := os.ReadFile(rp.Path(".sops.yaml"))
	if st, err = Add(ctx, em, rp, vars(addr)); err != nil || !st.Done() {
		t.Fatalf("third run: %v %v", err, states(st))
	}
	after, _ := os.ReadFile(rp.Path(".sops.yaml"))
	if !bytes.Equal(before, after) {
		t.Error("a run with nothing to do changed .sops.yaml")
	}

	// The key report agrees, and a secret can be added for the host.
	rep, err := Keys(ctx, rp, nil, []string{"web1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Hosts) != 1 || rep.Hosts[0].State != KeyUnreachable && rep.Hosts[0].State != KeyOK {
		t.Errorf("keys = %+v", rep.Hosts)
	}
	created, err := SetSecret(ctx, rp, SecretTarget{Host: "web1"}, "", "api_token", "t0ken")
	if err != nil || !created {
		t.Fatalf("set: created=%v err=%v", created, err)
	}
	sec, _ := Secrets(rp, []string{"web1"})
	if len(sec.Hosts) != 1 || len(sec.Hosts[0].Files) != 1 || !slices.Equal(sec.Hosts[0].Files[0].Names, []string{"api_token"}) || len(sec.Hosts[0].Files[0].Missing) != 0 {
		t.Errorf("secrets = %+v", sec)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// A provisioned host gets its key made up front: careen add finishes
// without the host existing, and every file is encrypted to the new key.
func TestAddPregenKey(t *testing.T) {
	rp := testRepo(t)
	ctx := context.Background()
	em := contract.NewSinkEmitter("test", func(contract.Event) {})
	opts := AddOptions{Vars: repo.HostVars{Name: "node1", Address: "127.0.0.1:1"}, PregenKey: true}

	st, err := Add(ctx, em, rp, opts)
	if err != nil || !st.Done() {
		t.Fatalf("err = %v, status %v", err, st.Steps)
	}
	want := map[string]string{StepModule: StepDone, StepSops: StepDone, StepSecrets: StepDone, StepHostKey: StepDone, StepKey: StepDone, StepEval: StepDone}
	if got := states(st); !maps.Equal(got, want) {
		t.Errorf("steps %v, want %v", got, want)
	}

	file := rp.Path(rp.HostKeyFile("node1"))
	pub, err := sopsfile.Plain(file, repo.HostKeyPublic)
	if err != nil {
		t.Fatal(err)
	}
	age, _ := agekey.FromSSH(pub)
	conf, _ := rp.Sops()
	if k, _ := conf.Key("node1_host"); k.Value != age {
		t.Errorf("node1_host = %s, want %s", k.Value, age)
	}
	for _, f := range []string{rp.HostKeyFile("node1"), "secrets/common/shared.yaml"} {
		if m, _ := sopsfile.ReadMeta(rp.Path(f)); !slices.Contains(m.AgeRecipients, age) {
			t.Errorf("%s recipients = %v, want %s among them", f, m.AgeRecipients, age)
		}
	}
	priv, err := (sopsfile.Sops{Dir: rp.Dir}).Extract(ctx, rp.HostKeyFile("node1"), repo.HostKeyPrivate)
	if err != nil || !strings.Contains(priv, "OPENSSH PRIVATE KEY") {
		t.Fatalf("private key: %v", err)
	}

	// Again: the key stays, nothing changes.
	before, _ := os.ReadFile(rp.Path(".sops.yaml"))
	if st, err = Add(ctx, em, rp, opts); err != nil || !st.Done() {
		t.Fatalf("second run: %v %v", err, states(st))
	}
	after, _ := os.ReadFile(rp.Path(".sops.yaml"))
	if !bytes.Equal(before, after) {
		t.Error("a run with nothing to do changed .sops.yaml")
	}
	if again, _ := sopsfile.Plain(file, repo.HostKeyPublic); again != pub {
		t.Error("the host key was regenerated")
	}
}

func TestMasterKeyAge(t *testing.T) {
	rp := testRepo(t) // the admin age key is the master key, in SOPS_AGE_KEY_FILE
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()
	if err := MasterKey(ctx, rp); err != nil {
		t.Fatalf("with the admin identity: %v", err)
	}

	other := filepath.Join(t.TempDir(), "other.txt")
	if out, err := exec.Command("age-keygen", "-o", other).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v: %s", err, out)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", other)
	if err := MasterKey(ctx, rp); contract.ExitCode(err) != contract.ExitPrecondition {
		t.Errorf("with another identity: err = %v, want a precondition error", err)
	}

	// An identity sops gets from a command can't be checked; don't guess.
	t.Setenv("SOPS_AGE_KEY_CMD", "false")
	if err := MasterKey(ctx, rp); err != nil {
		t.Errorf("with SOPS_AGE_KEY_CMD: %v", err)
	}
}
