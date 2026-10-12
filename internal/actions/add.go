package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/hostkey"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/sopsconf"
	"github.com/ryanmwright/careen/internal/sopsfile"
)

const AddSchema = "careen.add/v1"

// Add steps, in order.
const (
	StepModule  = "module"  // the host module, from the flake's template
	StepSops    = "sops"    // .sops.yaml: anchor, creation rule, shared rules
	StepSecrets = "secrets" // the host's secrets directory
	StepHostKey = "hostkey" // a pre-generated host key, for hosts careen provisions
	StepKey     = "key"     // the host's live key recorded, files re-keyed
	StepEval    = "eval"    // the host's system evaluates
)

// Step states.
const (
	StepDone    = "done"
	StepTodo    = "todo"
	StepWaiting = "waiting" // can't run yet, e.g. the host isn't up
	StepFailed  = "failed"
	StepUnknown = "unknown" // only known by running it
)

type AddStep struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// AddStatus is where onboarding a host stands; careen.add/v1.
type AddStatus struct {
	contract.Header
	Host  string    `json:"host"`
	Steps []AddStep `json:"steps"`
	// Files are the paths the onboarding writes, relative to the repo.
	Files []string `json:"files"`
}

func (s AddStatus) step(id string) *AddStep {
	for i := range s.Steps {
		if s.Steps[i].ID == id {
			return &s.Steps[i]
		}
	}
	return nil
}

// Done reports whether every step is done.
func (s AddStatus) Done() bool {
	return !slices.ContainsFunc(s.Steps, func(st AddStep) bool { return st.State != StepDone })
}

var hostNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// AddOptions describe the host. Address is where it's reached; the rest
// fill in the host template.
type AddOptions struct {
	Vars repo.HostVars
	// PregenKey makes the host's SSH host key now instead of reading it from
	// the running host, so its secrets can exist before it does. It's
	// automatic for hosts whose provisioner isn't "existing".
	PregenKey bool
}

// AddStatusFor works out each step's state from the checkout and, for the
// key, the host itself. It changes nothing.
func AddStatusFor(ctx context.Context, rp *repo.Repo, name, address string) (AddStatus, error) {
	return AddStatusForOpts(ctx, rp, name, address, false)
}

// AddStatusForOpts is AddStatusFor, with pregen forcing a pre-generated
// host key.
func AddStatusForOpts(ctx context.Context, rp *repo.Repo, name, address string, pregen bool) (AddStatus, error) {
	if !hostNameRE.MatchString(name) {
		return AddStatus{}, contract.Usage("host names are lower-case letters, digits and -, starting with a letter, not %q", name)
	}
	conf, err := rp.Sops()
	if err != nil {
		return AddStatus{}, err
	}
	defined, prov, err := rp.HostProvisioner(ctx, name)
	if err != nil {
		return AddStatus{}, contract.Failed("eval", "reading %s from the flake: %v", name, err)
	}
	pregen = pregen || (defined && prov != inventory.Existing)
	anchor := rp.Settings.Anchor(name)
	st := AddStatus{Header: contract.NewHeader(AddSchema), Host: name,
		Files: []string{rp.HostModule(name), rp.Settings.SopsConfig, filepath.ToSlash(filepath.Join(rp.SecretsDir(name), ".gitkeep"))}}
	add := func(id, title, state, detail string) {
		st.Steps = append(st.Steps, AddStep{ID: id, Title: title, State: state, Detail: detail})
	}

	switch {
	case exists(rp.Path(rp.HostModule(name))):
		add(StepModule, "host module", StepDone, rp.HostModule(name))
	case defined:
		add(StepModule, "host module", StepDone, "nixosConfigurations."+name+" is already in the flake")
	default:
		add(StepModule, "host module", StepTodo, "write "+rp.HostModule(name)+" from "+rp.Settings.HostTemplate)
	}

	k, hasKey := conf.Key(anchor)
	_, hasRule := conf.Rule(rp.HostRule(name))
	joined := true
	for _, sr := range rp.Settings.SharedRules {
		if r, ok := conf.Rule(sr); ok && !slices.Contains(r.Age, anchor) {
			joined = false
		}
	}
	switch {
	case hasKey && hasRule && joined:
		add(StepSops, ".sops.yaml entries", StepDone, "&"+anchor+", its rule and the shared rules")
	default:
		add(StepSops, ".sops.yaml entries", StepTodo, "add &"+anchor+", a rule for "+rp.SecretsDir(name)+", and join the shared rules")
	}

	if exists(rp.Path(rp.SecretsDir(name))) {
		add(StepSecrets, "secrets directory", StepDone, rp.SecretsDir(name))
	} else {
		add(StepSecrets, "secrets directory", StepTodo, "create "+rp.SecretsDir(name))
	}

	if pregen {
		hostKeySteps(&st, rp, conf, name)
		add(StepEval, "system evaluates", StepUnknown, "nix eval of the host's system")
		return st, nil
	}

	address = cmpOr(address, name)
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	live, scanErr := agekey.Scan(sctx, address, 0)
	cancel()
	switch {
	case hasKey && !rp.Settings.IsPlaceholder(k.Value) && live == k.Value:
		add(StepKey, "host key registered", StepDone, k.Value)
	case scanErr != nil:
		add(StepKey, "host key registered", StepWaiting, "waiting for "+address+" to answer on SSH; run again once it's up")
	case hasKey && !rp.Settings.IsPlaceholder(k.Value) && live != k.Value:
		add(StepKey, "host key registered", StepFailed, "live key "+live+" differs from the recorded "+k.Value+"; use careen keys register --replace")
	default:
		add(StepKey, "host key registered", StepTodo, "record "+live+" and re-key the shared files")
	}

	add(StepEval, "system evaluates", StepUnknown, "nix eval of the host's system")
	return st, nil
}

// Add onboards name: each step that isn't done yet runs, in order. It
// stops at a step that can't run (waiting or failed) after doing every
// step before it, and returns the status either way. Nothing is
// committed.
func Add(ctx context.Context, em *contract.Emitter, rp *repo.Repo, o AddOptions) (AddStatus, error) {
	name := o.Vars.Name
	st, err := AddStatusForOpts(ctx, rp, name, o.Vars.Address, o.PregenKey)
	if err != nil {
		return st, err
	}
	em.Started("add", st)
	anchor := rp.Settings.Anchor(name)

	if s := st.step(StepModule); s.State == StepTodo {
		em.Progress(name, StepModule, "writing %s", rp.HostModule(name))
		src, err := rp.RenderHost(o.Vars)
		if err != nil {
			return failStep(st, StepModule, fmt.Errorf("rendering %s: %w", rp.Settings.HostTemplate, err))
		}
		path := rp.Path(rp.HostModule(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return failStep(st, StepModule, err)
		}
		if err := os.WriteFile(path, src, 0o644); err != nil {
			return failStep(st, StepModule, err)
		}
		if err := rp.IntentToAdd(ctx, rp.HostModule(name)); err != nil {
			return failStep(st, StepModule, err)
		}
		s.State, s.Detail = StepDone, "wrote "+rp.HostModule(name)
	}

	if s := st.step(StepSops); s.State == StepTodo {
		em.Progress(name, StepSops, "adding &%s and its rules to %s", anchor, rp.Settings.SopsConfig)
		conf, err := rp.Sops()
		if err != nil {
			return failStep(st, StepSops, err)
		}
		if _, ok := conf.Key(anchor); !ok {
			if err := conf.SetKey(anchor, rp.Settings.Placeholder(name)); err != nil {
				return failStep(st, StepSops, err)
			}
		}
		if err := conf.AddRule(rp.HostRule(name), rp.Settings.MasterKeys.PGP, append(slices.Clone(rp.Settings.MasterKeys.Age), anchor), rp.Settings.SharedRules); err != nil {
			return failStep(st, StepSops, err)
		}
		for _, sr := range rp.Settings.SharedRules {
			if _, ok := conf.Rule(sr); ok {
				if err := conf.JoinRule(sr, anchor); err != nil {
					return failStep(st, StepSops, err)
				}
			}
		}
		if err := rp.SaveSops(conf); err != nil {
			return failStep(st, StepSops, err)
		}
		s.State, s.Detail = StepDone, "added &"+anchor+", its rule, and joined the shared rules"
	}

	if s := st.step(StepSecrets); s.State == StepTodo {
		em.Progress(name, StepSecrets, "creating %s", rp.SecretsDir(name))
		keep := filepath.Join(rp.SecretsDir(name), ".gitkeep")
		if err := os.MkdirAll(rp.Path(rp.SecretsDir(name)), 0o755); err != nil {
			return failStep(st, StepSecrets, err)
		}
		if err := os.WriteFile(rp.Path(keep), nil, 0o644); err != nil {
			return failStep(st, StepSecrets, err)
		}
		if err := rp.IntentToAdd(ctx, keep); err != nil {
			return failStep(st, StepSecrets, err)
		}
		s.State, s.Detail = StepDone, "created "+rp.SecretsDir(name)
	}

	if s := st.step(StepHostKey); s != nil && s.State == StepTodo {
		em.Progress(name, StepHostKey, "generating an SSH host key into %s", rp.HostKeyFile(name))
		if err := generateHostKey(ctx, rp, name); err != nil {
			return failStep(st, StepHostKey, err)
		}
		s.State, s.Detail = StepDone, "generated "+rp.HostKeyFile(name)
		conf, err := rp.Sops()
		if err != nil {
			return failStep(st, StepKey, err)
		}
		// The key step's state depends on the key that now exists.
		var redo AddStatus
		hostKeySteps(&redo, rp, conf, name)
		if k := redo.step(StepKey); k != nil {
			*st.step(StepKey) = *k
		}
	} else if s != nil && s.State == StepFailed {
		em.Error(name, StepHostKey, errors.New(s.Detail))
		return st, contract.Failed("hostkey", "%s", s.Detail)
	}

	// The system should evaluate whether or not the host is up yet.
	if s := st.step(StepEval); s != nil {
		em.Progress(name, StepEval, "evaluating nixosConfigurations.%s", name)
		if err := rp.EvalHost(ctx, name); err != nil {
			s.State, s.Detail = StepFailed, err.Error()
			em.Error(name, StepEval, err)
			return st, contract.Failed("eval", "%s doesn't evaluate: %v", name, err)
		}
		s.State, s.Detail = StepDone, "nixosConfigurations."+name+" evaluates"
	}

	if st.step(StepHostKey) != nil {
		s := st.step(StepKey)
		if s.State == StepTodo {
			_, list, _ := strings.Cut(s.Detail, "re-key:")
			if err := Rekey(ctx, em, rp, name, strings.Fields(list)); err != nil {
				s.State, s.Detail = StepFailed, err.Error()
				if contract.ExitCode(err) == contract.ExitPrecondition {
					em.Decision(name, StepKey, err.Error(), "run careen add again")
				}
				return st, err
			}
			s.State, s.Detail = StepDone, "recorded the generated key; files re-keyed"
		}
		em.Done(st)
		return st, nil
	}

	switch s := st.step(StepKey); s.State {
	case StepWaiting:
		em.Progress(name, StepKey, "%s", s.Detail)
		em.Done(st)
		return st, contract.Precondition("waiting", "%s", s.Detail)
	case StepFailed:
		em.Error(name, StepKey, errors.New(s.Detail))
		return st, contract.Failed("drift", "%s", s.Detail)
	case StepTodo:
		if err := RegisterKey(ctx, em, rp, name, RegisterOptions{Address: o.Vars.Address}); err != nil {
			s.State, s.Detail = StepFailed, err.Error()
			if contract.ExitCode(err) == contract.ExitPrecondition {
				em.Decision(name, StepKey, err.Error(), "run careen add again")
			}
			return st, err
		}
		conf, _ := rp.Sops()
		k, _ := conf.Key(anchor)
		s.State, s.Detail = StepDone, "recorded "+k.Value+"; files re-keyed"
	}
	em.Done(st)
	return st, nil
}

// hostKeySteps adds the hostkey and key steps for a host whose key careen
// makes: hostkey is done once its sops file exists; key is done once the
// anchor holds that key's recipient and every file the host should open
// is encrypted to it. The key step's detail ends with the files that still
// need re-keying, after a colon.
func hostKeySteps(st *AddStatus, rp *repo.Repo, conf *sopsconf.Config, name string) {
	add := func(id, title, state, detail string) {
		st.Steps = append(st.Steps, AddStep{ID: id, Title: title, State: state, Detail: detail})
	}
	file := rp.HostKeyFile(name)
	if !exists(rp.Path(file)) {
		add(StepHostKey, "host key generated", StepTodo, "generate an ed25519 key into "+file+" and record its recipient")
		add(StepKey, "host key registered", StepTodo, "re-key the files "+name+" should open")
		return
	}
	pub, err := sopsfile.Plain(rp.Path(file), repo.HostKeyPublic)
	if err != nil {
		add(StepHostKey, "host key generated", StepFailed, err.Error())
		add(StepKey, "host key registered", StepUnknown, "needs the generated key")
		return
	}
	age, err := agekey.FromSSH(pub)
	if err != nil {
		add(StepHostKey, "host key generated", StepFailed, file+": "+err.Error())
		add(StepKey, "host key registered", StepUnknown, "needs the generated key")
		return
	}
	add(StepHostKey, "host key generated", StepDone, file)

	anchor := rp.Settings.Anchor(name)
	k, ok := conf.Key(anchor)
	switch {
	case !ok || rp.Settings.IsPlaceholder(k.Value):
		add(StepKey, "host key registered", StepFailed, "&"+anchor+" doesn't hold "+age+" from "+file)
		return
	case k.Value != age:
		add(StepKey, "host key registered", StepFailed, "&"+anchor+" is "+k.Value+" but "+file+" holds "+age+"; one of them was replaced")
		return
	}
	var missing []string
	files := rp.HostSecrets(name)
	for _, f := range rp.SharedSecrets() {
		if fileRuleIncludes(conf, f, anchor) {
			files = append(files, f)
		}
	}
	for _, f := range files {
		m, err := sopsfile.ReadMeta(rp.Path(f))
		if err == nil && !slices.Contains(m.AgeRecipients, age) {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		add(StepKey, "host key registered", StepDone, age)
		return
	}
	add(StepKey, "host key registered", StepTodo, "re-key: "+strings.Join(missing, " "))
}

// generateHostKey makes name's host key, records its recipient under the
// host's anchor and writes the key into the host's sops file, encrypted to
// the master keys and the host. Only public keys are needed.
func generateHostKey(ctx context.Context, rp *repo.Repo, name string) error {
	conf, err := rp.Sops()
	if err != nil {
		return err
	}
	anchor := rp.Settings.Anchor(name)
	if k, ok := conf.Key(anchor); ok && !rp.Settings.IsPlaceholder(k.Value) {
		return fmt.Errorf("&%s already holds %s; remove it (or the host's old key) before generating a new one", anchor, k.Value)
	}
	key, err := hostkey.Generate("root@" + name)
	if err != nil {
		return err
	}
	age, err := agekey.FromSSH(key.Public)
	if err != nil {
		return err
	}
	if err := conf.SetKey(anchor, age); err != nil {
		return err
	}
	if err := rp.SaveSops(conf); err != nil {
		return err
	}
	file := rp.HostKeyFile(name)
	s := sopsfile.Sops{Dir: rp.Dir}
	if err := s.CreateValues(ctx, file, map[string]string{repo.HostKeyPrivate: key.Private, repo.HostKeyPublic: key.Public}); err != nil {
		return err
	}
	return rp.IntentToAdd(ctx, file)
}

func failStep(st AddStatus, id string, err error) (AddStatus, error) {
	if s := st.step(id); s != nil {
		s.State, s.Detail = StepFailed, err.Error()
	}
	return st, contract.Failed(id, "%v", err)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// AddPreview is what Add would write, without writing it.
type AddPreview struct {
	Module    string   `json:"module"`    // the rendered host module
	SopsLines []string `json:"sopsLines"` // .sops.yaml's changed lines, "+ " added, "- " removed
}

// PreviewAdd renders the host module and applies the .sops.yaml edits in
// memory.
func PreviewAdd(rp *repo.Repo, v repo.HostVars) (AddPreview, error) {
	var p AddPreview
	src, err := rp.RenderHost(v)
	if err != nil {
		return p, fmt.Errorf("rendering %s: %w", rp.Settings.HostTemplate, err)
	}
	p.Module = string(src)
	conf, err := rp.Sops()
	if err != nil {
		return p, err
	}
	before := slices.Clone(splitLines(conf.Bytes()))
	anchor := rp.Settings.Anchor(v.Name)
	if _, ok := conf.Key(anchor); !ok {
		if err := conf.SetKey(anchor, rp.Settings.Placeholder(v.Name)); err != nil {
			return p, err
		}
	}
	if err := conf.AddRule(rp.HostRule(v.Name), rp.Settings.MasterKeys.PGP, append(slices.Clone(rp.Settings.MasterKeys.Age), anchor), rp.Settings.SharedRules); err != nil {
		return p, err
	}
	for _, sr := range rp.Settings.SharedRules {
		if _, ok := conf.Rule(sr); ok {
			if err := conf.JoinRule(sr, anchor); err != nil {
				return p, err
			}
		}
	}
	p.SopsLines = lineDiff(before, splitLines(conf.Bytes()))
	return p, nil
}

// lineDiff is the changed lines between a and b, "+ " added and "- "
// removed, from a longest-common-subsequence match. The files are small.
func lineDiff(a, b []string) []string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	return out
}

func splitLines(b []byte) []string { return strings.Split(string(b), "\n") }
