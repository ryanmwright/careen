// Package hostkey makes SSH host keys ahead of time, so a host's age
// recipient (and every secret encrypted to it) can exist before the host
// does. careen add stores the key in sops; careen up installs it.
package hostkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Key is an ed25519 host key in OpenSSH formats.
type Key struct {
	// Private is the OpenSSH private key file (-----BEGIN OPENSSH PRIVATE KEY-----).
	Private string
	// Public is the authorized_keys form: "ssh-ed25519 AAAA... comment".
	Public string
}

// Generate makes a new ed25519 host key; comment ends the public line
// (sshd writes root@<host>).
func Generate(comment string) (Key, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Key{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return Key{}, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Key{}, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return Key{Private: string(pem.EncodeToMemory(block)), Public: line}, nil
}

// SameKey reports whether two authorized_keys/known_hosts style lines carry
// the same key, ignoring comments and host names.
func SameKey(a, b string) bool {
	ka, errA := parse(a)
	kb, errB := parse(b)
	return errA == nil && errB == nil && ka == kb
}

// Bare is a key line without its comment: "ssh-ed25519 AAAA...".
func Bare(line string) (string, error) {
	return parse(line)
}

func parse(line string) (string, error) {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		if strings.HasPrefix(f[i], "ssh-") || strings.HasPrefix(f[i], "ecdsa-") {
			k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(f[i] + " " + f[i+1]))
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), nil
		}
	}
	return "", fmt.Errorf("no SSH key in %q", line)
}
