// Package repo is the writable checkout of the flake that onboarding and
// key management change, and the settings the flake gives careen in its
// `careen` output. careen never commits: changes are left for review.
package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/sopsconf"
)

// Settings is the flake's `careen` output. Paths are relative to the repo.
type Settings struct {
	HostTemplate string `json:"hostTemplate"`
	HostDir      string `json:"hostDir"`
	SecretsDir   string `json:"secretsDir"`
	SopsConfig   string `json:"sopsConfig"`
	MasterKeys   struct {
		PGP []string `json:"pgp"`
		Age []string `json:"age"`
	} `json:"masterKeys"`
	SharedRules []string `json:"sharedRules"`
	// GroupsDir holds secret groups, one directory each: files encrypted
	// to the master keys and the group's hosts. Optional.
	GroupsDir string `json:"groupsDir"`
	// AnchorSuffix names each host's key anchor in .sops.yaml:
	// <host><AnchorSuffix>. Default "_host".
	AnchorSuffix string `json:"anchorSuffix"`
	// PlaceholderPrefix starts the key of a host that isn't registered yet:
	// <PlaceholderPrefix><HOST>. Default "age1REPLACEME_".
	PlaceholderPrefix string `json:"placeholderPrefix"`
}

// Defaults for the naming settings.
const (
	DefaultAnchorSuffix      = "_host"
	DefaultPlaceholderPrefix = "age1REPLACEME_"
)

type Repo struct {
	Dir      string
	Settings Settings
	run      execx.Runner
}

// Find picks the checkout: dir if given (--repo, else $CAREEN_REPO, which
// the caller passes in), otherwise the nearest directory at or above the
// current one that holds a flake.nix. A leading ~/ means the home directory.
func Find(dir string) (string, error) {
	if dir != "" {
		if rest, ok := strings.CutPrefix(dir, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, rest)
		}
		if !exists(filepath.Join(dir, "flake.nix")) {
			return "", fmt.Errorf("%s has no flake.nix", dir)
		}
		return filepath.Abs(dir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := cwd; ; d = filepath.Dir(d) {
		if exists(filepath.Join(d, "flake.nix")) {
			return d, nil
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return "", errors.New("no checkout of the flake found here or above: pass --repo or set CAREEN_REPO")
}

// Open loads the checkout at dir and its settings.
func Open(ctx context.Context, run execx.Runner, dir string) (*Repo, error) {
	if !exists(filepath.Join(dir, ".git")) {
		return nil, fmt.Errorf("%s isn't a git checkout", dir)
	}
	s, err := LoadSettings(ctx, run, dir)
	if err != nil {
		return nil, err
	}
	return &Repo{Dir: dir, Settings: s, run: run}, nil
}

// ErrNoSettings is returned for a flake without a `careen` output, which
// only add, keys and secrets need.
var ErrNoSettings = errors.New("the flake has no `careen` output (add one for add, keys and secrets; see Flake settings in the docs)")

// LoadSettings reads the `careen` output of the flake at ref, which may be
// a checkout or any flake reference.
func LoadSettings(ctx context.Context, run execx.Runner, ref string) (Settings, error) {
	var s Settings
	out, err := run(ctx, "nix", "eval", "--json", ref+"#careen")
	if err != nil {
		if strings.Contains(err.Error(), "does not provide attribute") {
			return s, ErrNoSettings
		}
		return s, fmt.Errorf("reading the flake's careen settings: %w", err)
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return s, fmt.Errorf("parsing the flake's careen settings: %w", err)
	}
	if s.AnchorSuffix == "" {
		s.AnchorSuffix = DefaultAnchorSuffix
	}
	if s.PlaceholderPrefix == "" {
		s.PlaceholderPrefix = DefaultPlaceholderPrefix
	}
	for name, v := range map[string]string{"hostTemplate": s.HostTemplate, "hostDir": s.HostDir, "secretsDir": s.SecretsDir, "sopsConfig": s.SopsConfig} {
		if v == "" {
			return s, fmt.Errorf("the flake's careen settings lack %s", name)
		}
	}
	return s, nil
}

// Path is rel inside the checkout.
func (r *Repo) Path(rel string) string { return filepath.Join(r.Dir, rel) }

// Anchor is host's key anchor in .sops.yaml.
func (s Settings) Anchor(host string) string { return host + s.AnchorSuffix }

// AnchorHost is the host an anchor belongs to, if it's a host anchor.
func (s Settings) AnchorHost(anchor string) (string, bool) {
	return strings.CutSuffix(anchor, s.AnchorSuffix)
}

// Placeholder is the key recorded for host until its real key is known.
func (s Settings) Placeholder(host string) string {
	return s.PlaceholderPrefix + strings.ToUpper(host)
}

// IsPlaceholder reports whether key is a placeholder rather than a real key.
func (s Settings) IsPlaceholder(key string) bool { return strings.HasPrefix(key, s.PlaceholderPrefix) }

func (r *Repo) HostModule(host string) string {
	return filepath.Join(r.Settings.HostDir, host, host+".nix")
}
func (r *Repo) SecretsDir(host string) string { return filepath.Join(r.Settings.SecretsDir, host) }

// The pre-generated SSH host key, for hosts careen provisions: a sops file
// in the host's secrets directory. The public half stays readable (sops
// leaves names ending in _unencrypted alone) so careen can check it
// without the master key.
const (
	HostKeyPrivate = "ssh_host_ed25519_key"
	HostKeyPublic  = "public_key_unencrypted"
)

// HostKeyFile is where host's pre-generated key is kept, relative to the
// repo.
func (s Settings) HostKeyFile(host string) string {
	return filepath.Join(s.SecretsDir, host, "host-key.yaml")
}
func (r *Repo) HostKeyFile(host string) string { return r.Settings.HostKeyFile(host) }
func (r *Repo) HostRule(host string) string {
	return r.Settings.SecretsDir + "/" + host + `/.*\.yaml$`
}

// GroupDir and GroupRule are group's secrets directory and the path regex
// of its creation rule.
func (r *Repo) GroupDir(group string) string { return filepath.Join(r.Settings.GroupsDir, group) }
func (r *Repo) GroupRule(group string) string {
	return r.Settings.GroupsDir + "/" + group + `/.*\.yaml$`
}

// Sops loads the .sops.yaml.
func (r *Repo) Sops() (*sopsconf.Config, error) {
	b, err := os.ReadFile(r.Path(r.Settings.SopsConfig))
	if err != nil {
		return nil, err
	}
	return sopsconf.Parse(b)
}

// SaveSops writes c over the .sops.yaml.
func (r *Repo) SaveSops(c *sopsconf.Config) error {
	return os.WriteFile(r.Path(r.Settings.SopsConfig), c.Bytes(), 0o644)
}

// HostSecrets lists host's secrets files, relative to the repo.
func (r *Repo) HostSecrets(host string) []string {
	files, _ := filepath.Glob(filepath.Join(r.Path(r.SecretsDir(host)), "*.yaml"))
	return r.rel(files)
}

// SecretFiles lists every .yaml file under the top directory of
// secretsDir, relative to the repo: host files, shared files and files only
// some hosts share.
func (r *Repo) SecretFiles() []string {
	root := strings.SplitN(filepath.ToSlash(r.Settings.SecretsDir), "/", 2)[0]
	out := []string{}
	filepath.WalkDir(r.Path(root), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		rel, _ := filepath.Rel(r.Dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out
}

func (r *Repo) rel(paths []string) []string {
	out := []string{}
	for _, p := range paths {
		rel, err := filepath.Rel(r.Dir, p)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// Changed lists which of paths have uncommitted changes in git.
func (r *Repo) Changed(ctx context.Context, paths ...string) ([]string, error) {
	out, err := r.run(ctx, "git", append([]string{"-C", r.Dir, "status", "--porcelain", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(l) > 3 {
			changed = append(changed, strings.TrimSpace(l[3:]))
		}
	}
	return changed, nil
}

// IntentToAdd tells git about new files without staging their content, so
// a flake evaluation of the checkout sees them.
func (r *Repo) IntentToAdd(ctx context.Context, paths ...string) error {
	_, err := r.run(ctx, "git", append([]string{"-C", r.Dir, "add", "--intent-to-add", "--"}, paths...)...)
	return err
}

// EvalHost evaluates host's system in the checkout as it is now, without
// building it.
func (r *Repo) EvalHost(ctx context.Context, host string) error {
	_, err := r.run(ctx, "nix", "eval", "--raw", r.Dir+"#nixosConfigurations."+host+".config.system.build.toplevel.drvPath")
	return err
}

// HostInfo is what the checkout's flake already says about a host.
type HostInfo struct {
	// Defined is set when the flake has nixosConfigurations.<host>.
	Defined bool `json:"defined"`
	// Provisioner is its fleet entry's provisioner ("existing" when unset
	// or when the host isn't in the fleet).
	Provisioner string `json:"provisioner"`
	// Address is its fleet entry's address, "" when unset.
	Address string `json:"address"`
}

// HostFleet reads what the checkout's flake says about host: whether it
// defines the host's system, and the host's entry in the `fleet` output.
// Hosts that share a module with others (a cluster's node table) have no
// file of their own, so this asks the flake rather than looking for one.
func (r *Repo) HostFleet(ctx context.Context, host string) (HostInfo, error) {
	out, err := r.run(ctx, "nix", "eval", "--json", r.Dir+"#nixosConfigurations", "--apply", fmt.Sprintf("cs: cs ? %q", host))
	if err != nil {
		return HostInfo{}, err
	}
	i := HostInfo{Provisioner: "existing"}
	if err := json.Unmarshal(out, &i.Defined); err != nil {
		return HostInfo{}, fmt.Errorf("checking whether the flake defines %s: %w", host, err)
	}
	if !i.Defined {
		return i, nil
	}
	out, err = r.run(ctx, "nix", "eval", "--json", r.Dir+"#fleet", "--apply", fmt.Sprintf("f: f.%q or null", host))
	if err != nil {
		return HostInfo{}, err
	}
	var entry *struct {
		Address     string `json:"address"`
		Provisioner string `json:"provisioner"`
	}
	if err := json.Unmarshal(out, &entry); err != nil {
		return HostInfo{}, fmt.Errorf("parsing %s's fleet entry: %w", host, err)
	}
	if entry != nil {
		i.Address = entry.Address
		if entry.Provisioner != "" {
			i.Provisioner = entry.Provisioner
		}
	}
	return i, nil
}

// HostProvisioner reports whether the checkout already defines host's
// system, and its fleet entry's provisioner.
func (r *Repo) HostProvisioner(ctx context.Context, host string) (bool, string, error) {
	i, err := r.HostFleet(ctx, host)
	return i.Defined, i.Provisioner, err
}

// HostVars fill in the host template.
type HostVars struct {
	Name, Address, IP, Gateway, Interface string
	Vars                                  map[string]string
}

// RenderHost renders the flake's host template for v.
func (r *Repo) RenderHost(v HostVars) ([]byte, error) {
	src, err := os.ReadFile(r.Path(r.Settings.HostTemplate))
	if err != nil {
		return nil, err
	}
	t, err := template.New(r.Settings.HostTemplate).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, err
	}
	if v.Vars == nil {
		v.Vars = map[string]string{}
	}
	var b bytes.Buffer
	if err := t.Execute(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
