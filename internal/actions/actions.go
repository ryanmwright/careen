// Package actions is careen's host-changing work (deploy, reboot,
// rollback), shared by the CLI commands and the TUI. Each action is planned
// first, then run, reporting through a contract.Emitter.
package actions

import (
	"context"
	"os"
	"slices"

	"github.com/ryanmwright/careen/internal/activate"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/sshx"
)

// ThisHost is the machine careen runs on, matched against inventory names.
var ThisHost, _ = os.Hostname()

func IsLocal(h inventory.Host) bool { return h.Name == ThisHost }

// Conn is how to run commands on h. fresh skips the shared ssh connection,
// for checks that must prove a new connection works.
func Conn(h inventory.Host, fresh bool) sshx.Host {
	if IsLocal(h) {
		return sshx.LocalHost{}
	}
	return sshx.Remote{User: h.User, Address: h.Address, NoMaster: fresh}
}

// IncludesLocal reports whether hosts include the machine careen runs on.
func IncludesLocal(hosts []inventory.Host) bool { return slices.ContainsFunc(hosts, IsLocal) }

// LocalSudo checks that careen can sudo on this machine without a
// password. When it can't, the caller decides how to get credentials: the
// CLI runs `sudo -v` in the terminal, the TUI suspends for it.
func LocalSudo(ctx context.Context) error {
	if _, err := activate.Run(ctx, sshx.LocalHost{}, "sudo-check"); err != nil {
		return contract.Precondition("sudo", "sudo on %s needs a password", ThisHost)
	}
	return nil
}

// InOnlyOrder reorders hosts to follow the only list, so the order hosts are
// named in is the order they're handled.
func InOnlyOrder(hosts []inventory.Host, only []string) []inventory.Host {
	if len(only) == 0 {
		return hosts
	}
	out := slices.Clone(hosts)
	slices.SortStableFunc(out, func(a, b inventory.Host) int {
		return slices.Index(only, a.Name) - slices.Index(only, b.Name)
	})
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
