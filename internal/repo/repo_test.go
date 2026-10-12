package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNoSettings(t *testing.T) {
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("nix: exit status 1: error: flake 'path:.' does not provide attribute 'packages.x86_64-linux.careen', 'legacyPackages.x86_64-linux.careen' or 'careen'")
	}
	if _, err := LoadSettings(context.Background(), run, "."); !errors.Is(err, ErrNoSettings) {
		t.Fatalf("err = %v, want ErrNoSettings", err)
	}
}

func TestSettingsDefaultsAndNaming(t *testing.T) {
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`{"hostTemplate":"t","hostDir":"h","secretsDir":"s","sopsConfig":".sops.yaml"}`), nil
	}
	s, err := LoadSettings(context.Background(), run, ".")
	if err != nil {
		t.Fatal(err)
	}
	if s.AnchorSuffix != "_host" || s.PlaceholderPrefix != "age1REPLACEME_" {
		t.Fatalf("defaults = %q, %q", s.AnchorSuffix, s.PlaceholderPrefix)
	}
	if got := s.Anchor("web1"); got != "web1_host" {
		t.Errorf("Anchor = %q", got)
	}
	if got := s.Placeholder("web1"); got != "age1REPLACEME_WEB1" || !s.IsPlaceholder(got) {
		t.Errorf("Placeholder = %q", got)
	}

	// A flake with its own conventions.
	c := Settings{AnchorSuffix: "-key", PlaceholderPrefix: "age1TODO-"}
	if got := c.Anchor("db1"); got != "db1-key" {
		t.Errorf("custom Anchor = %q", got)
	}
	if host, ok := c.AnchorHost("db1-key"); !ok || host != "db1" {
		t.Errorf("AnchorHost(db1-key) = %q, %v", host, ok)
	}
	if _, ok := c.AnchorHost("admin_pgp"); ok {
		t.Error("a master key anchor read as a host's")
	}
	if !c.IsPlaceholder(c.Placeholder("db1")) || c.IsPlaceholder("age1REPLACEME_DB1") {
		t.Error("custom placeholder prefix not honoured")
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "flake.nix"), []byte("{}"), 0o644)
	deep := filepath.Join(root, "hosts", "web1")
	os.MkdirAll(deep, 0o755)

	// Walks up from the current directory to the flake.
	t.Chdir(deep)
	if got, err := Find(""); err != nil || got != root {
		t.Errorf("Find from %s = %q, %v; want %s", deep, got, err, root)
	}

	// An explicit directory, with ~/ expanded.
	t.Setenv("HOME", filepath.Dir(root))
	if got, err := Find("~/" + filepath.Base(root)); err != nil || got != root {
		t.Errorf("Find(~/...) = %q, %v", got, err)
	}
	if _, err := Find(deep); err == nil {
		t.Error("expected an error for a directory without flake.nix")
	}

	// Nothing here or above.
	t.Chdir(t.TempDir())
	if _, err := Find(""); err == nil {
		t.Error("expected an error with no flake.nix here or above")
	}
}
