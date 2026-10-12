package activate

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ryanmwright/careen/internal/sshtest"
	"github.com/ryanmwright/careen/internal/sshx"
)

// TestScriptReadOnlyActions runs the real script's read-only actions over
// a local sshd. They need a NixOS host, so the test skips elsewhere.
func TestScriptReadOnlyActions(t *testing.T) {
	if _, err := os.Stat("/run/current-system"); err != nil {
		t.Skip("not NixOS")
	}
	srv := sshtest.Start(t)
	h := sshx.Remote{Address: sshtest.HostAlias, Args: []string{"-F", srv.ConfigFile}, NoMaster: true}
	ctx := context.Background()

	st, err := GetState(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := os.Readlink("/run/current-system")
	if !strings.HasPrefix(st.Current, "/nix/store/") || !strings.HasSuffix(st.Current, strings.TrimPrefix(cur, "/nix/store/")) {
		t.Errorf("state.Current = %q, /run/current-system → %q", st.Current, cur)
	}

	hl, err := CheckHealth(ctx, h, st.Failed, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hl.Current != st.Current || len(hl.NewFailed) != 0 {
		t.Errorf("health = %+v", hl)
	}

	out, err := Run(ctx, h, "boot-id")
	if err != nil || len(strings.TrimSpace(string(out))) != 36 {
		t.Errorf("boot-id = %q, %v", out, err)
	}

	if _, err := Run(ctx, h, "no-such-action"); err == nil {
		t.Error("unknown action didn't fail")
	}
}
