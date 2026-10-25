package actions

import (
	"bytes"
	"context"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/sopsfile"
)

// A file only some hosts share (its own creation rule, not a shared rule)
// is listed, counted among its members' files, and re-keyed with them.
func TestGroupFilesRekeyed(t *testing.T) {
	rp := testRepo(t)
	ctx := context.Background()
	em := contract.NewSinkEmitter("test", func(contract.Event) {})
	for _, h := range []string{"web1", "db1"} {
		opts := AddOptions{Vars: repo.HostVars{Name: h, Address: "127.0.0.1:1"}, PregenKey: true}
		if st, err := Add(ctx, em, rp, opts); err != nil || !st.Done() {
			t.Fatalf("add %s: %v %v", h, err, states(st))
		}
	}

	// A backup group: encrypted to the master key only, then web1 joins the
	// rule without the file being re-keyed.
	const group = "secrets/backup/secrets.yaml"
	sops, _ := os.ReadFile(rp.Path(".sops.yaml"))
	sops = append(sops, "  - path_regex: secrets/backup/.*\\.yaml$\n    key_groups:\n      - age: [*admin]\n"...)
	if err := os.WriteFile(rp.Path(".sops.yaml"), sops, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (sopsfile.Sops{Dir: rp.Dir}).Create(ctx, group, "restic_password", "pw"); err != nil {
		t.Fatal(err)
	}
	conf, err := rp.Sops()
	if err != nil {
		t.Fatal(err)
	}
	if err := conf.JoinRule(`secrets/backup/.*\.yaml$`, "web1_host"); err != nil {
		t.Fatal(err)
	}
	if err := rp.SaveSops(conf); err != nil {
		t.Fatal(err)
	}

	web1 := keyState(ctx, rp, conf, "web1", "127.0.0.1:1")
	if !slices.Contains(web1.Files, group) || !slices.Contains(web1.NeedsRekey, group) {
		t.Errorf("web1 files %v, needs re-key %v; want %s in both", web1.Files, web1.NeedsRekey, group)
	}
	if db1 := keyState(ctx, rp, conf, "db1", "127.0.0.1:1"); slices.Contains(db1.Files, group) {
		t.Errorf("db1 files %v include %s, which it isn't a member of", db1.Files, group)
	}

	if err := RekeyHost(ctx, em, rp, "web1"); err != nil {
		t.Fatal(err)
	}
	k, _ := conf.Key("web1_host")
	if m, _ := sopsfile.ReadMeta(rp.Path(group)); !slices.Contains(m.AgeRecipients, k.Value) {
		t.Errorf("%s recipients = %v, want web1's %s among them", group, m.AgeRecipients, k.Value)
	}

	rep, err := Secrets(rp, nil)
	if err != nil {
		t.Fatal(err)
	}
	var shared []string
	for _, f := range rep.Shared {
		shared = append(shared, f.Path)
	}
	if want := []string{group, "secrets/common/shared.yaml"}; !slices.Equal(shared, want) {
		t.Errorf("shared files %v, want %v", shared, want)
	}
}

// Secret groups: create, set, add, remove, listing, and a host that isn't
// registered yet.
func TestSecretGroups(t *testing.T) {
	rp := testRepo(t)
	ctx := context.Background()
	em := contract.NewSinkEmitter("test", func(contract.Event) {})
	for _, h := range []string{"web1", "db1"} {
		opts := AddOptions{Vars: repo.HostVars{Name: h, Address: "127.0.0.1:1"}, PregenKey: true}
		if st, err := Add(ctx, em, rp, opts); err != nil || !st.Done() {
			t.Fatalf("add %s: %v %v", h, err, states(st))
		}
	}
	backup := SecretTarget{Group: "backup"}
	if _, err := SetSecret(ctx, rp, backup, "", "pw", "x"); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("set without groupsDir: %v", err)
	}

	flake, _ := os.ReadFile(rp.Path("flake.nix"))
	flake = bytes.Replace(flake, []byte("sharedRules ="), []byte("groupsDir = \"secrets\"; sharedRules ="), 1)
	if err := os.WriteFile(rp.Path("flake.nix"), flake, 0o644); err != nil {
		t.Fatal(err)
	}
	rp, err := repo.Open(ctx, execx.Run, rp.Dir)
	if err != nil {
		t.Fatal(err)
	}

	for name, err := range map[string]error{
		"hosts":    GroupCreate(rp, "hosts", nil),
		"../x":     GroupCreate(rp, "../x", nil),
		"nope":     GroupCreate(rp, "backup", []string{"nope"}),
		"no group": GroupAdd(ctx, em, rp, "backup", []string{"db1"}),
	} {
		if contract.ExitCode(err) != contract.ExitUsage {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
	if err := GroupCreate(rp, "backup", []string{"web1"}); err != nil {
		t.Fatal(err)
	}
	if err := GroupCreate(rp, "backup", nil); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("creating it again: %v", err)
	}

	const file = "secrets/backup/secrets.yaml"
	if created, err := SetSecret(ctx, rp, backup, "", "restic_password", "pw"); err != nil || !created {
		t.Fatalf("set: created=%v err=%v", created, err)
	}
	conf, _ := rp.Sops()
	key := func(h string) string { k, _ := conf.Key(h + "_host"); return k.Value }
	recipients := func() []string { m, _ := sopsfile.ReadMeta(rp.Path(file)); return m.AgeRecipients }
	if r := recipients(); !slices.Contains(r, key("web1")) || slices.Contains(r, key("db1")) {
		t.Errorf("after create: recipients %v", r)
	}

	if err := GroupAdd(ctx, em, rp, "backup", []string{"db1"}); err != nil {
		t.Fatal(err)
	}
	if err := GroupRemove(ctx, em, rp, "backup", []string{"web1"}); err != nil {
		t.Fatal(err)
	}
	if r := recipients(); slices.Contains(r, key("web1")) || !slices.Contains(r, key("db1")) {
		t.Errorf("after add db1, remove web1: recipients %v", r)
	}

	rep, err := Secrets(rp, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, g := range rep.Groups {
		got[g.Group] = g.Hosts
	}
	if want := map[string][]string{"backup": {"db1"}, "common": {"web1", "db1"}}; !maps.EqualFunc(got, want, slices.Equal) || len(rep.Shared) != 0 {
		t.Errorf("groups %v, shared %v; want groups %v and nothing shared", got, rep.Shared, want)
	}
	if rep, _ := Secrets(rp, []string{"web1"}); len(rep.Groups) != 1 || rep.Groups[0].Group != "common" {
		t.Errorf("web1's groups = %+v, want common only", rep.Groups)
	}

	// An unregistered host can join (its registration re-keys the files
	// later), but then nothing can be re-keyed, so removing waits.
	if _, err := Add(ctx, em, rp, AddOptions{Vars: repo.HostVars{Name: "mgr", Address: "127.0.0.1:1"}}); contract.ExitCode(err) != contract.ExitPrecondition {
		t.Fatalf("add mgr: %v", err)
	}
	before := recipients()
	if err := GroupAdd(ctx, em, rp, "backup", []string{"mgr"}); err != nil {
		t.Fatalf("adding an unregistered host: %v", err)
	}
	if !slices.Equal(recipients(), before) {
		t.Error("the file was re-keyed with a placeholder in the group")
	}
	if err := GroupRemove(ctx, em, rp, "backup", []string{"db1"}); contract.ExitCode(err) != contract.ExitPrecondition {
		t.Errorf("removing with a placeholder in the group: %v", err)
	}
	if conf, _ := rp.Sops(); !slices.Contains(must(conf.Rule(rp.GroupRule("backup"))).Age, "db1_host") {
		t.Error("a failed remove still changed .sops.yaml")
	}
}

func must[T any](v T, _ bool) T { return v }
