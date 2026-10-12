// Command careen patches, reboots and inspects a NixOS fleet defined in a
// flake. Phase 1 is read-only: `careen hosts` and `careen status`.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type globalFlags struct {
	flake  string
	repo   string
	json   bool
	only   []string
	except []string
	tags   []string

	// schema names the running command's output, for the JSON error
	// object if it fails.
	schema string
	// onError, when set, reports the command's final error under --json
	// instead of the default error object; actions use their event stream.
	onError func(error)
}

func (g *globalFlags) selector() inventory.Selector {
	return inventory.Selector{Only: g.only, Except: g.except, Tags: g.tags}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	g := &globalFlags{schema: "careen.error/v1"}
	err := newRootCmd(g).ExecuteContext(ctx)
	stop()
	if err == nil {
		return
	}
	if strings.HasPrefix(err.Error(), "unknown command") {
		err = contract.Usage("%v", err)
	}
	switch {
	case g.json && g.onError != nil:
		g.onError(err)
	case g.json:
		contract.WriteError(os.Stdout, g.schema, err)
	}
	fmt.Fprintln(os.Stderr, "careen:", err)
	os.Exit(contract.ExitCode(err))
}

func newRootCmd(g *globalFlags) *cobra.Command {
	root := &cobra.Command{
		Use:           "careen",
		Short:         "Patch, reboot and inspect a NixOS fleet",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return contract.Usage("%v", err)
	})

	pf := root.PersistentFlags()
	pf.StringVar(&g.flake, "flake", os.Getenv("CAREEN_FLAKE"), "flake that defines the fleet ($CAREEN_FLAKE)")
	pf.StringVar(&g.repo, "repo", os.Getenv("CAREEN_REPO"), "writable checkout of the flake, for add, keys and secrets ($CAREEN_REPO)")
	pf.BoolVar(&g.json, "json", false, "print JSON for scripts")
	pf.StringSliceVar(&g.only, "only", nil, "only these hosts (comma-separated)")
	pf.StringSliceVar(&g.except, "except", nil, "skip these hosts (comma-separated)")
	pf.StringSliceVar(&g.tags, "tag", nil, "only hosts with any of these tags (comma-separated)")

	root.AddCommand(
		newHostsCmd(g),
		newStatusCmd(g),
		newDeployCmd(g),
		newRebootCmd(g),
		newRollbackCmd(g),
		newLogsCmd(g),
		newGCCmd(g),
		newExecCmd(g),
		newVulnsCmd(g),
		newUpCmd(g),
		newDownCmd(g),
		newVMCmd(g),
		newTemplateCmd(g),
		newAddCmd(g),
		newKeysCmd(g),
		newSecretsCmd(g),
		newDoctorCmd(g),
		newTUICmd(g),
		newGenDocsCmd(),
		&cobra.Command{
			Use:   "version",
			Short: "Print the careen version",
			Args:  noArgs,
			Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version) },
		},
	)
	return root
}

// noArgs is cobra.NoArgs as a usage error.
func noArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.NoArgs(cmd, args); err != nil {
		return contract.Usage("%v", err)
	}
	return nil
}
