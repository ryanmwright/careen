package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/format"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/reboot"
)

func newDeployCmd(g *globalFlags) *cobra.Command {
	var (
		all, planOnly, yes bool
		opts               actions.DeployOptions
		rf                 rebootFlags
	)
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Build, push and switch hosts, then optionally reboot them",
		Long: `Build each host's system from the flake, copy it to the host and switch to it.
After the switch careen checks the host over a new connection and rolls back
if it isn't healthy; a watchdog on the host rolls back by itself if careen
loses the connection.

  careen deploy --all                  patch, no reboot
  careen deploy --all --reboot         then reboot where needed, per rebootPolicy
  careen deploy --all --reboot=force   then reboot every deployed host (not never)

--all is every host with deploy = true; other hosts deploy only when named
with --only.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			if planOnly {
				g.schema, g.onError = actions.DeployPlanSchema, nil
			}
			if !all && len(g.only) == 0 && len(g.tags) == 0 {
				return contract.Usage("choose hosts with --all, --only or --tag")
			}
			opts.HealthTimeout, opts.RebootTimeout = rf.healthTimeout, rf.timeout
			rolling, err := rf.rolling(g, em)
			if err != nil {
				return err
			}
			opts.Rolling = rolling

			inv, err := inventory.Load(ctx, execx.Run, g.flake, true)
			if err != nil {
				return err
			}
			plan, err := actions.PlanDeploy(inv, g.selector(), opts)
			if err != nil {
				return err
			}
			if planOnly {
				if g.json {
					return contract.WriteJSON(out, plan)
				}
				printDeployPlan(out, plan)
				return nil
			}
			if len(plan.Targets()) == 0 {
				return contract.Usage("no hosts to deploy")
			}
			if !g.json {
				printDeployPlan(out, plan)
			}
			if err := confirm(em, yes, fmt.Sprintf("Deploy %d host(s)?", len(plan.Targets()))); err != nil {
				return err
			}
			if err := ensureLocalSudo(ctx, em, plan.Targets()); err != nil {
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
	f.BoolVar(&all, "all", false, "every host with deploy = true")
	f.StringVar(&opts.Reboot, "reboot", "", "after deploying, reboot: policy (where needed, per rebootPolicy) or force")
	f.Lookup("reboot").NoOptDefVal = reboot.Policy
	f.StringVar(&opts.BuildOn, "build-on", "", "override where to build: local, remote or target")
	f.StringVar(&opts.Builder, "builder", "", "override the remote builder's store URI")
	f.BoolVar(&opts.RebootSelf, "reboot-self", false, "let --reboot reboot the machine careen runs on (last; careen exits)")
	f.StringVar(&opts.As, "as", "", "connect as this user instead of the inventory's (a first deploy from a generic image)")
	f.BoolVar(&planOnly, "plan", false, "show what would be deployed and stop")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	f.BoolVar(&opts.AllowDirty, "allow-dirty", false, "deploy from a flake with uncommitted changes")
	f.IntVar(&opts.Parallel, "parallel", 4, "hosts to build and switch at once")
	f.DurationVar(&opts.Watchdog, "rollback-after", 5*time.Minute, "a host rolls back by itself unless careen confirms it within this time (0 turns it off)")
	f.DurationVar(&opts.Settle, "settle", 10*time.Second, "wait this long after a switch before checking health")
	rf.add(cmd)
	rf.addHooks(cmd)
	return cmd
}

func printDeployPlan(w io.Writer, p *actions.DeployPlan) {
	reboots := "no reboots"
	switch p.Reboot {
	case reboot.Policy:
		reboots = "then reboot where needed, per rebootPolicy"
	case reboot.Force:
		reboots = "then reboot every deployed host (except rebootPolicy = never)"
	}
	fmt.Fprintf(w, "Deploy %s to %d host(s), %s\n", format.FlakeLine(p.Flake), len(p.Hosts), reboots)
	if p.Reboot != "" && !p.RebootSelf && slices.ContainsFunc(p.Hosts, func(h actions.PlanHost) bool { return h.Local }) {
		fmt.Fprintln(w, "  this machine won't be rebooted; pass --reboot-self to include it")
	}
	var rows, notes []string
	for _, h := range p.Hosts {
		where := h.BuildOn
		if h.Builder != "" {
			where += " on " + h.Builder
		}
		note := ""
		if h.Local {
			note = "this machine, deployed last"
		}
		rows = append(rows, fmt.Sprintf("  %s\tbuild %s\t%s\t%s", h.Host, where, h.Operation, note))
		hooks := ""
		if hs := p.RebootHooks(h.Host); p.Reboot != "" && len(hs) > 0 {
			hooks = "      reboot hooks: " + strings.Join(hs, ", ")
		}
		notes = append(notes, hooks)
	}
	printWithNotes(w, rows, notes)
	for _, s := range p.Skipped {
		fmt.Fprintf(w, "  skipping %s: %s\n", s.Host, s.Reason)
	}
}

func printDeploySummary(w io.Writer, results []actions.DeployResult) {
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tRESULT\tREBOOT\tNOTE")
	for _, r := range results {
		rb := "-"
		note := ""
		switch {
		case r.Reboot != nil && r.Reboot.Rebooted:
			rb = "rebooted"
			if r.Reboot.Forced {
				note = "pre timed out and was forced"
			}
		case r.Reboot != nil && r.Reboot.Error != nil:
			rb = "failed"
		case len(r.RebootPending) > 0:
			rb = "pending (" + strings.Join(r.RebootPending, ",") + ")"
			if r.Reboot != nil && r.Reboot.Skip != "" {
				note = r.Reboot.Skip
			}
		}
		if r.Error != nil {
			note = r.Error.Message
		} else if r.Reboot != nil && r.Reboot.Error != nil {
			note = r.Reboot.Error.Message
		}
		if r.Reboot != nil && r.Reboot.Manual != "" {
			note = strings.TrimPrefix(note+"; "+r.Reboot.Manual, "; ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Host, r.Status, rb, note)
	}
	tw.Flush()
}
