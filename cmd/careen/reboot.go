package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/reboot"
)

func newRebootCmd(g *globalFlags) *cobra.Command {
	var (
		force, planOnly, yes, self bool
		rf                         rebootFlags
	)
	cmd := &cobra.Command{
		Use:   "reboot",
		Short: "Reboot hosts that need it, one at a time, per rebootPolicy",
		Long: `Reboot hosts whose kernel, initrd or kernel modules changed, or that have a
system staged for the next boot. Proxmox VMs with hardware changes waiting for
a restart (reason: hardware) are restarted through Proxmox (qm reboot), which
applies them. One host at a time; careen waits for each to
come back healthy and stops at the first that doesn't. The machine careen runs
on is skipped unless --reboot-self, and then it goes last.

rebootPolicy: auto hosts are rebooted; manual hosts only when named with
--only; never hosts are skipped. --force reboots selected hosts even if they
don't need it (never still skips).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			if planOnly {
				g.schema, g.onError = actions.RebootPlanSchema, nil
			}
			rolling, err := rf.rolling(g, em)
			if err != nil {
				return err
			}
			opts := actions.RebootOptions{Mode: reboot.Policy, Self: self, Timeout: rf.timeout, HealthTimeout: rf.healthTimeout, Rolling: rolling}
			if force {
				opts.Mode = reboot.Force
			}

			inv, err := inventory.Load(ctx, execx.Run, g.flake, true)
			if err != nil {
				return err
			}
			plan, err := actions.PlanReboot(ctx, inv, g.selector(), opts)
			if err != nil {
				return err
			}
			if planOnly {
				if g.json {
					return contract.WriteJSON(out, plan)
				}
				printRebootPlan(out, plan)
				return nil
			}
			if !g.json {
				printRebootPlan(out, plan)
			}
			if plan.Count() > 0 {
				if err := confirm(em, yes, fmt.Sprintf("Reboot %d host(s)?", plan.Count())); err != nil {
					return err
				}
				if err := ensureLocalSudo(ctx, em, plan.Targets()); err != nil {
					return err
				}
			}
			_, err = actions.Reboot(ctx, em, plan, func(done actions.RebootDone) {
				if !g.json && plan.Count() > 0 {
					printRebootSummary(out, done.Hosts)
				}
			})
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&force, "force", false, "reboot selected hosts even if they don't need it")
	f.BoolVar(&self, "reboot-self", false, "also reboot the machine careen runs on (last; careen exits)")
	f.BoolVar(&planOnly, "plan", false, "show what would be rebooted and stop")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	rf.add(cmd)
	rf.addHooks(cmd)
	return cmd
}

func printRebootPlan(w io.Writer, p *actions.RebootPlan) {
	fmt.Fprintf(w, "Reboot (%s), one at a time in this order:\n", p.Mode)
	var rows []string
	var hooks []string
	for _, h := range p.Hosts {
		what := "reboot"
		if h.Skip != "" {
			what = "skip: " + h.Skip
		}
		why := "-"
		if len(h.Reasons) > 0 {
			why = strings.Join(h.Reasons, ",")
		}
		rows = append(rows, fmt.Sprintf("  %s\tneeds: %s\t%s", h.Host, why, what))
		note := ""
		if len(h.Hooks) > 0 && h.Skip == "" {
			note = "      hooks: " + strings.Join(h.Hooks, ", ")
		}
		hooks = append(hooks, note)
	}
	printWithNotes(w, rows, hooks)
	if p.NoHooks && slices.ContainsFunc(p.Hosts, func(h actions.RebootOutcome) bool { return len(h.Hooks) > 0 }) {
		fmt.Fprintln(w, "  --no-hooks: none of these hooks will run")
	}
}

func printRebootSummary(w io.Writer, outs []actions.RebootOutcome) {
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tRESULT\tNOTE")
	for _, o := range outs {
		result, note := "skipped", o.Skip
		switch {
		case o.Error != nil:
			result, note = "failed", o.Error.Message
		case o.Rebooted && o.Forced:
			result, note = "rebooted", "pre timed out and was forced"
		case o.Rebooted:
			result, note = "rebooted", ""
		}
		if o.Manual != "" {
			note = strings.TrimPrefix(note+"; "+o.Manual, "; ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", o.Host, result, note)
	}
	tw.Flush()
}
