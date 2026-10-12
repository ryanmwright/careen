package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/format"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/probe"
	"github.com/ryanmwright/careen/internal/status"
)

func newStatusCmd(g *globalFlags) *cobra.Command {
	var (
		watch    bool
		interval time.Duration
		timeout  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show each host's drift, pending reboot and health",
		Long: "Show each host's drift from the flake, pending reboot, failed units, uptime and\n" +
			"store usage, from one SSH call per host. An unreachable host is a row with an\n" +
			"error; the command still exits 0. With --watch it repeats every --interval;\n" +
			"with --watch --json it prints one full snapshot per line.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g.schema = status.Schema
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if interval <= 0 {
				return contract.Usage("--interval must be positive")
			}
			p := probe.New()
			p.Timeout = timeout
			sel := g.selector()

			// Load once up front, so a bad flake or host name fails the
			// command instead of becoming an error line in the stream.
			inv, err := inventory.Load(ctx, execx.Run, g.flake, true)
			if err != nil {
				return err
			}
			if _, err := inv.Select(sel); err != nil {
				return err
			}
			first := inv
			collect := func(ctx context.Context) (any, error) {
				inv := first
				first = nil
				if inv == nil {
					var err error
					if inv, err = inventory.Load(ctx, execx.Run, g.flake, true); err != nil {
						return nil, err
					}
				}
				hosts, err := inv.Select(sel)
				if err != nil {
					return nil, err
				}
				return status.Build(inv.Flake, p.ProbeAll(ctx, hosts)), nil
			}

			switch {
			case watch && g.json:
				return contract.Watch(ctx, out, status.Schema, interval, collect)
			case watch:
				return watchHuman(ctx, out, interval, collect)
			}
			rep, err := collect(ctx)
			if err != nil {
				return err
			}
			if g.json {
				return contract.WriteJSON(out, rep)
			}
			printStatus(out, rep.(status.Report), time.Now())
			return nil
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "repeat until interrupted")
	cmd.Flags().DurationVar(&interval, "interval", 30*time.Second, "time between --watch updates")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Second, "give up on a host after this long")
	return cmd
}

// watchHuman redraws the table every interval.
func watchHuman(ctx context.Context, w io.Writer, interval time.Duration, collect func(context.Context) (any, error)) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		rep, err := collect(ctx)
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprint(w, "\033[H\033[2J")
		if err != nil {
			fmt.Fprintf(w, "%s  error: %v\n", time.Now().Format("15:04:05"), err)
		} else {
			printStatus(w, rep.(status.Report), time.Now())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func printStatus(w io.Writer, rep status.Report, now time.Time) {
	fmt.Fprintln(w, format.FlakeLine(rep.Flake))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tREVISION\tDRIFT\tREBOOT\tSTATE\tFAILED\tUPTIME\tSTORE\tGENS\tSWITCHED")
	var notes []string
	for _, r := range rep.Hosts {
		if r.Error != nil {
			fmt.Fprintf(tw, "%s\t%s: %s\n", r.Host, r.Error.Code, r.Error.Message)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%d\t%s\n",
			r.Host, format.Revision(r.State), r.Drift, format.Reboot(r.State), format.Dash(r.SystemState), len(r.FailedUnits),
			format.Duration(time.Duration(r.UptimeSeconds)*time.Second), format.Store(r.StoreUsed, r.StoreSize),
			r.Generations, format.Ago(r.LastSwitch, now))
		for _, u := range r.FailedUnits {
			notes = append(notes, fmt.Sprintf("%s: %s failed", r.Host, u))
		}
	}
	tw.Flush()
	if len(notes) > 0 {
		fmt.Fprintln(w)
		for _, n := range notes {
			fmt.Fprintln(w, n)
		}
	}
}
