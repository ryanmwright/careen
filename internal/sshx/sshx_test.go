package sshx

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

func TestQuote(t *testing.T) {
	for _, s := range []string{"plain", "/nix/store/abc-x", "", "it's", "a b", "$(rm -rf /)", `"q"`} {
		out, err := exec.Command("sh", "-c", "printf %s "+Quote(s)).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("Quote(%q) round-trips to %q", s, out)
		}
	}
}

func TestSSHArgs(t *testing.T) {
	r := Remote{User: "admin", Address: "10.0.0.5", NoMaster: true, Args: []string{"-o", "BatchMode=no"}}
	args := r.SSHArgs("sh -s")
	// Extra args come first so they win over the defaults.
	if args[1] != "BatchMode=no" || !slices.Contains(args, "ControlMaster=no") {
		t.Errorf("args = %v", args)
	}
	if got := args[len(args)-5:]; !slices.Equal(got, []string{"-l", "admin", "--", "10.0.0.5", "sh -s"}) {
		t.Errorf("tail = %v", got)
	}
}

func TestLocalHostArgs(t *testing.T) {
	out, err := LocalHost{}.Run(context.Background(), `printf '%s|%s' "$1" "$2"`, "a b", "it's")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a b|it's" {
		t.Errorf("out = %q", out)
	}
}

func TestCapture(t *testing.T) {
	out, err := Capture(context.Background(), LocalHost{}, 8, `echo hello world; echo oops >&2; exit 3`)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 3 || string(out.Stdout) != "hello wo" || !out.Truncated || string(out.Stderr) != "oops\n" {
		t.Errorf("out = %+v (stdout %q)", out, out.Stdout)
	}
}

func TestLabel(t *testing.T) {
	r := Remote{Address: "10.0.0.21"}
	script := "# comment\nset -e\n\nsystemctl is-system-running\necho done\n"
	if got, want := label(r, script), "ssh 10.0.0.21: systemctl is-system-running"; got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
	if got := label(r, "set -e\n"); got != "ssh 10.0.0.21" {
		t.Errorf("label of an empty script = %q", got)
	}
}
