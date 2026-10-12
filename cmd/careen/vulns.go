package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/vulns"
)

func newVulnsCmd(g *globalFlags) *cobra.Command {
	var opts actions.VulnsOptions
	cmd := &cobra.Command{
		Use:   "vulns",
		Short: "Scan each host's running system for known CVEs (vulnix)",
		Long: "Scan each host's running system with vulnix, here, against NVD. Systems not in\n" +
			"this machine's store are copied in first. Results are cached per host and\n" +
			"system for --max-age; --refresh rescans. CVEs listed in vulnix-whitelist.toml\n" +
			"at the flake's root are left out. Findings don't change the exit code.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			g.schema = actions.VulnsSchema
			inv, err := inventory.Load(ctx, execx.Run, g.flake, false)
			if err != nil {
				return err
			}
			rep, err := actions.Vulns(ctx, inv, g.selector(), opts, func(host, msg string) {
				if !g.json {
					fmt.Fprintf(os.Stderr, "%s: %s\n", host, msg)
				}
			})
			if err != nil {
				return err
			}
			if g.json {
				return contract.WriteJSON(out, rep)
			}
			printVulns(out, rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.Refresh, "refresh", false, "rescan even if a cached report matches")
	cmd.Flags().DurationVar(&opts.MaxAge, "max-age", 24*time.Hour, "how old a cached report may be")
	return cmd
}

func printVulns(w io.Writer, rep actions.VulnsReport) {
	if rep.Whitelist == "" {
		fmt.Fprintln(w, "no "+actions.WhitelistFile+" in the flake")
	}
	for _, r := range rep.Hosts {
		fmt.Fprintln(w)
		if r.Error != nil {
			fmt.Fprintf(w, "%s: %s\n", r.Host, r.Error.Message)
			continue
		}
		fmt.Fprintf(w, "%s: %s (%d packages; scanned %s)\n", r.Host, r.Summary.Short(), len(r.Findings), r.ScannedAt.Local().Format("Jan 02 15:04"))
		for _, f := range r.Findings {
			fmt.Fprintf(w, "  %-6s %s %s  %s\n", severity(f.MaxScore()), f.Package, f.Version, cveList(f.CVEs))
		}
	}
}

func severity(score float64) string {
	switch {
	case score >= 9:
		return "crit"
	case score >= 7:
		return "high"
	case score >= 4:
		return "medium"
	}
	return "low"
}

func cveList(cves []vulns.CVE) string {
	var parts []string
	for _, c := range cves {
		parts = append(parts, fmt.Sprintf("%s (%.1f)", c.ID, c.Score))
	}
	return strings.Join(parts, ", ")
}
