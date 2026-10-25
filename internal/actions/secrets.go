package actions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/sopsconf"
	"github.com/ryanmwright/careen/internal/sopsfile"
)

const SecretsSchema = "careen.secrets/v1"

// DefaultSecretsFile is the file a host's secrets go in unless told
// otherwise; sops-nix reads it as the host's default.
const DefaultSecretsFile = "secrets.yaml"

// SecretFile is one encrypted file, as far as it can be read without
// decrypting it.
type SecretFile struct {
	Path         string    `json:"path"`
	Names        []string  `json:"names"`
	LastModified time.Time `json:"lastModified"`
	// Missing lists hosts whose recorded key the file isn't encrypted to.
	Missing []string        `json:"missing"`
	Error   *contract.Error `json:"error,omitempty"`
}

type HostSecrets struct {
	Host  string       `json:"host"`
	Files []SecretFile `json:"files"`
}

// GroupSecrets is a secret group: its hosts and its files.
type GroupSecrets struct {
	Group string       `json:"group"`
	Hosts []string     `json:"hosts"`
	Files []SecretFile `json:"files"`
}

type SecretsReport struct {
	contract.Header
	Hosts  []HostSecrets  `json:"hosts"`
	Groups []GroupSecrets `json:"groups"`
	Shared []SecretFile   `json:"shared"`
}

// Secrets lists the secrets of the hosts in .sops.yaml (or only those
// named), of the secret groups (those with a named host, if any are
// named), and the other files hosts share (every file under the secrets
// root that a creation rule covers and that isn't a host's or a group's),
// without decrypting anything.
func Secrets(rp *repo.Repo, only []string) (SecretsReport, error) {
	conf, err := rp.Sops()
	if err != nil {
		return SecretsReport{}, err
	}
	rep := SecretsReport{Header: contract.NewHeader(SecretsSchema), Hosts: []HostSecrets{}, Groups: []GroupSecrets{}, Shared: []SecretFile{}}
	describe := func(f string) SecretFile {
		sf := SecretFile{Path: f, Names: []string{}, Missing: []string{}}
		m, err := sopsfile.ReadMeta(rp.Path(f))
		if err != nil {
			e := contract.ErrorObject(err)
			sf.Error = &e
			return sf
		}
		sf.Names, sf.LastModified = m.Names, m.LastModified
		for _, r := range conf.Rules {
			if !matches(r.PathRegex, f) {
				continue
			}
			for _, a := range r.Age {
				if k, ok := conf.Key(a); ok && !rp.Settings.IsPlaceholder(k.Value) && !slices.Contains(m.AgeRecipients, k.Value) {
					sf.Missing = append(sf.Missing, trimHost(rp.Settings, a))
				}
			}
			break
		}
		return sf
	}
	own := map[string]bool{}
	for _, k := range conf.Keys {
		host, ok := rp.Settings.AnchorHost(k.Anchor)
		if !ok {
			continue
		}
		files := rp.HostSecrets(host)
		for _, f := range files {
			own[f] = true
		}
		if len(only) > 0 && !slices.Contains(only, host) {
			continue
		}
		hs := HostSecrets{Host: host, Files: []SecretFile{}}
		for _, f := range files {
			hs.Files = append(hs.Files, describe(f))
		}
		rep.Hosts = append(rep.Hosts, hs)
	}
	for _, g := range groups(rp, conf) {
		files := groupFiles(rp, g.name)
		for _, f := range files {
			own[f] = true
		}
		if len(only) > 0 && !slices.ContainsFunc(only, func(h string) bool { return slices.Contains(g.hosts, h) }) {
			continue
		}
		gs := GroupSecrets{Group: g.name, Hosts: g.hosts, Files: []SecretFile{}}
		for _, f := range files {
			gs.Files = append(gs.Files, describe(f))
		}
		rep.Groups = append(rep.Groups, gs)
	}
	for _, f := range rp.SecretFiles() {
		if !own[f] && hasRule(conf, f) {
			rep.Shared = append(rep.Shared, describe(f))
		}
	}
	return rep, nil
}

// hasRule reports whether a creation rule covers file.
func hasRule(conf *sopsconf.Config, file string) bool {
	return slices.ContainsFunc(conf.Rules, func(r sopsconf.Rule) bool { return matches(r.PathRegex, file) })
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// SecretTarget is whose secrets a command changes: a host's, or a
// secret group's when Group is set.
type SecretTarget struct {
	Host  string
	Group string
}

// String names the target in messages: "web1's secrets" or "group backup".
func (t SecretTarget) String() string {
	if t.Group != "" {
		return "group " + t.Group
	}
	return t.Host + "'s secrets"
}

// SecretPath is the target's file (DefaultSecretsFile if empty), relative
// to the repo. A group must exist.
func SecretPath(rp *repo.Repo, t SecretTarget, file string) (string, error) {
	file = cmpOr(file, DefaultSecretsFile)
	if filepath.Base(file) != file || filepath.Ext(file) != ".yaml" {
		return "", contract.Usage("secrets file must be a plain name ending in .yaml, not %q", file)
	}
	dir := rp.SecretsDir(t.Host)
	if t.Group != "" {
		if err := checkGroup(rp, t.Group); err != nil {
			return "", err
		}
		conf, err := rp.Sops()
		if err != nil {
			return "", err
		}
		if _, ok := conf.Rule(rp.GroupRule(t.Group)); !ok {
			return "", noGroup(rp, t.Group)
		}
		dir = rp.GroupDir(t.Group)
	}
	return filepath.ToSlash(filepath.Join(dir, file)), nil
}

// SetSecret sets name in the target's file. A new file is created holding just
// it, which needs only public keys; changing an existing file needs the
// master key. The value is given to sops on stdin only.
func SetSecret(ctx context.Context, rp *repo.Repo, t SecretTarget, file, name, value string) (created bool, err error) {
	if !nameRE.MatchString(name) {
		return false, contract.Usage("secret names are letters, digits, _ . and -, not %q", name)
	}
	path, err := SecretPath(rp, t, file)
	if err != nil {
		return false, err
	}
	if _, err := rp.Sops(); err != nil {
		return false, err
	}
	s := sopsfile.Sops{Dir: rp.Dir}
	if _, err := os.Stat(rp.Path(path)); os.IsNotExist(err) {
		if err := s.Create(ctx, path, name, value); err != nil {
			return false, contract.Failed("secrets", "%v", err)
		}
		return true, rp.IntentToAdd(ctx, path)
	}
	if err := MasterKey(ctx, rp); err != nil {
		return false, err
	}
	if err := s.Set(ctx, path, name, value); err != nil {
		return false, contract.Failed("secrets", "%v", err)
	}
	return false, nil
}

// UnsetSecret removes name from the target's file.
func UnsetSecret(ctx context.Context, rp *repo.Repo, t SecretTarget, file, name string) error {
	path, err := SecretPath(rp, t, file)
	if err != nil {
		return err
	}
	if err := MasterKey(ctx, rp); err != nil {
		return err
	}
	if err := (sopsfile.Sops{Dir: rp.Dir}).Unset(ctx, path, name); err != nil {
		return contract.Failed("secrets", "%v", err)
	}
	return nil
}

// EditSecrets is the command that opens the target's file in $EDITOR
// through sops, creating its directory first; the caller runs it in a
// terminal.
func EditSecrets(ctx context.Context, rp *repo.Repo, t SecretTarget, file string) (*exec.Cmd, string, error) {
	path, err := SecretPath(rp, t, file)
	if err != nil {
		return nil, "", err
	}
	if err := MasterKey(ctx, rp); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(rp.Path(path)), 0o755); err != nil {
		return nil, "", err
	}
	return (sopsfile.Sops{Dir: rp.Dir}).Edit(path), path, nil
}

// trimHost is the host an anchor belongs to, or the anchor itself.
func trimHost(s repo.Settings, anchor string) string {
	host, _ := s.AnchorHost(anchor)
	return host
}
