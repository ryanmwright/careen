package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/agekey"
	"github.com/ryanmwright/careen/internal/contract"
)

func newKeysCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "Check each host's sops key against its live host key",
		Long: `List every host's age key in .sops.yaml next to the key its SSH host key
gives now, and the secrets files not encrypted to it yet:

  ok           recorded, live and every file agree
  placeholder  never registered (age1REPLACEME_…): careen keys register HOST
  drift        the host was reinstalled; it can't decrypt: register --replace
  needs-rekey  some files aren't encrypted to it: careen keys rekey HOST
  no-anchor    in the inventory but not in .sops.yaml: careen add HOST
  unreachable  couldn't fetch the live key`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			g.schema = actions.KeysSchema
			rp, err := openRepo(ctx, g)
			if err != nil {
				return err
			}
			rep, err := actions.Keys(ctx, rp, optionalInventory(ctx, g), g.only)
			if err != nil {
				return err
			}
			if g.json {
				return contract.WriteJSON(cmd.OutOrStdout(), rep)
			}
			printKeys(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.AddCommand(newKeysRegisterCmd(g), newKeysRekeyCmd(g))
	return cmd
}

func newKeysRegisterCmd(g *globalFlags) *cobra.Command {
	var (
		o   actions.RegisterOptions
		yes bool
	)
	cmd := &cobra.Command{
		Use:   "register HOST",
		Short: "Record HOST's live key in .sops.yaml and re-key its files",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return contract.Usage("register takes one host")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			em := newEmitter(g, cmd.OutOrStdout())
			rp, err := openRepo(ctx, g)
			if err != nil {
				return err
			}
			if o.Address == "" {
				o.Address = hostAddress(optionalInventory(ctx, g), args[0])
			}
			if err := confirm(em, yes, fmt.Sprintf("Register %s's host key and re-key its files in %s?", args[0], rp.Dir)); err != nil {
				return err
			}
			if err := actions.RegisterKey(ctx, em, rp, args[0], o); err != nil {
				return err
			}
			em.Done(map[string]string{"host": args[0]})
			if !g.json {
				fmt.Fprintf(cmd.OutOrStdout(), "Done. Review `git -C %s diff` and commit.\n", rp.Dir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Address, "address", "", "where to fetch the key (default: the inventory's address, else HOST)")
	cmd.Flags().BoolVar(&o.Replace, "replace", false, "replace a recorded key that differs from the live one")
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func newKeysRekeyCmd(g *globalFlags) *cobra.Command {
	var all, yes bool
	cmd := &cobra.Command{
		Use:   "rekey (HOST... | --all)",
		Short: "Re-encrypt hosts' files to the keys .sops.yaml gives them now",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			em := newEmitter(g, cmd.OutOrStdout())
			if len(args) == 0 && !all {
				return contract.Usage("name hosts, or --all")
			}
			rp, err := openRepo(ctx, g)
			if err != nil {
				return err
			}
			hosts := args
			if all {
				rep, err := actions.Keys(ctx, rp, nil, nil)
				if err != nil {
					return err
				}
				hosts = nil
				for _, h := range rep.Hosts {
					if h.Anchor != "" {
						hosts = append(hosts, h.Host)
					}
				}
			}
			if err := confirm(em, yes, fmt.Sprintf("Re-key the files of %s?", strings.Join(hosts, ", "))); err != nil {
				return err
			}
			for _, h := range hosts {
				if err := actions.RekeyHost(ctx, em, rp, h); err != nil {
					return err
				}
			}
			em.Done(map[string]any{"hosts": hosts})
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every host in .sops.yaml")
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func printKeys(w io.Writer, rep actions.KeysReport) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tSTATE\tRECORDED\tLIVE\tNOT RE-KEYED")
	for _, h := range rep.Hosts {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", h.Host, h.State, shortKey(h.Recorded), shortKey(h.Live), dashJoin(h.NeedsRekey))
	}
	tw.Flush()
}

// shortKey is an age key's start and end, enough to tell keys apart.
func shortKey(k string) string {
	switch {
	case k == "":
		return "-"
	case agekey.IsRecipient(k):
		return k[:10] + "…" + k[len(k)-6:]
	}
	return k
}

func dashJoin(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ", ")
}
