package execx

import (
	"context"
	"strings"
	"testing"
	"time"
)

const nixUnfree = `error:
       … while calling the 'head' builtin
       … while evaluating the option ` + "`system.build.toplevel'" + `:
       error: Refusing to evaluate package 'rider-with-plugins-2026.2.0.1' in /nix/store/x-source/pkgs/applications/editors/jetbrains/ides/rider.nix:85 because it has an unfree license (‘unfree’)
       a) To temporarily allow unfree packages, you can use an environment variable
          for a single invocation of the nix tools.
         $ export NIXPKGS_ALLOW_UNFREE=1
       c) For ` + "`nix-env`" + `, ` + "`nix-build`" + `, ` + "`nix-shell`" + ` or any other Nix command you can add
         { allowUnfree = true; }
       to ~/.config/nixpkgs/config.nix.
`

func TestSummaryPicksTheInnermostNixError(t *testing.T) {
	got := Summary(nixUnfree)
	if !strings.HasPrefix(got, "error: Refusing to evaluate package 'rider-with-plugins-2026.2.0.1'") {
		t.Errorf("summary = %q", got)
	}
	if strings.Contains(got, "allowUnfree") {
		t.Errorf("summary kept the hints: %q", got)
	}
}

func TestSummaryWithoutNixError(t *testing.T) {
	if got := Summary("one\ntwo\nthree\nfour\n"); got != "two; three; four" {
		t.Errorf("summary = %q", got)
	}
}

func TestTimings(t *testing.T) {
	var tm *Timings
	tm.Add("nil is fine", time.Second) // must not panic
	if tm.Items() != nil {
		t.Error("nil Timings has items")
	}
	tm = &Timings{}
	run := tm.Wrap(func(context.Context, string, ...string) ([]byte, error) { return []byte("ok"), nil })
	if out, err := run(context.Background(), "nix", "eval", "x#pve"); err != nil || string(out) != "ok" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	tm.AddPhase("web1: deploy", time.Second)
	items := tm.Items()
	if len(items) != 2 || items[0].What != "nix eval x#pve" || items[0].Phase || !items[1].Phase {
		t.Errorf("items = %+v", items)
	}
}
