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

type SecretsReport struct {
	contract.Header
	Hosts  []HostSecrets `json:"hosts"`
	Shared []SecretFile  `json:"shared"`
}

// Secrets lists the secrets of the hosts in .sops.yaml (or only those
// named), and the files hosts share (every file under the secrets root
// that a creation rule covers and that isn't a host's own), without
// decrypting anything.
func Secrets(rp *repo.Repo, only []string) (SecretsReport, error) {
	conf, err := rp.Sops()
	if err != nil {
		return SecretsReport{}, err
	}
	rep := SecretsReport{Header: contract.NewHeader(SecretsSchema), Hosts: []HostSecrets{}, Shared: []SecretFile{}}
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

// SecretPath is host's file (DefaultSecretsFile if empty), relative to the
// repo.
func SecretPath(rp *repo.Repo, host, file string) (string, error) {
	file = cmpOr(file, DefaultSecretsFile)
	if filepath.Base(file) != file || filepath.Ext(file) != ".yaml" {
		return "", contract.Usage("secrets file must be a plain name ending in .yaml, not %q", file)
	}
	return filepath.ToSlash(filepath.Join(rp.SecretsDir(host), file)), nil
}

// SetSecret sets name in host's file. A new file is created holding just
// it, which needs only public keys; changing an existing file needs the
// master key. The value is given to sops on stdin only.
func SetSecret(ctx context.Context, rp *repo.Repo, host, file, name, value string) (created bool, err error) {
	if !nameRE.MatchString(name) {
		return false, contract.Usage("secret names are letters, digits, _ . and -, not %q", name)
	}
	path, err := SecretPath(rp, host, file)
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

// UnsetSecret removes name from host's file.
func UnsetSecret(ctx context.Context, rp *repo.Repo, host, file, name string) error {
	path, err := SecretPath(rp, host, file)
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

// EditSecrets is the command that opens host's file in $EDITOR through
// sops, creating its directory first; the caller runs it in a terminal.
func EditSecrets(ctx context.Context, rp *repo.Repo, host, file string) (*exec.Cmd, string, error) {
	path, err := SecretPath(rp, host, file)
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
