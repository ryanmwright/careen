package actions

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/repo"
	"github.com/ryanmwright/careen/internal/sopsconf"
)

// A secret group is a directory under groupsDir whose files are encrypted
// to the master keys and some hosts. It's only a creation rule for
// <groupsDir>/NAME/.*\.yaml$ in .sops.yaml: the rule's host anchors are its
// hosts, so a rule written by hand is a group too.

type group struct {
	name  string
	hosts []string
}

// groups lists the groups .sops.yaml has rules for, in its order.
func groups(rp *repo.Repo, conf *sopsconf.Config) []group {
	if rp.Settings.GroupsDir == "" {
		return nil
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(rp.Settings.GroupsDir+"/") + `([A-Za-z0-9_.-]+)` + regexp.QuoteMeta(`/.*\.yaml$`) + `$`)
	var out []group
	for _, r := range conf.Rules {
		m := re.FindStringSubmatch(r.PathRegex)
		if m == nil || checkGroup(rp, m[1]) != nil {
			continue
		}
		g := group{name: m[1], hosts: []string{}}
		for _, a := range r.Age {
			if h, ok := rp.Settings.AnchorHost(a); ok {
				g.hosts = append(g.hosts, h)
			}
		}
		out = append(out, g)
	}
	return out
}

// groupFiles lists group's files, relative to the repo.
func groupFiles(rp *repo.Repo, group string) []string {
	files, _ := filepath.Glob(filepath.Join(rp.Path(rp.GroupDir(group)), "*.yaml"))
	out := []string{}
	for _, f := range files {
		if rel, err := filepath.Rel(rp.Dir, f); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// checkGroup checks that groups are set up and that group is a usable
// name: not one whose directory holds the hosts' secrets.
func checkGroup(rp *repo.Repo, group string) error {
	if rp.Settings.GroupsDir == "" {
		return contract.Usage(`secret groups need groupsDir in the flake's careen settings (for example "secrets")`)
	}
	if !nameRE.MatchString(group) || group == "." || group == ".." {
		return contract.Usage("group names are letters, digits, _ . and -, not %q", group)
	}
	dir := filepath.ToSlash(rp.GroupDir(group)) + "/"
	if strings.HasPrefix(filepath.ToSlash(rp.Settings.SecretsDir)+"/", dir) {
		return contract.Usage("%q holds the hosts' secrets (secretsDir); pick another group name", group)
	}
	return nil
}

func noGroup(rp *repo.Repo, group string) error {
	return contract.Usage("no group %s: .sops.yaml has no rule for %s (careen secrets group create %s)", group, rp.GroupRule(group), group)
}

// hostAnchor is host's anchor, which must be in .sops.yaml.
func hostAnchor(rp *repo.Repo, conf *sopsconf.Config, host string) (string, error) {
	a := rp.Settings.Anchor(host)
	if _, ok := conf.Key(a); !ok {
		return "", contract.Usage("%s has no key in .sops.yaml (&%s); onboard it first with careen add %s", host, a, host)
	}
	return a, nil
}

// GroupCreate adds group's creation rule: the master keys and hosts. Its
// files are made by careen secrets set or edit.
func GroupCreate(rp *repo.Repo, group string, hosts []string) error {
	if err := checkGroup(rp, group); err != nil {
		return err
	}
	conf, err := rp.Sops()
	if err != nil {
		return err
	}
	rule := rp.GroupRule(group)
	if _, ok := conf.Rule(rule); ok {
		return contract.Usage("group %s exists; add hosts with careen secrets group add %s HOST", group, group)
	}
	age := slices.Clone(rp.Settings.MasterKeys.Age)
	for _, h := range hosts {
		a, err := hostAnchor(rp, conf, h)
		if err != nil {
			return err
		}
		if !slices.Contains(age, a) {
			age = append(age, a)
		}
	}
	if err := conf.AddRule(rule, rp.Settings.MasterKeys.PGP, age, nil); err != nil {
		return err
	}
	return rp.SaveSops(conf)
}

// GroupAdd adds hosts to group and re-keys its files, unless a host isn't
// registered yet: its registration re-keys them then.
func GroupAdd(ctx context.Context, em *contract.Emitter, rp *repo.Repo, group string, hosts []string) error {
	return changeGroup(ctx, em, rp, group, hosts, true)
}

// GroupRemove removes hosts from group and re-keys its files. The hosts
// can still read every value they could before, from the repo's history.
func GroupRemove(ctx context.Context, em *contract.Emitter, rp *repo.Repo, group string, hosts []string) error {
	return changeGroup(ctx, em, rp, group, hosts, false)
}

func changeGroup(ctx context.Context, em *contract.Emitter, rp *repo.Repo, group string, hosts []string, join bool) error {
	if err := checkGroup(rp, group); err != nil {
		return err
	}
	conf, err := rp.Sops()
	if err != nil {
		return err
	}
	rule := rp.GroupRule(group)
	if _, ok := conf.Rule(rule); !ok {
		return noGroup(rp, group)
	}
	for _, h := range hosts {
		a, err := hostAnchor(rp, conf, h)
		if err != nil {
			return err
		}
		if join {
			err = conf.JoinRule(rule, a)
		} else {
			err = conf.LeaveRule(rule, a)
		}
		if err != nil {
			return err
		}
	}

	// sops can't encrypt to a placeholder, so files are re-keyed only once
	// every host in the group is registered.
	files := groupFiles(rp, group)
	r, _ := conf.Rule(rule)
	var pending []string
	for _, a := range r.Age {
		if k, ok := conf.Key(a); ok && rp.Settings.IsPlaceholder(k.Value) {
			pending = append(pending, trimHost(rp.Settings, a))
		}
	}
	switch {
	case len(files) == 0:
	case len(pending) > 0 && !join:
		return contract.Precondition("rekey", "can't re-key group %s while %s isn't registered (careen keys register HOST)", group, strings.Join(pending, ", "))
	case len(pending) > 0:
		em.Progress(group, "rekey", "%s isn't registered yet; careen keys register re-keys the group's files", strings.Join(pending, ", "))
		files = nil
	default:
		// Check before changing .sops.yaml, so it never runs ahead of the files.
		if err := MasterKey(ctx, rp); err != nil {
			return err
		}
	}
	if err := rp.SaveSops(conf); err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	return Rekey(ctx, em, rp, group, files)
}
