package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
)

func newRollbackCmd(g *globalFlags) *cobra.Command {
	var (
		yes           bool
		healthTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "rollback HOST",
		Short: "Switch a host back to its previous system generation",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return contract.Usage("rollback takes one host")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)

			inv, err := inventory.Load(ctx, execx.Run, g.flake, true)
			if err != nil {
				return err
			}
			hosts, err := inv.Select(inventory.Selector{Only: args})
			if err != nil {
				return err
			}
			h := hosts[0]
			if err := confirm(em, yes, fmt.Sprintf("Roll %s back to its previous generation?", h.Name)); err != nil {
				return err
			}
			if err := ensureLocalSudo(ctx, em, hosts); err != nil {
				return err
			}
			done, err := actions.Rollback(ctx, em, h, healthTimeout)
			if err != nil {
				return err
			}
			if !g.json {
				fmt.Fprintf(out, "%s: now running %s\n", h.Name, done.Result.To)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	cmd.Flags().DurationVar(&healthTimeout, "health-timeout", 2*time.Minute, "how long the host may take to settle")
	return cmd
}
