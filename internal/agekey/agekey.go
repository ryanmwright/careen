// Package agekey turns a host's SSH host key into the age recipient that
// sops-nix uses for it: the host decrypts its secrets with its ed25519 host
// key, converted to X25519.
package agekey

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	agessh "github.com/Mic92/ssh-to-age"

	"github.com/ryanmwright/careen/internal/execx"
)

// IsRecipient reports whether s is an age X25519 recipient (age1 and 58
// bech32 characters), as opposed to a placeholder or anything else.
func IsRecipient(s string) bool { return recipient.MatchString(s) }

var recipient = regexp.MustCompile(`^age1[02-9ac-hj-np-z]{58}$`)

// FromSSH converts an ed25519 public key, as an authorized_keys or
// known_hosts line, to an age recipient (age1…).
func FromSSH(line string) (string, error) {
	age, err := agessh.SSHPublicKeyToAge([]byte(strings.TrimSpace(line)))
	if err != nil {
		return "", err
	}
	return *age, nil
}

// Scan fetches address's ed25519 host key with ssh-keyscan and returns it
// as an age recipient. address may carry a port, as host:port; port, if
// not 0, overrides it.
func Scan(ctx context.Context, address string, port int) (string, error) {
	line, err := ScanLine(ctx, address, port)
	if err != nil {
		return "", err
	}
	return FromSSH(line)
}

// ScanLine is Scan's key as ssh-keyscan printed it: "host ssh-ed25519 AAAA...".
func ScanLine(ctx context.Context, address string, port int) (string, error) {
	if h, p, err := net.SplitHostPort(address); err == nil {
		address = h
		if port == 0 {
			port, _ = strconv.Atoi(p)
		}
	}
	// ssh-keyscan ignores ssh_config, so resolve an alias (Host web1-vm)
	// to the host and port ssh would use.
	if h, p := resolve(ctx, address); h != "" {
		address = h
		if port == 0 {
			port = p
		}
	}
	args := []string{"-T", "5", "-t", "ed25519"}
	if port != 0 {
		args = append(args, "-p", fmt.Sprint(port))
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ssh-keyscan", append(args, address)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ssh-keyscan %s: %w: %s", address, err, execx.Tail(stderr.String(), 1))
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "ssh-ed25519") {
			continue
		}
		return line, nil
	}
	return "", errors.New("no ed25519 host key from " + address + " (is sshd running?)")
}

// resolve is the hostname and port `ssh -G` gives address, which applies
// ssh_config without connecting; "" when ssh can't say.
func resolve(ctx context.Context, address string) (string, int) {
	out, err := exec.CommandContext(ctx, "ssh", "-G", address).Output()
	if err != nil {
		return "", 0
	}
	return parseSSHConfig(out)
}

func parseSSHConfig(out []byte) (host string, port int) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), " ")
		switch k {
		case "hostname":
			host = v
		case "port":
			port, _ = strconv.Atoi(v)
		}
	}
	return host, port
}
