package actions

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/ryanmwright/careen/internal/contract"
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
