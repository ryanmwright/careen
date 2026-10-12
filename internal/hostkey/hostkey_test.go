package hostkey

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ryanmwright/careen/internal/agekey"
)

func TestGenerate(t *testing.T) {
	k, err := Generate("root@cp1")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(k.Private))
	if err != nil {
		t.Fatalf("private key doesn't parse: %v", err)
	}
	if !strings.HasSuffix(k.Public, " root@cp1") || !strings.HasPrefix(k.Public, "ssh-ed25519 ") {
		t.Fatalf("public line %q", k.Public)
	}
	if !SameKey(k.Public, string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) {
		t.Fatal("public line doesn't match the private key")
	}
	age, err := agekey.FromSSH(k.Public)
	if err != nil || !strings.HasPrefix(age, "age1") {
		t.Fatalf("age recipient %q, %v", age, err)
	}
}

func TestSameKey(t *testing.T) {
	a, _ := Generate("one")
	b, _ := Generate("two")
	bare, _ := Bare(a.Public)
	switch {
	case !SameKey(a.Public, "10.0.0.21,cp1 "+bare):
		t.Error("a known_hosts line with the same key should match")
	case SameKey(a.Public, b.Public):
		t.Error("different keys matched")
	case SameKey(a.Public, "garbage"):
		t.Error("garbage matched")
	}
}
