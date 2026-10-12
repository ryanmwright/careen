package main

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/reboot"
	"github.com/ryanmwright/careen/internal/tui"
)

func newTUICmd(g *globalFlags) *cobra.Command {
	var (
		interval, execTimeout time.Duration
		gcOlderThan           string
		opts                  actions.DeployOptions
		rf                    rebootFlags
	)
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive terminal UI",
		Long: "Open the terminal UI: the fleet's live status, with keys to deploy, reboot,\n" +
			"roll back, follow logs and open a shell. Actions use the same code and\n" +
			"defaults as the CLI commands; the deploy flags below set their defaults.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.flake == "" {
				return contract.Usage("no flake given: pass --flake or set CAREEN_FLAKE")
			}
			opts.HealthTimeout, opts.RebootTimeout = rf.healthTimeout, rf.timeout
			// The TUI asks with its own prompt (set per run); the flags only
			// preset the answer or turn the hooks off.
			rolling, err := rf.rolling(g, nil)
			if err != nil {
				return err
			}
			rolling.Ask = nil
			opts.Rolling = rolling
			be := tui.NewBackend(tui.Options{
				Flake:  g.flake,
				Repo:   g.repo,
				Deploy: opts,
				Reboot: actions.RebootOptions{Mode: reboot.Policy, Timeout: rf.timeout, HealthTimeout: rf.healthTimeout, Rolling: rolling},
				GC:     actions.GCOptions{OlderThan: gcOlderThan, Parallel: opts.Parallel},
				Exec:   actions.ExecOptions{Timeout: execTimeout, Parallel: 8},
				Vulns:  actions.VulnsOptions{MaxAge: 24 * time.Hour},
			})
			_, err = tea.NewProgram(tui.New(be, interval), tea.WithContext(cmd.Context())).Run()
			return err
		},
	}
	f := cmd.Flags()
	f.DurationVar(&interval, "interval", 30*time.Second, "time between status refreshes")
	f.StringVar(&opts.BuildOn, "build-on", "", "override where to build: local, remote or target")
	f.StringVar(&opts.Builder, "builder", "", "override the remote builder's store URI")
	f.IntVar(&opts.Parallel, "parallel", 4, "hosts to build and switch at once")
	f.DurationVar(&opts.Watchdog, "rollback-after", 5*time.Minute, "a host rolls back by itself unless careen confirms it within this time")
	f.DurationVar(&opts.Settle, "settle", 10*time.Second, "wait this long after a switch before checking health")
	f.StringVar(&gcOlderThan, "older-than", "14d", "gc: delete system generations older than this")
	f.DurationVar(&execTimeout, "exec-timeout", 60*time.Second, "exec: give up on a host after this long")
	rf.add(cmd)
	rf.addHooks(cmd)
	return cmd
}
