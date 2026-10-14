package actions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ryanmwright/careen/internal/inventory"
)

func TestReportFlake(t *testing.T) {
	var b bytes.Buffer
	ReportFlake(context.Background(), &b, inventory.Flake{Ref: "./fleet", Revision: "abc"}, "")
	if b.Len() != 0 {
		t.Errorf("a local flake printed %q", b.String())
	}
	ReportFlake(context.Background(), &b, inventory.Flake{Ref: "git+https://git.example/fleet", Revision: "0123456789abcdef", Remote: true}, "")
	if want := "Flake git+https://git.example/fleet is its default branch at 0123456789ab\n"; b.String() != want {
		t.Errorf("printed %q, want %q", b.String(), want)
	}
	b.Reset()
	ReportFlake(context.Background(), &b, inventory.Flake{Ref: "git+https://git.example/fleet", Revision: "0123456789abcdef", Remote: true, Branch: "main"}, t.TempDir())
	if !strings.Contains(b.String(), "is main at") || !strings.Contains(b.String(), "warning:") {
		t.Errorf("printed %q", b.String())
	}
}
