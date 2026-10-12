package inventory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
)

const fleetJSON = `{
  "mgr": {"address":"mgr.example","buildOn":"remote","builder":"ssh-ng://b","deploy":true,"kind":"nixos","operation":"switch","rebootPolicy":"manual","tags":["vm"],"user":"admin"},
  "app1": {"address":"10.0.0.40","buildOn":"remote","builder":"ssh-ng://b","deploy":true,"kind":"nixos","operation":"switch","rebootPolicy":"auto","tags":["ci","vm"],"user":"admin"},
  "app2": {"address":"app2.example","buildOn":"remote","builder":"ssh-ng://b","deploy":true,"kind":"nixos","operation":"switch","rebootPolicy":"manual","tags":[],"user":"admin"}
}`

type fake struct {
	eval, meta string
	evalErr    error
	evalRefs   []string // what each nix eval was given
	metaCmds   []string
}

func (f *fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := name + " " + strings.Join(args, " ")
	switch {
	case strings.HasPrefix(cmd, "nix eval --json "):
		f.evalRefs = append(f.evalRefs, args[len(args)-1])
		return []byte(f.eval), f.evalErr
	case strings.HasPrefix(cmd, "nix flake metadata --json "):
		f.metaCmds = append(f.metaCmds, cmd)
		return []byte(f.meta), nil
	}
	return nil, errors.New("unexpected command: " + cmd)
}

func fakeRunner(eval, meta string, evalErr error) execx.Runner {
	return (&fake{eval: eval, meta: meta, evalErr: evalErr}).run
}

func TestLoad(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{eval: fleetJSON, meta: `{"url":"git+ssh://example/nix?ref=main&rev=abc123","revision":"abc123"}`}
	inv, err := Load(context.Background(), f.run, "git+ssh://example/nix?ref=main", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.names(); !slices.Equal(got, []string{"app1", "app2", "mgr"}) {
		t.Errorf("hosts = %v", got)
	}
	if inv.Flake != (Flake{Ref: "git+ssh://example/nix?ref=main", Revision: "abc123", Locked: "git+ssh://example/nix?ref=main&rev=abc123"}) {
		t.Errorf("flake = %+v", inv.Flake)
	}
	if inv.Hosts[0].RebootPolicy != "auto" || inv.Hosts[0].Address != "10.0.0.40" {
		t.Errorf("app1 = %+v", inv.Hosts[0])
	}
	// The fleet is evaluated at the locked revision, once; the second load
	// comes from the cache.
	if _, err := Load(context.Background(), f.run, "git+ssh://example/nix?ref=main", false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.evalRefs, []string{"git+ssh://example/nix?ref=main&rev=abc123#fleet"}) {
		t.Errorf("evals = %v", f.evalRefs)
	}
}

func TestLoadRefresh(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{eval: fleetJSON, meta: `{"url":"git+ssh://x?rev=abc","revision":"abc"}`}
	if _, err := Load(context.Background(), f.run, "git+ssh://x", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.metaCmds, []string{"nix flake metadata --json --refresh git+ssh://x"}) {
		t.Errorf("metadata = %v", f.metaCmds)
	}
}

func TestLoadDirtyFlakeIsNotCached(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{eval: fleetJSON, meta: `{"url":"git+file:///src/nix","dirtyRevision":"abc123-dirty"}`}
	for range 2 {
		inv, err := Load(context.Background(), f.run, ".", false)
		if err != nil {
			t.Fatal(err)
		}
		if inv.Flake.Revision != "abc123" || !inv.Flake.Dirty {
			t.Errorf("flake = %+v", inv.Flake)
		}
	}
	if !slices.Equal(f.evalRefs, []string{".#fleet", ".#fleet"}) {
		t.Errorf("evals = %v", f.evalRefs)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ctx := context.Background()
	if _, err := Load(ctx, fakeRunner(fleetJSON, `{}`, nil), "", false); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("no flake: err = %v", err)
	}
	if _, err := Load(ctx, fakeRunner("", `{}`, errors.New("eval failed")), ".", false); contract.ExitCode(err) != contract.ExitFailed {
		t.Errorf("eval error: err = %v", err)
	}
}

func TestSelect(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	inv, err := Load(context.Background(), fakeRunner(fleetJSON, `{"revision":"abc"}`, nil), ".", false)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		sel  Selector
		want []string
	}{
		{"all", Selector{}, []string{"app1", "app2", "mgr"}},
		{"only", Selector{Only: []string{"mgr", "app2"}}, []string{"app2", "mgr"}},
		{"except", Selector{Except: []string{"mgr"}}, []string{"app1", "app2"}},
		{"tag", Selector{Tags: []string{"vm"}}, []string{"app1", "mgr"}},
		{"tag and except", Selector{Tags: []string{"vm"}, Except: []string{"mgr"}}, []string{"app1"}},
	}
	for _, c := range cases {
		hosts, err := inv.Select(c.sel)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got []string
		for _, h := range hosts {
			got = append(got, h.Name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	if _, err := inv.Select(Selector{Only: []string{"nosuchhost"}}); contract.ExitCode(err) != contract.ExitUsage {
		t.Errorf("unknown host: err = %v", err)
	}
}

func TestLoadLast(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ref := "git+ssh://example/nix?ref=main"
	if _, ok := LoadLast(ref); ok {
		t.Fatal("LoadLast before any Load")
	}
	f := &fake{eval: fleetJSON, meta: `{"url":"git+ssh://example/nix?ref=main&rev=abc123","path":"/nix/store/src","revision":"abc123"}`}
	want, err := Load(context.Background(), f.run, ref, true)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := LoadLast(ref)
	if !ok {
		t.Fatal("LoadLast after Load: none")
	}
	if got.Flake != want.Flake || !slices.Equal(got.names(), want.names()) || !slices.Equal(got.Hosts[0].Tags, want.Hosts[0].Tags) {
		t.Errorf("LoadLast = %+v, want %+v", got, want)
	}
	if _, ok := LoadLast("git+ssh://example/other"); ok {
		t.Error("LoadLast of another ref")
	}
}

func TestLoadLastSkipsDirtyTree(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{eval: fleetJSON, meta: `{"url":"git+file:///src/nix","dirtyRevision":"abc123-dirty"}`}
	if _, err := Load(context.Background(), f.run, ".", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadLast("."); ok {
		t.Error("a dirty tree was kept")
	}
}
