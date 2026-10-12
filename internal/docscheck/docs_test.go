package docscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/provision/proxmox"
	"github.com/ryanmwright/careen/internal/repo"
)

// root is the repository root, from this package's directory.
const root = "../.."

func page(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "docs", name))
	if err != nil {
		t.Fatalf("reading docs/%s: %v", name, err)
	}
	return string(b)
}

// Every "careen.<name>/v<N>" schema in the code is in the JSON reference.
func TestSchemasDocumented(t *testing.T) {
	schema := regexp.MustCompile(`"(careen\.[a-z][a-z.]*/v[0-9]+)"`)
	found := map[string]string{} // schema -> a file that uses it
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "docs" || strings.HasPrefix(d.Name(), "result")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range schema.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 10 {
		t.Fatalf("found only %d schemas; is the pattern still right?", len(found))
	}
	doc := page(t, "reference/json-output.md")
	for s, file := range found {
		if !strings.Contains(doc, "`"+s+"`") {
			t.Errorf("schema %s (used in %s) isn't documented in docs/reference/json-output.md", s, file)
		}
	}
}

// Every field careen reads from the flake is documented on its page.
func TestFlakeFieldsDocumented(t *testing.T) {
	for _, c := range []struct {
		page string
		typ  any
		skip []string
	}{
		{"reference/inventory-fields.md", inventory.Host{}, []string{"name"}}, // the attribute name, not a field
		{"concepts/flake-settings.md", repo.Settings{}, nil},
		{"guides/provisioning-proxmox.md", proxmox.Config{}, nil},
		{"guides/provisioning-proxmox.md", proxmox.Node{}, nil},
		{"guides/provisioning-proxmox.md", proxmox.VM{}, nil},
	} {
		doc := page(t, c.page)
		for _, f := range jsonFields(reflect.TypeOf(c.typ)) {
			if slices.Contains(c.skip, f) {
				continue
			}
			if !strings.Contains(doc, "`"+f+"`") {
				t.Errorf("%T field %q isn't documented in docs/%s", c.typ, f, c.page)
			}
		}
	}
}

// jsonFields lists a struct's JSON names, nested structs' included.
func jsonFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
		if f.Type.Kind() == reflect.Struct {
			out = append(out, jsonFields(f.Type)...)
		}
	}
	return out
}

// careen is a public project for any NixOS flake: its Go code may only read
// the flake's outputs, never a host's own NixOS options (config.my.*, say),
// whose names belong to one flake. Patterns that would identify a particular
// deployment (domains, subnets) can't be listed here without naming it, so
// they come from $CAREEN_PRIVATE_PATTERNS, a regex set on a maintainer's own
// machine; without it that half of the check is skipped.
func TestNoPersonalReferences(t *testing.T) {
	var denied *regexp.Regexp
	if p := os.Getenv("CAREEN_PRIVATE_PATTERNS"); p != "" {
		denied = regexp.MustCompile(p)
	}
	options := regexp.MustCompile(`config\.my\.`)
	scan := []string{"cmd", "internal", "docs", "examples", "scripts", "nix", ".github", "flake.nix", "mkdocs.yml", "README.md", "CLAUDE.md", "CONTRIBUTING.md"}
	for _, top := range scan {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(b), "\n") {
				if denied != nil {
					if m := denied.FindString(line); m != "" {
						t.Errorf("%s:%d mentions %q, which points at a particular deployment", path, i+1, m)
					}
				}
				if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "docs_test.go") && options.MatchString(line) {
					t.Errorf("%s:%d reads a host's own NixOS options; read the flake's outputs instead", path, i+1)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
