package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
)

func newDoctorCmd(g *globalFlags) *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check careen's tools, flake, checkout, master key and SSH",
		Long: "Check what careen needs. The report exits 0 like other reads; with --strict it\n" +
			"exits 3 when a check fails, for scripts that gate on it.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g.schema = actions.DoctorSchema
			rep := actions.Doctor(cmd.Context(), g.flake, g.repo)
			out := cmd.OutOrStdout()
			if g.json {
				if err := contract.WriteJSON(out, rep); err != nil {
					return err
				}
			} else {
				for _, c := range rep.Checks {
					mark := map[string]string{actions.CheckOK: "✓", actions.CheckWarn: "!", actions.CheckFail: "✗"}[c.Status]
					fmt.Fprintf(out, "%s %-16s %s\n", mark, c.Name, c.Detail)
				}
			}
			if strict && !rep.OK {
				return contract.Precondition("doctor", "a check failed")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "exit 3 if a check fails")
	return cmd
}
