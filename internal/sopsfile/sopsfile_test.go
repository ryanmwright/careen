package sopsfile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testRepo makes a directory with a throwaway age key and a .sops.yaml
// that encrypts secrets/*.yaml to it.
func testRepo(t *testing.T) (Sops, string) {
	t.Helper()
	for _, tool := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	out, err := exec.Command("age-keygen", "-o", keyFile).CombinedOutput()
	if err != nil {
		t.Fatalf("age-keygen: %v: %s", err, out)
	}
	pub := strings.TrimSpace(strings.TrimPrefix(string(out), "Public key:"))
	conf := "keys:\n  - &test " + pub + "\ncreation_rules:\n  - path_regex: secrets/.*\\.yaml$\n    key_groups:\n      - age: [*test]\n"
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	return Sops{Dir: dir, Env: []string{"SOPS_AGE_KEY_FILE=" + keyFile}}, pub
}

func TestCreateSetUnset(t *testing.T) {
	s, pub := testRepo(t)
	ctx := context.Background()
	file := "secrets/web.yaml"

	if err := s.Create(ctx, file, "api_token", "s3cret: with 'quotes'"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, file))
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("value written in the clear")
	}
	m, err := ReadMeta(filepath.Join(s.Dir, file))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Names, []string{"api_token"}) || !slices.Equal(m.AgeRecipients, []string{pub}) || m.LastModified.IsZero() {
		t.Errorf("meta = %+v", m)
	}

	if err := s.Set(ctx, file, "db_password", "pw\nwith newline"); err != nil {
		t.Fatal(err)
	}
	if m, _ := ReadMeta(filepath.Join(s.Dir, file)); !slices.Equal(m.Names, []string{"api_token", "db_password"}) {
		t.Errorf("after set: %v", m.Names)
	}
	dec, err := s.run(ctx, nil, "decrypt", "--extract", `["db_password"]`, file)
	if err != nil || string(dec) != "pw\nwith newline" {
		t.Errorf("decrypted %q, %v", dec, err)
	}

	if err := s.Unset(ctx, file, "api_token"); err != nil {
		t.Fatal(err)
	}
	if m, _ := ReadMeta(filepath.Join(s.Dir, file)); !slices.Equal(m.Names, []string{"db_password"}) {
		t.Errorf("after unset: %v", m.Names)
	}
	if err := s.UpdateKeys(ctx, file); err != nil {
		t.Errorf("updatekeys: %v", err)
	}
}

func TestCreateValuesExtractPlain(t *testing.T) {
	s, _ := testRepo(t)
	ctx := context.Background()
	file := "secrets/host-key.yaml"
	priv := "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\ndef\n-----END OPENSSH PRIVATE KEY-----\n"
	pub := "ssh-ed25519 AAAAC3Nza root@host"
	if err := s.CreateValues(ctx, file, map[string]string{"ssh_host_ed25519_key": priv, "public_key_unencrypted": pub}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, file))
	if strings.Contains(string(raw), "abc") {
		t.Fatal("private key written in the clear")
	}
	if got, err := Plain(filepath.Join(s.Dir, file), "public_key_unencrypted"); err != nil || got != pub {
		t.Fatalf("Plain = %q, %v", got, err)
	}
	if _, err := Plain(filepath.Join(s.Dir, file), "ssh_host_ed25519_key"); err == nil {
		t.Fatal("Plain read an encrypted value")
	}
	got, err := s.Extract(ctx, file, "ssh_host_ed25519_key")
	if err != nil || got != priv {
		t.Fatalf("Extract = %q, %v", got, err)
	}
}
