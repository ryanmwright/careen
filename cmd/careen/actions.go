package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/reboot"
)

const eventsSchema = "careen.events/v1"

// newEmitter sets up an action's event stream and routes the command's
// final error into it.
func newEmitter(g *globalFlags, w io.Writer) *contract.Emitter {
	em := contract.NewEmitter(w, g.json, eventsSchema)
	g.schema = eventsSchema
	g.onError = func(err error) { em.Error("", "", err) }
	return em
}

// confirm asks before an action unless --yes was given. Under --json or
// without a terminal it emits a decision event and fails with exit 3.
func confirm(em *contract.Emitter, yes bool, question string) error {
	if yes {
		return nil
	}
	if em.JSON() || !contract.Interactive() {
		em.Decision("", "confirm", question, "--yes", "--plan")
		return contract.Precondition("confirm", "%s: pass --yes, or --plan to only show the plan", question)
	}
	if !contract.Confirm(os.Stderr, question) {
		return contract.Failed("aborted", "not confirmed")
	}
	return nil
}

// ensureLocalSudo makes sure careen can sudo on this machine before
// starting work on it: passwordless, or by prompting once in a terminal.
func ensureLocalSudo(ctx context.Context, em *contract.Emitter, hosts []inventory.Host) error {
	if !actions.IncludesLocal(hosts) || actions.LocalSudo(ctx) == nil {
		return nil
	}
	host := actions.ThisHost
	if em.JSON() || !contract.Interactive() {
		em.Decision(host, "sudo", "sudo on this machine needs a password", "run in a terminal", "leave this host out")
		return contract.Precondition("sudo", "sudo on %s needs a password; run careen in a terminal or leave %s out", host, host)
	}
	cmd := exec.CommandContext(ctx, "sudo", "-v")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return contract.Precondition("sudo", "sudo on %s: %v", host, err)
	}
	return nil
}

// printWithNotes prints tab-separated rows aligned as a table, each
// followed by its note (when not empty) on a line of its own, so long notes
// don't stretch the columns.
func printWithNotes(w io.Writer, rows, notes []string) {
	if len(rows) == 0 {
		return
	}
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, r)
	}
	tw.Flush()
	for i, line := range strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n") {
		fmt.Fprintln(w, strings.TrimRight(line, " "))
		if i < len(notes) && notes[i] != "" {
			fmt.Fprintln(w, notes[i])
		}
	}
}

type rebootFlags struct {
	timeout       time.Duration
	healthTimeout time.Duration
	onTimeout     string
	noHooks       bool
}

func (f *rebootFlags) add(cmd *cobra.Command) {
	cmd.Flags().DurationVar(&f.timeout, "reboot-timeout", 10*time.Minute, "how long a host may take to come back from a reboot")
	cmd.Flags().DurationVar(&f.healthTimeout, "health-timeout", 2*time.Minute, "how long a host may take to settle after a change")
}

// addHooks adds the flags for hosts' reboot hooks, for commands that
// reboot hosts in service.
func (f *rebootFlags) addHooks(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.onTimeout, "on-timeout", "", "when a host's pre hook times out: force, skip or abort (default: its onTimeout, else ask)")
	cmd.Flags().BoolVar(&f.noHooks, "no-hooks", false, "reboot without running hosts' reboot hooks (pre, post, gate)")
}

// rolling is how reboots run hooks: --on-timeout and --no-hooks, and asking
// on the terminal (or, without one, a decision event and exit 3).
func (f *rebootFlags) rolling(g *globalFlags, em *contract.Emitter) (actions.Rolling, error) {
	switch f.onTimeout {
	case "", reboot.TimeoutForce, reboot.TimeoutSkip, reboot.TimeoutAbort:
	default:
		return actions.Rolling{}, contract.Usage("--on-timeout is force, skip or abort, not %q", f.onTimeout)
	}
	batch := []string{reboot.TimeoutForce, reboot.TimeoutSkip, reboot.TimeoutAbort}
	return actions.Rolling{OnTimeout: f.onTimeout, NoHooks: f.noHooks, Ask: asker(g, em, "--on-timeout", batch)}, nil
}

// asker answers an action's mid-run questions on the terminal. Without one,
// or under --json, it emits a decision event listing the flag values that
// answer it (those in batch) and fails with exit 3, so a script can rerun.
func asker(g *globalFlags, em *contract.Emitter, flag string, batch []string) contract.Ask {
	return func(_ context.Context, q contract.Question) (string, error) {
		if !g.json && contract.Interactive() {
			if a, ok := contract.Choose(os.Stderr, q); ok {
				return a, nil
			}
			return "", contract.Failed("aborted", "no answer to: %s", q.Message)
		}
		var opts []string
		for _, o := range q.Options {
			if slices.Contains(batch, o) {
				opts = append(opts, flag+" "+o)
			}
		}
		em.Decision(q.Host, q.Step, q.Message, opts...)
		return "", contract.Precondition("decision", "%s: %s; rerun with %s", q.Host, q.Message, strings.Join(opts, " or "))
	}
}
