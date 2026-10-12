package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/format"
	"github.com/ryanmwright/careen/internal/inventory"
)

func newGCCmd(g *globalFlags) *cobra.Command {
	var (
		all, planOnly, yes bool
		opts               actions.GCOptions
	)
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Delete old system generations and unreachable store paths",
		Long: "Run nix-collect-garbage --delete-older-than on each host (with sudo) and report\n" +
			"the space freed. The current and booted systems are never removed. This is for\n" +
			"when a disk is filling up now; for routine collection, set nix.gc.automatic.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			if planOnly {
				g.schema, g.onError = actions.GCPlanSchema, nil
			}
			if !all && len(g.only) == 0 && len(g.tags) == 0 {
				return contract.Usage("choose hosts with --all, --only or --tag")
			}
			inv, err := inventory.Load(ctx, execx.Run, g.flake, false)
			if err != nil {
				return err
			}
			plan, err := actions.PlanGC(ctx, inv, g.selector(), opts)
			if err != nil {
				return err
			}
			if planOnly {
				if g.json {
					return contract.WriteJSON(out, plan)
				}
				printGCPlan(out, plan)
				return nil
			}
			if len(plan.Targets()) == 0 {
				return contract.Precondition("unreachable", "none of the selected hosts can be reached")
			}
			if !g.json {
				printGCPlan(out, plan)
			}
			if err := confirm(em, yes, fmt.Sprintf("Collect garbage on %d host(s)?", len(plan.Targets()))); err != nil {
				return err
			}
			if err := ensureLocalSudo(ctx, em, plan.Targets()); err != nil {
				return err
			}
			done, err := actions.GC(ctx, em, plan)
			if !g.json {
				printGCSummary(out, done)
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "every host")
	f.StringVar(&opts.OlderThan, "older-than", "14d", "delete system generations older than this")
	f.IntVar(&opts.Parallel, "parallel", 4, "hosts to collect at once")
	f.BoolVar(&planOnly, "plan", false, "show the hosts and stop")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func printGCPlan(w io.Writer, p *actions.GCPlan) {
	fmt.Fprintf(w, "Collect garbage, deleting generations older than %s:\n", p.OlderThan)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, h := range p.Hosts {
		if h.Error != nil {
			fmt.Fprintf(tw, "  %s\tskip: %s\n", h.Host, h.Error.Message)
			continue
		}
		fmt.Fprintf(tw, "  %s\t%d generations\tstore %s\n", h.Host, h.Generations, format.Store(h.StoreUsed, h.StoreSize))
	}
	tw.Flush()
}

func printGCSummary(w io.Writer, d actions.GCDone) {
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tFREED\tPATHS\tGENERATIONS\tNOTE")
	for _, r := range d.Hosts {
		if r.Error != nil {
			fmt.Fprintf(tw, "%s\t-\t-\t-\t%s\n", r.Host, r.Error.Message)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d → %d\t\n", r.Host, actions.Bytes(r.Freed), r.PathsDeleted, r.GenerationsBefore, r.GenerationsAfter)
	}
	tw.Flush()
	fmt.Fprintf(w, "freed %s in total\n", actions.Bytes(d.Freed))
}
