package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
)

func newSecretsCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets [HOST...]",
		Short: "List hosts' secrets (names only; nothing is decrypted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			g.schema = actions.SecretsSchema
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			rep, err := actions.Secrets(rp, args)
			if err != nil {
				return err
			}
			if g.json {
				return contract.WriteJSON(cmd.OutOrStdout(), rep)
			}
			printSecrets(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.AddCommand(newSecretsEditCmd(g), newSecretsSetCmd(g), newSecretsUnsetCmd(g))
	return cmd
}

func hostFileArgs(cmd *cobra.Command, args []string, min, max int) error {
	if len(args) < min || len(args) > max {
		return contract.Usage("%s", cmd.Use)
	}
	return nil
}

func newSecretsEditCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "edit HOST [FILE]",
		Short: "Edit a host's secrets file in $EDITOR (sops decrypts and re-encrypts)",
		Args:  func(cmd *cobra.Command, args []string) error { return hostFileArgs(cmd, args, 1, 2) },
		RunE: func(cmd *cobra.Command, args []string) error {
			if !contract.Interactive() {
				return contract.Precondition("terminal", "edit needs a terminal; use careen secrets set to change a value without one")
			}
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			file := ""
			if len(args) == 2 {
				file = args[1]
			}
			c, path, err := actions.EditSecrets(cmd.Context(), rp, args[0], file)
			if err != nil {
				return err
			}
			_, statErr := os.Stat(rp.Path(path))
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return contract.Failed("sops", "%v", err)
			}
			if os.IsNotExist(statErr) {
				return rp.IntentToAdd(cmd.Context(), path)
			}
			return nil
		},
	}
}

func newSecretsSetCmd(g *globalFlags) *cobra.Command {
	var (
		file        string
		keepNewline bool
	)
	cmd := &cobra.Command{
		Use:   "set HOST NAME",
		Short: "Set a secret from stdin (or a hidden prompt in a terminal)",
		Long: "Set NAME in HOST's secrets file (--file, default secrets.yaml). The value is read\n" +
			"from stdin, so it never appears in a command line; in a terminal you're asked for\n" +
			"it without echo. One trailing newline is dropped unless --keep-newline. A new file\n" +
			"needs only public keys; changing an existing one needs the master key.",
		Args: func(cmd *cobra.Command, args []string) error { return hostFileArgs(cmd, args, 2, 2) },
		RunE: func(cmd *cobra.Command, args []string) error {
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			var value []byte
			if term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprintf(os.Stderr, "value for %s: ", args[1])
				value, err = term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
			} else {
				value, err = io.ReadAll(os.Stdin)
				if !keepNewline {
					value = []byte(strings.TrimSuffix(string(value), "\n"))
				}
			}
			if err != nil {
				return err
			}
			created, err := actions.SetSecret(cmd.Context(), rp, args[0], file, args[1], string(value))
			if err != nil {
				return err
			}
			if g.json {
				return contract.WriteJSON(cmd.OutOrStdout(), map[string]any{"host": args[0], "name": args[1], "created": created})
			}
			what := "updated"
			if created {
				what = "created"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s in %s's secrets (%s); review and commit.\n", what, args[1], args[0], rp.Dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "secrets file (default secrets.yaml)")
	cmd.Flags().BoolVar(&keepNewline, "keep-newline", false, "keep a trailing newline from stdin")
	return cmd
}

func newSecretsUnsetCmd(g *globalFlags) *cobra.Command {
	var (
		file string
		yes  bool
	)
	cmd := &cobra.Command{
		Use:   "unset HOST NAME",
		Short: "Remove a secret",
		Args:  func(cmd *cobra.Command, args []string) error { return hostFileArgs(cmd, args, 2, 2) },
		RunE: func(cmd *cobra.Command, args []string) error {
			em := newEmitter(g, cmd.OutOrStdout())
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			if err := confirm(em, yes, fmt.Sprintf("Remove %s from %s's secrets?", args[1], args[0])); err != nil {
				return err
			}
			if err := actions.UnsetSecret(cmd.Context(), rp, args[0], file, args[1]); err != nil {
				return err
			}
			em.Done(map[string]string{"host": args[0], "name": args[1]})
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "secrets file (default secrets.yaml)")
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func printSecrets(w io.Writer, rep actions.SecretsReport) {
	file := func(f actions.SecretFile) {
		if f.Error != nil {
			fmt.Fprintf(w, "  %s: %s\n", f.Path, f.Error.Message)
			return
		}
		note := ""
		if len(f.Missing) > 0 {
			note = "  (not encrypted to " + strings.Join(f.Missing, ", ") + ": careen keys rekey)"
		}
		fmt.Fprintf(w, "  %s, changed %s%s\n", f.Path, f.LastModified.Local().Format("2006-01-02"), note)
		for _, n := range f.Names {
			fmt.Fprintf(w, "    %s\n", n)
		}
	}
	for _, h := range rep.Hosts {
		fmt.Fprintln(w, h.Host)
		if len(h.Files) == 0 {
			fmt.Fprintln(w, "  (no secrets)")
		}
		for _, f := range h.Files {
			file(f)
		}
	}
	if len(rep.Shared) > 0 {
		fmt.Fprintln(w, "shared")
		for _, f := range rep.Shared {
			file(f)
		}
	}
}
