// Package sshx runs shell commands on a host, over ssh or, for the machine
// careen runs on, directly.
package sshx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ryanmwright/careen/internal/execx"
)

// Host runs shell commands on one machine.
type Host interface {
	// Run runs script with sh, passing args as $1, $2, …, and returns its
	// stdout.
	Run(ctx context.Context, script string, args ...string) ([]byte, error)
	// Local reports whether commands run on this machine, without ssh.
	Local() bool
}

// ErrUnreachable wraps errors where ssh itself failed (exit 255).
var ErrUnreachable = errors.New("unreachable")

// Remote is a host reached over ssh.
type Remote struct {
	User    string
	Address string
	// Args are extra ssh arguments; they take precedence over the defaults
	// (ssh keeps the first value it sees for an option). Tests use them
	// to pass -F.
	Args []string
	// NoMaster skips the shared ControlMaster connection, for checks that
	// must prove a new connection works (after a switch or a reboot).
	NoMaster bool
	// ConnectTimeout bounds connection setup; zero means 5s.
	ConnectTimeout time.Duration
}

func (r Remote) Local() bool { return false }

// SSHArgs is the ssh command line for running remoteCmd on r.
func (r Remote) SSHArgs(remoteCmd string) []string {
	ct := r.ConnectTimeout
	if ct == 0 {
		ct = 5 * time.Second
	}
	args := slices.Concat(r.Args, []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(ct.Seconds())),
	})
	if dir := controlDir(); dir != "" && !r.NoMaster {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+filepath.Join(dir, "%C"),
			"-o", "ControlPersist=10m",
		)
	} else {
		args = append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	}
	if r.User != "" {
		args = append(args, "-l", r.User)
	}
	return append(args, "--", r.Address, remoteCmd)
}

// Run pipes script to `sh -s` on the host.
func (r Remote) Run(ctx context.Context, script string, args ...string) ([]byte, error) {
	remote := "sh -s --"
	for _, a := range args {
		remote += " " + Quote(a)
	}
	cmd := exec.CommandContext(ctx, "ssh", r.SSHArgs(remote)...)
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// A ControlPersist master inherits stderr and would keep Output
	// waiting; don't wait for it once ssh itself has exited.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 255 {
			return out, fmt.Errorf("%w: %s", ErrUnreachable, orDefault(execx.Tail(stderr.String(), 1), "ssh failed"))
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("%w: %s", err, execx.Summary(stderr.String()))
	}
	return out, nil
}

// LocalHost runs commands on this machine.
type LocalHost struct{}

func (LocalHost) Local() bool { return true }

func (LocalHost) Run(ctx context.Context, script string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("%w: %s", err, execx.Summary(stderr.String()))
	}
	return out, nil
}

// Stream runs script on h like Run, calling onLine for each line of its
// output as it arrives, until the script ends or ctx is done.
func Stream(ctx context.Context, h Host, script string, onLine func([]byte), args ...string) error {
	var cmd *exec.Cmd
	switch h := h.(type) {
	case Remote:
		remote := "sh -s --"
		for _, a := range args {
			remote += " " + Quote(a)
		}
		cmd = exec.CommandContext(ctx, "ssh", h.SSHArgs(remote)...)
		cmd.WaitDelay = time.Second
	case LocalHost:
		cmd = exec.CommandContext(ctx, "sh", append([]string{"-s", "--"}, args...)...)
	default:
		return fmt.Errorf("can't stream from %T", h)
	}
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		onLine(sc.Bytes())
	}
	err = cmd.Wait()
	if err != nil && ctx.Err() == nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 255 && !h.Local() {
			return fmt.Errorf("%w: %s", ErrUnreachable, orDefault(execx.Tail(stderr.String(), 1), "ssh failed"))
		}
		return fmt.Errorf("%w: %s", err, execx.Summary(stderr.String()))
	}
	return nil
}

// Output is what a command printed and how it exited.
type Output struct {
	Stdout, Stderr []byte
	ExitCode       int
	// Truncated is set when output passed the limit and was cut.
	Truncated bool
}

// Capture runs script on h like Run, but a non-zero exit is not an error:
// it comes back in Output.ExitCode with all of stderr. Errors are only for
// reaching the host (ErrUnreachable), starting the command, or ctx ending.
// Each stream keeps at most limit bytes.
func Capture(ctx context.Context, h Host, limit int, script string, args ...string) (Output, error) {
	var cmd *exec.Cmd
	switch h := h.(type) {
	case Remote:
		remote := "sh -s --"
		for _, a := range args {
			remote += " " + Quote(a)
		}
		cmd = exec.CommandContext(ctx, "ssh", h.SSHArgs(remote)...)
		cmd.WaitDelay = time.Second
	case LocalHost:
		cmd = exec.CommandContext(ctx, "sh", append([]string{"-s", "--"}, args...)...)
	default:
		return Output{}, fmt.Errorf("can't run on %T", h)
	}
	cmd.Stdin = strings.NewReader(script)
	stdout, stderr := &capped{limit: limit}, &capped{limit: limit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	out := Output{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), Truncated: stdout.cut || stderr.cut}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		out.ExitCode = exitErr.ExitCode()
		if out.ExitCode == 255 && !h.Local() {
			return out, fmt.Errorf("%w: %s", ErrUnreachable, orDefault(execx.Tail(string(out.Stderr), 1), "ssh failed"))
		}
		return out, nil
	}
	return out, err
}

// capped is a buffer that keeps the first limit bytes written to it.
type capped struct {
	buf   bytes.Buffer
	limit int
	cut   bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room < len(p) {
		c.cut = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// Quote quotes s for a POSIX shell.
func Quote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// controlDir is where ssh keeps its connection sockets, so repeated calls
// reuse one connection per host. Empty if it can't be created.
func controlDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(base, "careen", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
