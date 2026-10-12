package actions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/sopsconf"
	"github.com/ryanmwright/careen/internal/sopsfile"
)

const KeysSchema = "careen.keys/v1"

// Key states, worst first.
const (
	KeyNoAnchor    = "no-anchor"   // in the inventory, but no key in .sops.yaml
	KeyPlaceholder = "placeholder" // .sops.yaml still holds age1REPLACEME_…
	KeyDrift       = "drift"       // the host's live key differs: it can't decrypt
	KeyNeedsRekey  = "needs-rekey" // some files aren't encrypted to the key yet
	KeyUnreachable = "unreachable" // couldn't fetch the live key
	KeyOK          = "ok"
)

// KeyState is one host's key, on record and live.
type KeyState struct {
	Host     string `json:"host"`
	Address  string `json:"address,omitempty"`
	Anchor   string `json:"anchor,omitempty"`
	Recorded string `json:"recorded,omitempty"` // the age key in .sops.yaml
	Live     string `json:"live,omitempty"`     // from the host's SSH host key
	State    string `json:"state"`
	// Files is every secrets file the host's key should open; NeedsRekey
	// is those not encrypted to its recorded key.
	Files      []string        `json:"files"`
	NeedsRekey []string        `json:"needsRekey"`
	Error      *contract.Error `json:"error,omitempty"`
}

type KeysReport struct {
	contract.Header
	Repo  string     `json:"repo"`
	Hosts []KeyState `json:"hosts"`
}

// Keys compares every host's key on record with its live host key and with
// the recipients of the files it should be able to open. Hosts come from
// the inventory (inv may be nil) and from the host anchors in .sops.yaml.
func Keys(ctx context.Context, rp *repo.Repo, inv *inventory.Inventory, only []string) (KeysReport, error) {
	conf, err := rp.Sops()
	if err != nil {
		return KeysReport{}, err
	}
	addr := map[string]string{}
	var names []string
	if inv != nil {
		for _, h := range inv.Hosts {
			names = append(names, h.Name)
			addr[h.Name] = h.Address
		}
	}
	for _, k := range conf.Keys {
		if host, ok := rp.Settings.AnchorHost(k.Anchor); ok && !slices.Contains(names, host) {
			names = append(names, host)
		}
	}
	if len(only) > 0 {
		for _, o := range only {
			if !slices.Contains(names, o) {
				return KeysReport{}, contract.Usage("unknown host %q", o)
			}
		}
		names = slices.DeleteFunc(names, func(n string) bool { return !slices.Contains(only, n) })
	}
	slices.Sort(names)

	shared := rp.SharedSecrets()
	states := make([]KeyState, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { states[i] = keyState(ctx, rp, conf, shared, name, addr[name]) })
	}
	wg.Wait()
	return KeysReport{Header: contract.NewHeader(KeysSchema), Repo: rp.Dir, Hosts: states}, nil
}

func keyState(ctx context.Context, rp *repo.Repo, conf *sopsconf.Config, shared []string, host, address string) KeyState {
	if address == "" {
		address = host
	}
	st := KeyState{Host: host, Address: address, Anchor: rp.Settings.Anchor(host), Files: []string{}, NeedsRekey: []string{}}
	k, ok := conf.Key(st.Anchor)
	if !ok {
		st.Anchor, st.State = "", KeyNoAnchor
		return st
	}
	st.Recorded = k.Value

	st.Files = append(st.Files, rp.HostSecrets(host)...)
	for _, f := range shared {
		if fileRuleIncludes(conf, f, st.Anchor) {
			st.Files = append(st.Files, f)
		}
	}
	if !rp.Settings.IsPlaceholder(k.Value) {
		for _, f := range st.Files {
			if m, err := sopsfile.ReadMeta(rp.Path(f)); err == nil && !slices.Contains(m.AgeRecipients, k.Value) {
				st.NeedsRekey = append(st.NeedsRekey, f)
			}
		}
	}

	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	live, err := agekey.Scan(sctx, address, 0)
	if err != nil {
		e := contract.ErrorObject(contract.Failed("unreachable", "%v", err))
		e.Host = host
		st.Error = &e
	}
	st.Live = live

	switch {
	case rp.Settings.IsPlaceholder(k.Value):
		st.State = KeyPlaceholder
	case live != "" && live != k.Value:
		st.State = KeyDrift
	case len(st.NeedsRekey) > 0:
		st.State = KeyNeedsRekey
	case live == "":
		st.State = KeyUnreachable
	default:
		st.State = KeyOK
	}
	return st
}

// fileRuleIncludes reports whether the creation rule that covers file (the
// first whose path_regex matches, as sops picks) lists anchor.
func fileRuleIncludes(conf *sopsconf.Config, file, anchor string) bool {
	for _, r := range conf.Rules {
		if matches(r.PathRegex, file) {
			return slices.Contains(r.Age, anchor)
		}
	}
	return false
}

// MasterKey checks that this machine can use the master keys that
// re-keying and editing need: PGP keys through gpg (a local key, a security
// key, or a forwarded gpg-agent) and age keys through the identities sops
// reads. It doesn't decrypt anything, so it never asks for a touch or PIN.
func MasterKey(ctx context.Context, rp *repo.Repo) error {
	conf, err := rp.Sops()
	if err != nil {
		return err
	}
	if pgp := rp.Settings.MasterKeys.PGP; len(pgp) > 0 {
		if _, err := exec.LookPath("gpg"); err != nil {
			return contract.Precondition("master-key", "gpg isn't installed here")
		}
		for _, a := range pgp {
			k, ok := conf.Key(a)
			if !ok {
				return contract.Failed("master-key", ".sops.yaml has no key &%s", a)
			}
			if err := exec.CommandContext(ctx, "gpg", "--list-secret-keys", "--with-colons", k.Value).Run(); err != nil {
				return contract.Precondition("master-key", "the master key %s (&%s) isn't available here: plug in or forward the key that holds it", k.Value, a)
			}
		}
	}
	if age := rp.Settings.MasterKeys.Age; len(age) > 0 {
		have, err := ageIdentities(ctx)
		if err != nil {
			return err
		}
		for _, a := range age {
			k, ok := conf.Key(a)
			if !ok {
				return contract.Failed("master-key", ".sops.yaml has no key &%s", a)
			}
			// Plugin recipients (age1yubikey1…) and identities sops gets
			// from a command can't be checked without using them.
			if have == nil || !agekey.IsRecipient(k.Value) {
				continue
			}
			if !slices.Contains(have, k.Value) {
				return contract.Precondition("master-key", "the master key %s (&%s) isn't among the age identities sops can use here (SOPS_AGE_KEY, SOPS_AGE_KEY_FILE or ~/.config/sops/age/keys.txt)", k.Value, a)
			}
		}
	}
	return nil
}

// ageIdentities lists the recipients of the age identities sops would use,
// from SOPS_AGE_KEY, SOPS_AGE_KEY_FILE and the default keys file. nil means
// they can't be listed without running something (SOPS_AGE_KEY_CMD,
// SOPS_AGE_SSH_PRIVATE_KEY_FILE), so the caller shouldn't judge.
func ageIdentities(ctx context.Context) ([]string, error) {
	if os.Getenv("SOPS_AGE_KEY_CMD") != "" || os.Getenv("SOPS_AGE_SSH_PRIVATE_KEY_FILE") != "" {
		return nil, nil
	}
	var sources []string // identity file contents
	if v := os.Getenv("SOPS_AGE_KEY"); v != "" {
		sources = append(sources, v)
	}
	files := []string{os.Getenv("SOPS_AGE_KEY_FILE")}
	if dir, err := os.UserConfigDir(); err == nil {
		files = append(files, filepath.Join(dir, "sops", "age", "keys.txt"))
	}
	for _, f := range files {
		if f == "" {
			continue
		}
		if b, err := os.ReadFile(f); err == nil {
			sources = append(sources, string(b))
		}
	}
	if len(sources) == 0 {
		return []string{}, nil
	}
	if _, err := exec.LookPath("age-keygen"); err != nil {
		return nil, contract.Precondition("master-key", "age-keygen isn't installed here, so the age master key can't be checked")
	}
	var have []string
	for _, src := range sources {
		cmd := exec.CommandContext(ctx, "age-keygen", "-y")
		cmd.Stdin = strings.NewReader(src)
		out, err := cmd.Output()
		if err != nil {
			continue // plugin identities and the like
		}
		have = append(have, strings.Fields(string(out))...)
	}
	return have, nil
}

// RegisterOptions say how to register a host's key.
type RegisterOptions struct {
	Address string // where to fetch the key; the host name if empty
	// Replace allows overwriting a key on record that differs from the
	// live one (after a reinstall). Without it a drift is an error.
	Replace bool
}

// RegisterKey records host's live key in .sops.yaml and re-keys the files
// it should open. The host must already have an anchor (careen add makes
// one).
func RegisterKey(ctx context.Context, em *contract.Emitter, rp *repo.Repo, host string, o RegisterOptions) error {
	address := cmpOr(o.Address, host)
	em.Progress(host, "key", "fetching the SSH host key from %s", address)
	live, err := agekey.Scan(ctx, address, 0)
	if err != nil {
		return contract.Precondition("unreachable", "%v", err)
	}
	conf, err := rp.Sops()
	if err != nil {
		return err
	}
	anchor := rp.Settings.Anchor(host)
	k, ok := conf.Key(anchor)
	switch {
	case !ok:
		return contract.Failed("no-anchor", "%s has no key &%s in .sops.yaml; onboard it with careen add", host, anchor)
	case k.Value == live:
		em.Progress(host, "key", "already recorded: %s", live)
	case !rp.Settings.IsPlaceholder(k.Value) && !o.Replace:
		return contract.Precondition("drift", "%s's live key %s differs from the recorded %s; pass --replace if it was reinstalled", host, live, k.Value)
	default:
		if err := conf.SetKey(anchor, live); err != nil {
			return err
		}
		if err := rp.SaveSops(conf); err != nil {
			return err
		}
		em.Progress(host, "key", "recorded %s in %s", live, rp.Settings.SopsConfig)
	}
	return RekeyHost(ctx, em, rp, host)
}

// RekeyHost re-keys host's files and the shared files, so they match
// .sops.yaml. It needs the master key.
func RekeyHost(ctx context.Context, em *contract.Emitter, rp *repo.Repo, host string) error {
	conf, err := rp.Sops()
	if err != nil {
		return err
	}
	files := rp.HostSecrets(host)
	for _, f := range rp.SharedSecrets() {
		if fileRuleIncludes(conf, f, rp.Settings.Anchor(host)) {
			files = append(files, f)
		}
	}
	return Rekey(ctx, em, rp, host, files)
}

// Rekey runs sops updatekeys on files.
func Rekey(ctx context.Context, em *contract.Emitter, rp *repo.Repo, host string, files []string) error {
	if len(files) == 0 {
		em.Progress(host, "rekey", "no secrets files to re-key")
		return nil
	}
	if err := MasterKey(ctx, rp); err != nil {
		return err
	}
	s := sopsfile.Sops{Dir: rp.Dir}
	for _, f := range files {
		em.Progress(host, "rekey", "sops updatekeys %s", f)
		if err := s.UpdateKeys(ctx, f); err != nil {
			return contract.Failed("rekey", "%s: %v", f, err)
		}
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func matches(re, s string) bool {
	ok, err := regexp.MatchString(re, s)
	return err == nil && ok
}
