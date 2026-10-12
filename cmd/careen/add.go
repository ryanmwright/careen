package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/repo"
)

func newAddCmd(g *globalFlags) *cobra.Command {
	var (
		vars                repo.HostVars
		sets                []string
		status, yes, deploy bool
		pregen              bool
		as                  string
	)
	cmd := &cobra.Command{
		Use:   "add [NAME...] [--address ADDRESS]",
		Short: "Onboard a host: module, sops entries, secrets dir, host key",
		Long: `Onboard a NixOS host into the flake checkout (--repo), leaving the changes
uncommitted for you to review:

  1. write <hostDir>/NAME/NAME.nix from the flake's host template
  2. add NAME's key anchor (a placeholder), creation rule and shared rules
     to .sops.yaml
  3. create NAME's secrets directory
  4. check that NAME's system evaluates
  5. when NAME answers on SSH: record its host key and re-key the files it
     should open (needs the master key)

Every step that's already done is skipped, so run it again to continue: for
example once before the VM exists (steps 1-4, exit 3 "waiting"), and again
after it has booted (step 5). --status shows where it stands.

Hosts careen provisions (provisioner = "proxmox" in the fleet), or any host with
--pregen-key, get their SSH host key made now instead: it's stored in
<secretsDir>/NAME/host-key.yaml, its recipient goes into .sops.yaml, and
careen up installs it. Onboarding then finishes before the machine exists.
A host already defined in the flake (say, in a cluster's node table) keeps
its module; no file is written for it, and its address comes from its
fleet entry's address, so --address is only needed for new hosts. Hosts the
flake defines can also be chosen with --only or --tag:

  careen add --tag cluster --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			vars.Vars = map[string]string{}
			for _, kv := range sets {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return contract.Usage("--set takes key=value, not %q", kv)
				}
				vars.Vars[k] = v
			}
			rp, err := openRepo(ctx, g)
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				if len(g.only) == 0 && len(g.tags) == 0 {
					return contract.Usage("name the hosts, or choose ones the flake defines with --only or --tag")
				}
				inv, err := inventory.Load(ctx, execx.Run, rp.Dir, false)
				if err != nil {
					return err
				}
				hosts, err := inv.Select(g.selector())
				if err != nil {
					return err
				}
				for _, h := range hosts {
					names = append(names, h.Name)
				}
				if len(names) == 0 {
					return contract.Usage("no hosts match")
				}
			}
			if len(names) > 1 && (vars.Address != "" || vars.IP != "" || deploy) {
				return contract.Usage("--address, --ip and --deploy take a single host")
			}

			type item struct {
				vars repo.HostVars
				st   actions.AddStatus
			}
			var items []item
			for _, name := range names {
				v := vars
				v.Name = name
				if v.Address == "" {
					info, err := rp.HostFleet(ctx, name)
					if err != nil {
						return contract.Failed("eval", "reading %s from the flake: %v", name, err)
					}
					switch {
					case info.Defined && info.Address != "":
						v.Address = info.Address
					case !status:
						return contract.Usage("%s isn't in the flake's fleet yet (or has no address there): give its --address", name)
					}
				}
				st, err := actions.AddStatusForOpts(ctx, rp, name, v.Address, pregen)
				if err != nil {
					return err
				}
				items = append(items, item{v, st})
			}

			if status {
				g.schema, g.onError = actions.AddSchema, nil
				if g.json {
					if len(items) == 1 {
						return contract.WriteJSON(out, items[0].st)
					}
					all := make([]actions.AddStatus, len(items))
					for i, it := range items {
						all[i] = it.st
					}
					return contract.WriteJSON(out, all)
				}
				for _, it := range items {
					printAddStatus(out, rp, it.st)
				}
				return nil
			}
			var pending []item
			for _, it := range items {
				if !g.json {
					printAddStatus(out, rp, it.st)
				}
				if !it.st.Done() {
					pending = append(pending, it)
				}
			}
			if len(pending) == 0 {
				em.Done(items[len(items)-1].st)
				return nil
			}
			question := fmt.Sprintf("Onboard %s into %s?", pending[0].vars.Name, rp.Dir)
			if len(pending) > 1 {
				question = fmt.Sprintf("Onboard %d hosts into %s?", len(pending), rp.Dir)
			}
			if err := confirm(em, yes, question); err != nil {
				return err
			}
			for _, it := range pending {
				st, err := actions.Add(ctx, em, rp, actions.AddOptions{Vars: it.vars, PregenKey: pregen})
				if !g.json {
					fmt.Fprintln(out)
					printAddStatus(out, rp, st)
				}
				if err != nil {
					return err
				}
			}
			if !deploy {
				if !g.json {
					fmt.Fprintf(out, "\nNext: review `git -C %s diff`, commit and push, then\n  careen up --only %s   (hosts careen provisions)\n  careen deploy --only %s%s   (existing machines)\n",
						rp.Dir, strings.Join(names, ","), strings.Join(names, ","), asFlag(as))
				}
				return nil
			}
			vars = pending[0].vars

			// Deploy straight from the working tree.
			inv, err := inventory.Load(ctx, execx.Run, rp.Dir, false)
			if err != nil {
				return err
			}
			plan, err := actions.PlanDeploy(inv, inventory.Selector{Only: []string{vars.Name}}, actions.DeployOptions{AllowDirty: true, As: as, Parallel: 1})
			if err != nil {
				return err
			}
			_, err = actions.Deploy(ctx, em, plan, func(done actions.DeployDone) {
				if !g.json {
					printDeploySummary(out, done.Hosts)
				}
			})
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&vars.Address, "address", "", "where the host is reached (and its key fetched); default: its fleet entry's address")
	f.StringVar(&vars.IP, "ip", "", "static IP, for the template")
	f.StringVar(&vars.Gateway, "gateway", "", "gateway, for the template")
	f.StringVar(&vars.Interface, "interface", "", "network interface, for the template")
	f.StringArrayVar(&sets, "set", nil, "extra template value, key=value (repeatable)")
	f.BoolVar(&pregen, "pregen-key", false, "make the host's SSH key now instead of reading it from the host (automatic for hosts careen provisions)")
	f.BoolVar(&status, "status", false, "show where onboarding stands and stop")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	f.BoolVar(&deploy, "deploy", false, "after onboarding, deploy NAME from the working tree")
	f.StringVar(&as, "as", "", "with --deploy: connect as this user (a generic image's)")
	return cmd
}

func asFlag(as string) string {
	if as == "" {
		return ""
	}
	return " --as " + as
}

func printAddStatus(w io.Writer, rp *repo.Repo, st actions.AddStatus) {
	fmt.Fprintf(w, "Onboarding %s in %s\n", st.Host, rp.Dir)
	for _, s := range st.Steps {
		mark := map[string]string{actions.StepDone: "✓", actions.StepTodo: "·", actions.StepWaiting: "…", actions.StepFailed: "✗", actions.StepUnknown: "?"}[s.State]
		fmt.Fprintf(w, "  %s %-20s %s\n", mark, s.Title, s.Detail)
	}
}
