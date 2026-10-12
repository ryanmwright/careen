package sopsconf

import (
	"os"
	"slices"
	"strings"
	"testing"
)

const common = `secrets/common/.*\.yaml$`

func load(t *testing.T) (*Config, string) {
	t.Helper()
	b, err := os.ReadFile("testdata/sops.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return c, string(b)
}

func TestParseRealFile(t *testing.T) {
	c, _ := load(t)
	k, ok := c.Key("mgr_host")
	if !ok || !strings.HasPrefix(k.Value, "age1REPLACEME_") {
		t.Errorf("mgr_host = %+v", k)
	}
	if k, _ := c.Key("app2_host"); k.Value != "age1yrfwes0nhue60pqrtlq4khnsfkw08xjqkxkhvv94n72fvv63mvyq8jzw24" || strings.HasPrefix(k.Value, "age1REPLACEME_") {
		t.Errorf("app2_host = %+v", k)
	}
	r, ok := c.Rule(common)
	if !ok || !slices.Equal(r.PGP, []string{"admin_pgp"}) || len(r.Age) != 6 {
		t.Errorf("common rule = %+v", r)
	}
	if r, _ := c.Rule(`secrets/hosts/app3/.*\.yaml$`); !slices.Equal(r.Age, []string{"app3_host"}) {
		t.Errorf("app3 rule = %+v", r)
	}
}

func TestAddHost(t *testing.T) {
	c, orig := load(t)
	rule := `secrets/hosts/web1/.*\.yaml$`
	steps := []error{
		c.SetKey("web1_host", "age1REPLACEME_"+"WEB1"),
		c.AddRule(rule, []string{"admin_pgp"}, []string{"web1_host"}, []string{common}),
		c.JoinRule(common, "web1_host"),
	}
	for i, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	out := string(c.Bytes())

	// Every original line survives, comments included; only additions.
	for _, l := range strings.Split(orig, "\n") {
		if !strings.Contains(out, l) && !strings.HasPrefix(strings.TrimSpace(l), "age: [*app2_host") {
			t.Errorf("lost line %q", l)
		}
	}
	for _, want := range []string{
		"  - &app3_host age147e9e0mql6t2w32mfmhq7dwnzqz56n6ycs6wc4tssz6w96kf0cxqhq7az0\n  - &web1_host age1REPLACEME_WEB1\n",
		"  - path_regex: secrets/hosts/web1/.*\\.yaml$\n    key_groups:\n      - pgp: [*admin_pgp]\n        age: [*web1_host]\n\n  - path_regex: secrets/common/",
		"*app3_host, *web1_host]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// Registering the key fills the placeholder in place.
	if err := c.SetKey("web1_host", "age1abc"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c.Bytes()), "  - &web1_host age1abc\n") {
		t.Error("placeholder not replaced")
	}

	// Doing it all again changes nothing.
	before := string(c.Bytes())
	c.AddRule(rule, []string{"admin_pgp"}, []string{"web1_host"}, []string{common})
	c.JoinRule(common, "web1_host")
	c.SetKey("web1_host", "age1abc")
	if string(c.Bytes()) != before {
		t.Error("repeating the edits changed the file")
	}
}
