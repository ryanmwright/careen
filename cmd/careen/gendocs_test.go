package main

import (
	"strings"
	"testing"
)

func TestCLIDocsCoverEveryCommand(t *testing.T) {
	root := newRootCmd(&globalFlags{})
	var b strings.Builder
	if err := writeCLIDocs(&b, root); err != nil {
		t.Fatal(err)
	}
	doc := b.String()

	for _, c := range documented(root) {
		if !strings.Contains(doc, "## "+c.CommandPath()+"\n") {
			t.Errorf("no section for %q", c.CommandPath())
		}
	}
	for _, want := range []string{
		"## careen vm plan\n",                     // nested commands
		"| `--flake STRING` |",                    // global flags, once
		"`--reboot[=STRING]`",                     // a flag with an optional value
		"| `-n`, `--lines N` | `200` |",           // shorthand and default
		"```text\ncareen deploy --all  ",          // an indented example became a code block
		"&lt;secretsDir&gt;/NAME",                 // placeholders aren't read as HTML
		"- [`careen vm apply`](#careen-vm-apply)", // subcommand links
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs lack %q", want)
		}
	}
	for _, hidden := range []string{"## careen gen-docs", "## careen help", "## careen completion", "`--help`"} {
		if strings.Contains(doc, hidden) {
			t.Errorf("docs include %q", hidden)
		}
	}
	if n := strings.Count(doc, "| `--flake STRING` |"); n != 1 {
		t.Errorf("global flags listed %d times, want once", n)
	}
}
