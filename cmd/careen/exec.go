package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
)

func newExecCmd(g *globalFlags) *cobra.Command {
	var (
		all, planOnly, yes bool
		opts               actions.ExecOptions
	)
	cmd := &cobra.Command{
		Use:   "exec (--only HOSTS | --tag T | --all) -- COMMAND [ARGS...]",
		Short: "Run a command on hosts",
		Long: "Run a shell command on the selected hosts in parallel and show each host's\n" +
			"output and exit code. One argument is run as written, so pipes work:\n\n" +
			"  careen exec --all -- 'df -h /nix | tail -1'\n\n" +
			"Several arguments are quoted one by one. --sudo runs it as root.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return contract.Usage("no command given: careen exec --only HOST -- COMMAND")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			if planOnly {
				g.schema, g.onError = actions.ExecPlanSchema, nil
			}
			if !all && len(g.only) == 0 && len(g.tags) == 0 {
				return contract.Usage("choose hosts with --all, --only or --tag")
			}
			inv, err := inventory.Load(ctx, execx.Run, g.flake, false)
			if err != nil {
				return err
			}
			plan, err := actions.PlanExec(inv, g.selector(), actions.ShellCommand(args), opts)
			if err != nil {
				return err
			}
			if planOnly {
				if g.json {
					return contract.WriteJSON(out, plan)
				}
				printExecPlan(out, plan)
				return nil
			}
			if !g.json {
				printExecPlan(out, plan)
			}
			if err := confirm(em, yes, fmt.Sprintf("Run it on %d host(s)?", len(plan.Hosts))); err != nil {
				return err
			}
			if opts.Sudo {
				if err := ensureLocalSudo(ctx, em, plan.Targets()); err != nil {
					return err
				}
			}
			done, err := actions.Exec(ctx, em, plan)
			if !g.json {
				printExecResults(out, done)
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "every host")
	f.BoolVar(&opts.Sudo, "sudo", false, "run the command as root (sudo -n)")
	f.DurationVar(&opts.Timeout, "timeout", 60*time.Second, "give up on a host after this long")
	f.IntVar(&opts.Parallel, "parallel", 8, "hosts to run on at once")
	f.BoolVar(&planOnly, "plan", false, "show the hosts and command and stop")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func printExecPlan(w io.Writer, p *actions.ExecPlan) {
	as := ""
	if p.Sudo {
		as = " as root"
	}
	fmt.Fprintf(w, "Run%s on %s:\n  %s\n", as, strings.Join(p.Hosts, ", "), p.Command)
}

func printExecResults(w io.Writer, d actions.ExecDone) {
	ok := 0
	for _, r := range d.Hosts {
		fmt.Fprintln(w)
		if r.Error != nil {
			fmt.Fprintf(w, "── %s (%s) ──\n", r.Host, r.Error.Message)
		} else {
			fmt.Fprintf(w, "── %s (exit %d, %s) ──\n", r.Host, r.ExitCode, shortDuration(time.Duration(r.DurationMs)*time.Millisecond))
		}
		if r.OK() {
			ok++
		}
		for _, s := range []string{r.Stdout, r.Stderr} {
			if s != "" {
				fmt.Fprint(w, s)
				if !strings.HasSuffix(s, "\n") {
					fmt.Fprintln(w)
				}
			}
		}
		if r.Truncated {
			fmt.Fprintln(w, "[output truncated]")
		}
	}
	fmt.Fprintf(w, "\n%d ok, %d failed\n", ok, len(d.Hosts)-ok)
}

// shortDuration is d in ms under a second, else to a tenth of a second.
func shortDuration(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
}
