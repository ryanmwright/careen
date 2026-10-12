package build

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ryanmwright/careen/internal/inventory"
)

const sys = "/nix/store/aaaa-nixos-system-web"

func recorder(calls *[]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		if args[0] == "build" {
			return []byte(sys + "\n"), nil
		}
		return nil, nil
	}
}

func TestBuildModes(t *testing.T) {
	host := inventory.Host{Name: "web", User: "admin", Address: "10.0.0.5"}
	dest := HostStore(host)
	const ref = "git+ssh://x/nix?rev=abc"
	top := ref + "#nixosConfigurations.web.config.system.build.toplevel"
	cases := []struct {
		name string
		job  Job
		want []string
	}{
		{"local", Job{Host: host, Mode: Local, Dest: dest}, []string{
			"nix build --no-link --print-out-paths " + top,
			"nix copy --no-check-sigs --substitute-on-destination --to ssh-ng://admin@10.0.0.5 " + sys,
		}},
		{"remote", Job{Host: host, Mode: Remote, Builder: "ssh-ng://b", Dest: dest}, []string{
			"nix build --eval-store auto --store ssh-ng://b --no-link --print-out-paths " + top,
			"nix copy --no-check-sigs --substitute-on-destination --from ssh-ng://b --to ssh-ng://admin@10.0.0.5 " + sys,
		}},
		{"target", Job{Host: host, Mode: Target, Dest: dest}, []string{
			"nix build --eval-store auto --store ssh-ng://admin@10.0.0.5 --no-link --print-out-paths " + top,
		}},
		{"remote to this machine", Job{Host: host, Mode: Remote, Builder: "ssh-ng://b"}, []string{
			"nix build --eval-store auto --store ssh-ng://b --no-link --print-out-paths " + top,
			"nix copy --no-check-sigs --substitute-on-destination --from ssh-ng://b " + sys,
		}},
		{"target is this machine", Job{Host: host, Mode: Target}, []string{
			"nix build --no-link --print-out-paths " + top,
		}},
	}
	for _, c := range cases {
		var calls []string
		path, err := Build(context.Background(), recorder(&calls), ref, c.job, func(string, string) {})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if path != sys {
			t.Errorf("%s: path = %q", c.name, path)
		}
		if !slices.Equal(calls, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, calls, c.want)
		}
	}
}

func TestBuildErrors(t *testing.T) {
	host := inventory.Host{Name: "web", Address: "a"}
	var calls []string
	if _, err := Build(context.Background(), recorder(&calls), ".", Job{Host: host, Mode: Remote}, func(string, string) {}); err == nil {
		t.Error("remote without builder: no error")
	}
	if _, err := Build(context.Background(), recorder(&calls), ".", Job{Host: host, Mode: "elsewhere"}, func(string, string) {}); err == nil {
		t.Error("unknown mode: no error")
	}
}
