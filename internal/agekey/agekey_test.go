package agekey

import "testing"

// app2's host key and the recipient .sops.yaml has for it.
const (
	hostSSH = "app2.example ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIM1Bdya+Aw4bkFQvxNVd/A6CqJF0ZA3YMghQ2ZBtnw/Y"
	hostAge = "age1yrfwes0nhue60pqrtlq4khnsfkw08xjqkxkhvv94n72fvv63mvyq8jzw24"
)

func TestParseSSHConfig(t *testing.T) {
	out := "user admin\nhostname 127.0.0.1\nport 2201\nidentityfile ~/demo-key\n"
	if h, p := parseSSHConfig([]byte(out)); h != "127.0.0.1" || p != 2201 {
		t.Errorf("parseSSHConfig = %q, %d", h, p)
	}
}

func TestFromSSH(t *testing.T) {
	for _, line := range []string{hostSSH, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIM1Bdya+Aw4bkFQvxNVd/A6CqJF0ZA3YMghQ2ZBtnw/Y root@app2"} {
		got, err := FromSSH(line)
		if err != nil {
			t.Fatal(err)
		}
		if got != hostAge {
			t.Errorf("FromSSH(%q) = %s, want %s", line, got, hostAge)
		}
	}
	if _, err := FromSSH("host ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC"); err == nil {
		t.Error("an RSA key converted")
	}
}
