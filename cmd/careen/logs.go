package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/logs"
)

const (
	logsSchema      = "careen.logs/v1"
	logsEntrySchema = "careen.logs.entry/v1"
)

type logsReport struct {
	contract.Header
	Host    string       `json:"host"`
	Unit    string       `json:"unit,omitempty"`
	Entries []logs.Entry `json:"entries"`
}

// logsLine is one entry of a --follow --json stream.
type logsLine struct {
	Schema string `json:"schema"`
	Host   string `json:"host"`
	logs.Entry
}

func newLogsCmd(g *globalFlags) *cobra.Command {
	var (
		lines  int
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs HOST [UNIT]",
		Short: "Show a host's journal, or one unit's",
		Long: "Show the last --lines entries of a host's systemd journal, or of one unit.\n" +
			"With --follow, keep printing new entries until interrupted; with --json\n" +
			"--follow, one JSON object per entry.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 {
				return contract.Usage("logs takes a host and optionally a unit")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			g.schema = logsSchema
			unit := ""
			if len(args) == 2 {
				unit = args[1]
			}
			inv, err := inventory.Load(ctx, execx.Run, g.flake, false)
			if err != nil {
				return err
			}
			hosts, err := inv.Select(inventory.Selector{Only: args[:1]})
			if err != nil {
				return err
			}
			h := hosts[0]
			conn := actions.Conn(h, false)

			if follow {
				enc := json.NewEncoder(out)
				color := !g.json && isTerminal(out)
				return logs.Follow(ctx, conn, unit, lines, func(e logs.Entry) {
					if g.json {
						enc.Encode(logsLine{Schema: logsEntrySchema, Host: h.Name, Entry: e})
						return
					}
					printEntry(out, e, color)
				})
			}
			entries, err := logs.Read(ctx, conn, unit, lines)
			if err != nil {
				return contract.Failed("logs", "%s: %v", h.Name, err)
			}
			if g.json {
				return contract.WriteJSON(out, logsReport{Header: contract.NewHeader(logsSchema), Host: h.Name, Unit: unit, Entries: entries})
			}
			color := isTerminal(out)
			for _, e := range entries {
				printEntry(out, e, color)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 200, "how many entries to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new entries")
	return cmd
}

func printEntry(w io.Writer, e logs.Entry, color bool) {
	line := fmt.Sprintf("%s %s: %s", e.Time.Local().Format("Jan 02 15:04:05"), e.Unit, e.Message)
	switch {
	case color && e.Priority <= 3:
		line = "\033[31m" + line + "\033[0m"
	case color && e.Priority == 4:
		line = "\033[33m" + line + "\033[0m"
	}
	fmt.Fprintln(w, line)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
