package actions

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshx"
)

const ExecPlanSchema = "careen.exec.plan/v1"

// outputLimit caps each of a host's stdout and stderr.
const outputLimit = 1 << 20

type ExecOptions struct {
	Sudo     bool
	Timeout  time.Duration
	Parallel int
}

// ExecPlan is what Exec will run; its JSON form is careen.exec.plan/v1.
type ExecPlan struct {
	contract.Header
	// Command is the shell command each host runs with sh -c.
	Command string   `json:"command"`
	Sudo    bool     `json:"sudo"`
	Hosts   []string `json:"hosts"`

	targets []inventory.Host
	opts    ExecOptions
}

func (p *ExecPlan) Targets() []inventory.Host { return p.targets }

// ShellCommand turns command-line arguments into one shell command. A
// single argument is used as written, so `careen exec -- 'df -h | grep nix'`
// keeps its pipe; several are quoted one by one.
func ShellCommand(argv []string) string {
	if len(argv) == 1 {
		return argv[0]
	}
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = sshx.Quote(a)
	}
	return strings.Join(q, " ")
}

func PlanExec(inv *inventory.Inventory, sel inventory.Selector, command string, opts ExecOptions) (*ExecPlan, error) {
	if strings.TrimSpace(command) == "" {
		return nil, contract.Usage("no command given: careen exec --only HOST -- COMMAND")
	}
	hosts, err := inv.Select(sel)
	if err != nil {
		return nil, err
	}
	hosts = InOnlyOrder(hosts, sel.Only)
	p := &ExecPlan{Header: contract.NewHeader(ExecPlanSchema), Command: command, Sudo: opts.Sudo, Hosts: []string{}, targets: hosts, opts: opts}
	for _, h := range hosts {
		p.Hosts = append(p.Hosts, h.Name)
	}
	return p, nil
}

// ExecResult is one host's run.
type ExecResult struct {
	Host       string          `json:"host"`
	ExitCode   int             `json:"exitCode"`
	Stdout     string          `json:"stdout"`
	Stderr     string          `json:"stderr"`
	Truncated  bool            `json:"truncated,omitempty"`
	DurationMs int64           `json:"durationMs"`
	Error      *contract.Error `json:"error,omitempty"`
}

// OK reports whether the command ran and exited 0.
func (r ExecResult) OK() bool { return r.Error == nil && r.ExitCode == 0 }

type ExecDone struct {
	Hosts []ExecResult `json:"hosts"`
}

// Exec runs the plan's command on its hosts in parallel. A host whose
// command fails or times out makes the whole run fail.
func Exec(ctx context.Context, em *contract.Emitter, p *ExecPlan) (ExecDone, error) {
	em.Started("exec", p)
	script := `exec sh -c "$1"`
	if p.Sudo {
		script = `exec sudo -n sh -c "$1"`
	}
	results := make([]ExecResult, len(p.targets))
	sem := make(chan struct{}, max(p.opts.Parallel, 1))
	var wg sync.WaitGroup
	for i, h := range p.targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			hctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
			defer cancel()
			start := time.Now()
			out, err := sshx.Capture(hctx, Conn(h, false), outputLimit, script, p.Command)
			r := ExecResult{Host: h.Name, ExitCode: out.ExitCode, Stdout: string(out.Stdout), Stderr: string(out.Stderr),
				Truncated: out.Truncated, DurationMs: time.Since(start).Milliseconds()}
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				err = contract.Failed("timeout", "no result within %s", p.opts.Timeout)
			case err != nil:
				err = contract.Failed("exec", "%v", err)
			}
			if err != nil {
				e := contract.ErrorObject(err)
				e.Host = h.Name
				r.Error = &e
				em.Error(h.Name, "exec", err)
			} else {
				em.Progress(h.Name, "done", "exit %d in %s", r.ExitCode, time.Duration(r.DurationMs)*time.Millisecond)
			}
			results[i] = r
		})
	}
	wg.Wait()

	done := ExecDone{Hosts: results}
	em.Done(done)
	failed := 0
	for _, r := range results {
		if !r.OK() {
			failed++
		}
	}
	if failed > 0 {
		return done, contract.Failed("exec", "%d of %d host(s) failed", failed, len(results))
	}
	return done, nil
}
