// Package knownhosts edits an OpenSSH known_hosts file: careen up records
// the key it installed on a host it provisioned, and careen down forgets
// it, so a recreated machine with the same address doesn't trip host key
// checking.
package knownhosts

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Default is the user's known_hosts file.
func Default() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "known_hosts")
}

// Remove drops every line for any of hosts (plain or hashed entries). A
// missing file is fine.
func Remove(path string, hosts ...string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var keep []string
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line == "" {
			continue
		}
		if !matches(line, hosts) {
			keep = append(keep, line)
		}
	}
	return write(path, strings.Join(keep, ""))
}

// Set replaces hosts' entries with one line giving them key
// ("ssh-ed25519 AAAA...").
func Set(path, key string, hosts ...string) error {
	if err := Remove(path, hosts...); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return write(path, s+strings.Join(hosts, ",")+" "+key+"\n")
}

func write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".careen"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// matches reports whether a known_hosts line is for any of hosts.
func matches(line string, hosts []string) bool {
	f := strings.Fields(line)
	if len(f) < 2 || strings.HasPrefix(f[0], "#") {
		return false
	}
	field := f[0]
	if strings.HasPrefix(field, "@") { // @cert-authority / @revoked
		if len(f) < 3 {
			return false
		}
		field = f[1]
	}
	for _, pat := range strings.Split(field, ",") {
		if strings.HasPrefix(pat, "|1|") {
			if slices.ContainsFunc(hosts, func(h string) bool { return hashed(pat, h) }) {
				return true
			}
			continue
		}
		if slices.Contains(hosts, pat) {
			return true
		}
	}
	return false
}

// hashed checks a HashKnownHosts entry, |1|salt|hash, against host.
func hashed(entry, host string) bool {
	parts := strings.Split(entry, "|")
	if len(parts) != 4 {
		return false
	}
	salt, err1 := base64.StdEncoding.DecodeString(parts[2])
	want, err2 := base64.StdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	m := hmac.New(sha1.New, salt)
	m.Write([]byte(host))
	return hmac.Equal(m.Sum(nil), want)
}
