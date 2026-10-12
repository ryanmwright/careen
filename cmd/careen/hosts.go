package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/format"
	"github.com/ryanmwright/careen/internal/inventory"
)

const hostsSchema = "careen.hosts/v1"

type hostsReport struct {
	contract.Header
	Flake inventory.Flake  `json:"flake"`
	Hosts []inventory.Host `json:"hosts"`
}

func newHostsCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "hosts",
		Short: "List the hosts in the flake's fleet inventory",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g.schema = hostsSchema
			inv, err := inventory.Load(cmd.Context(), execx.Run, g.flake, false)
			if err != nil {
				return err
			}
			hosts, err := inv.Select(g.selector())
			if err != nil {
				return err
			}
			if hosts == nil {
				hosts = []inventory.Host{}
			}
			if g.json {
				return contract.WriteJSON(cmd.OutOrStdout(), hostsReport{
					Header: contract.NewHeader(hostsSchema),
					Flake:  inv.Flake,
					Hosts:  hosts,
				})
			}
			printHosts(cmd.OutOrStdout(), inv.Flake, hosts)
			return nil
		},
	}
}

func printHosts(w io.Writer, flake inventory.Flake, hosts []inventory.Host) {
	fmt.Fprintln(w, format.FlakeLine(flake))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tADDRESS\tUSER\tTAGS\tREBOOT\tDEPLOY\tOPERATION\tBUILD")
	for _, h := range hosts {
		build := h.BuildOn
		if build == "remote" {
			build += " (" + h.Builder + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h.Name, h.Address, h.User, format.Dash(strings.Join(h.Tags, ",")), h.RebootPolicy, format.YesNo(h.Deploy), h.Operation, format.Dash(build))
	}
	tw.Flush()
}
