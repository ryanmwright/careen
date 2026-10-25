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
	cmd.AddCommand(newSecretsEditCmd(g), newSecretsSetCmd(g), newSecretsUnsetCmd(g), newSecretsGroupCmd(g))
	return cmd
}

// targetArgs splits args into the target and the rest: with --group there
// is no HOST argument. Without the HOST, min and max count the rest.
func targetArgs(cmd *cobra.Command, args []string, group string, min, max int) (actions.SecretTarget, []string, error) {
	t := actions.SecretTarget{Group: group}
	if group == "" {
		if len(args) == 0 {
			return t, nil, contract.Usage("%s", cmd.Use)
		}
		t.Host, args = args[0], args[1:]
	}
	if len(args) < min || len(args) > max {
		return t, nil, contract.Usage("%s", cmd.Use)
	}
	return t, args, nil
}

func newSecretsEditCmd(g *globalFlags) *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "edit (HOST | --group GROUP) [FILE]",
		Short: "Edit a host's or a group's secrets file in $EDITOR (sops decrypts and re-encrypts)",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, rest, err := targetArgs(cmd, args, group, 0, 1)
			if err != nil {
				return err
			}
			if !contract.Interactive() {
				return contract.Precondition("terminal", "edit needs a terminal; use careen secrets set to change a value without one")
			}
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			file := ""
			if len(rest) == 1 {
				file = rest[0]
			}
			c, path, err := actions.EditSecrets(cmd.Context(), rp, t, file)
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
	cmd.Flags().StringVar(&group, "group", "", "edit a secret group's file instead of a host's")
	return cmd
}

func newSecretsSetCmd(g *globalFlags) *cobra.Command {
	var (
		file        string
		group       string
		keepNewline bool
	)
	cmd := &cobra.Command{
		Use:   "set (HOST | --group GROUP) NAME",
		Short: "Set a secret from stdin (or a hidden prompt in a terminal)",
		Long: "Set NAME in HOST's or GROUP's secrets file (--file, default secrets.yaml). The value is read\n" +
			"from stdin, so it never appears in a command line; in a terminal you're asked for\n" +
			"it without echo. One trailing newline is dropped unless --keep-newline. A new file\n" +
			"needs only public keys; changing an existing one needs the master key.",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, rest, err := targetArgs(cmd, args, group, 1, 1)
			if err != nil {
				return err
			}
			name := rest[0]
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			var value []byte
			if term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprintf(os.Stderr, "value for %s: ", name)
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
			created, err := actions.SetSecret(cmd.Context(), rp, t, file, name, string(value))
			if err != nil {
				return err
			}
			if g.json {
				return contract.WriteJSON(cmd.OutOrStdout(), targetJSON(t, map[string]any{"name": name, "created": created}))
			}
			what := "updated"
			if created {
				what = "created"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s in %s (%s); review and commit.\n", what, name, t, rp.Dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "secrets file (default secrets.yaml)")
	cmd.Flags().StringVar(&group, "group", "", "set it in a secret group's file instead of a host's")
	cmd.Flags().BoolVar(&keepNewline, "keep-newline", false, "keep a trailing newline from stdin")
	return cmd
}

func newSecretsUnsetCmd(g *globalFlags) *cobra.Command {
	var (
		file  string
		group string
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "unset (HOST | --group GROUP) NAME",
		Short: "Remove a secret",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, rest, err := targetArgs(cmd, args, group, 1, 1)
			if err != nil {
				return err
			}
			name := rest[0]
			em := newEmitter(g, cmd.OutOrStdout())
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			if err := confirm(em, yes, fmt.Sprintf("Remove %s from %s?", name, t)); err != nil {
				return err
			}
			if err := actions.UnsetSecret(cmd.Context(), rp, t, file, name); err != nil {
				return err
			}
			em.Done(targetJSON(t, map[string]any{"name": name}))
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "secrets file (default secrets.yaml)")
	cmd.Flags().StringVar(&group, "group", "", "remove it from a secret group's file instead of a host's")
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
	for _, gr := range rep.Groups {
		hosts := strings.Join(gr.Hosts, ", ")
		if hosts == "" {
			hosts = "no hosts"
		}
		fmt.Fprintf(w, "group %s (%s)\n", gr.Group, hosts)
		if len(gr.Files) == 0 {
			fmt.Fprintln(w, "  (no secrets)")
		}
		for _, f := range gr.Files {
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

// targetJSON is a result naming the target: "host" or "group", then fields.
func targetJSON(t actions.SecretTarget, fields map[string]any) map[string]any {
	if t.Group != "" {
		fields["group"] = t.Group
	} else {
		fields["host"] = t.Host
	}
	return fields
}

func newSecretsGroupCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group",
		Short: "Manage secret groups: secrets files shared by some hosts",
		Long: "A secret group is a directory under the flake's groupsDir whose files are encrypted\n" +
			"to the master keys and the group's hosts. It is a creation rule in .sops.yaml;\n" +
			"careen secrets lists the groups, and set, unset and edit take --group.",
	}
	groupArgs := func(min int) cobra.PositionalArgs {
		return func(cmd *cobra.Command, args []string) error {
			if len(args) < min {
				return contract.Usage("%s", cmd.Use)
			}
			return nil
		}
	}
	create := &cobra.Command{
		Use:   "create GROUP [HOST...]",
		Short: "Create a secret group: a creation rule for the master keys and the hosts",
		Args:  groupArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			em := newEmitter(g, cmd.OutOrStdout())
			rp, err := openRepo(cmd.Context(), g)
			if err != nil {
				return err
			}
			if err := actions.GroupCreate(rp, args[0], args[1:]); err != nil {
				return err
			}
			em.Done(map[string]any{"group": args[0], "hosts": args[1:]})
			if !g.json {
				fmt.Fprintf(cmd.OutOrStdout(), "Created group %s in %s; add secrets with careen secrets set --group %s NAME.\n", args[0], rp.Settings.SopsConfig, args[0])
			}
			return nil
		},
	}
	change := func(use, short, question string, join bool) *cobra.Command {
		var yes bool
		c := &cobra.Command{
			Use:   use + " GROUP HOST...",
			Short: short,
			Args:  groupArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := cmd.Context()
				em := newEmitter(g, cmd.OutOrStdout())
				rp, err := openRepo(ctx, g)
				if err != nil {
					return err
				}
				group, hosts := args[0], args[1:]
				if err := confirm(em, yes, fmt.Sprintf(question, strings.Join(hosts, ", "), group)); err != nil {
					return err
				}
				if join {
					err = actions.GroupAdd(ctx, em, rp, group, hosts)
				} else {
					err = actions.GroupRemove(ctx, em, rp, group, hosts)
				}
				if err != nil {
					return err
				}
				em.Done(map[string]any{"group": group, "hosts": hosts})
				if !g.json {
					if !join {
						fmt.Fprintf(cmd.OutOrStdout(), "%s can still read every value in group %s from the repo's history: rotate them.\n", strings.Join(hosts, ", "), group)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Done. Review `git -C %s diff` and commit.\n", rp.Dir)
				}
				return nil
			},
		}
		c.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
		return c
	}
	cmd.AddCommand(create,
		change("add", "Add hosts to a secret group and re-key its files", "Add %s to group %s and re-key its files?", true),
		change("remove", "Remove hosts from a secret group and re-key its files (then rotate its values)", "Remove %s from group %s and re-key its files?", false))
	return cmd
}
