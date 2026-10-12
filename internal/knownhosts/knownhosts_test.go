package knownhosts

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func hash(host string) string {
	salt := []byte("0123456789abcdefghij")
	m := hmac.New(sha1.New, salt)
	m.Write([]byte(host))
	return "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func TestSetRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	orig := "other.example ssh-ed25519 AAAAother\n" +
		"10.0.0.21 ssh-ed25519 AAAAold\n" +
		hash("node1") + " ssh-ed25519 AAAAhashed\n" +
		"# a comment with 10.0.0.21\n" +
		"node1,10.0.0.21 ssh-rsa AAAArsa\n"
	os.WriteFile(path, []byte(orig), 0o600)

	if err := Set(path, "ssh-ed25519 AAAAnew", "node1", "10.0.0.21"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "other.example ssh-ed25519 AAAAother\n# a comment with 10.0.0.21\nnode1,10.0.0.21 ssh-ed25519 AAAAnew\n"
	if string(got) != want {
		t.Fatalf("after Set:\n%s\nwant:\n%s", got, want)
	}

	if err := Remove(path, "10.0.0.21"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "other.example ssh-ed25519 AAAAother\n# a comment with 10.0.0.21\n" {
		t.Fatalf("after Remove: %q", got)
	}
	if err := Remove(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
}
