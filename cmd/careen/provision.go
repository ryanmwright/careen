package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanmwright/careen/internal/actions"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/format"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/provision"
	"github.com/ryanmwright/careen/internal/provision/proxmox"
	"github.com/ryanmwright/careen/internal/repo"
)

// machineSetup loads what up, down and vm need: the fleet, the flake's
// careen settings and the provisioners.
func machineSetup(ctx context.Context, g *globalFlags, all bool) (*inventory.Inventory, repo.Settings, *actions.Provisioners, inventory.Selector, error) {
	sel := g.selector()
	if !all && len(sel.Only) == 0 && len(sel.Tags) == 0 {
		return nil, repo.Settings{}, nil, sel, contract.Usage("choose hosts with --all, --only or --tag")
	}
	inv, err := inventory.Load(ctx, g.timings.Wrap(execx.Run), g.flake, true)
	if err != nil {
		return nil, repo.Settings{}, nil, sel, err
	}
	if all && len(sel.Only) == 0 && len(sel.Tags) == 0 {
		// --all means every host careen provisions, never the whole fleet.
		for _, h := range inv.Hosts {
			if h.ProvisionerName() != inventory.Existing {
				sel.Only = append(sel.Only, h.Name)
			}
		}
		if len(sel.Only) == 0 {
			return nil, repo.Settings{}, nil, sel, contract.Usage("no host in the fleet is provisioned by careen")
		}
	}
	run := inventory.CacheEvals(inv.Flake.Locked, g.timings.Wrap(execx.Run))
	settings, err := repo.LoadSettings(ctx, run, inv.Flake.BuildRef())
	if err != nil {
		return nil, repo.Settings{}, nil, sel, contract.Failed("settings", "%v", err)
	}
	ps := actions.NewProvisioners(inv, decrypter(g))
	ps.Proxmox().Run = run
	return inv, settings, ps, sel, nil
}

// decrypter says on stderr (with a bell) when a master-key decryption is
// waiting on the security key, and, in a terminal, offers to retry a
// missed touch.
func decrypter(g *globalFlags) *actions.Decrypter {
	return &actions.Decrypter{Prompt: os.Stderr, Interactive: !g.json && contract.Interactive()}
}

func newUpCmd(g *globalFlags) *cobra.Command {
	var (
		all, planOnly, yes bool
		opts               actions.UpOptions
		rf                 rebootFlags
	)
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Create hosts' machines if needed, install their keys and deploy them",
		Long: `Bring hosts up, one at a time, in inventory order:

  1. the host's provisioner makes its machine exist and run (proxmox: a VM
     cloned from its node's template, per the flake's pve output)
  2. careen installs the host's pre-generated SSH key (from careen add) over
     a connection pinned to the key the machine booted with
  3. careen deploys the host; a first deploy over the generic image goes in
     as a boot entry and the host reboots into it

Each step is skipped when already done, so run it again to continue after a
failure. --all is every host careen provisions.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			if planOnly {
				g.schema, g.onError = actions.UpPlanSchema, nil
			}
			inv, settings, ps, sel, err := machineSetup(ctx, g, all)
			if err != nil {
				return err
			}
			if !g.json {
				actions.ReportFlake(ctx, os.Stderr, inv.Flake, g.repo)
			}
			plan, err := actions.PlanUp(ctx, inv, sel, ps, settings, opts.Deploy.AllowDirty)
			if err != nil {
				return err
			}
			if len(plan.Targets()) == 0 {
				return contract.Usage("no hosts to bring up")
			}
			if planOnly {
				if g.json {
					return contract.WriteJSON(out, plan)
				}
				printMachinePlan(out, "Bring up", plan)
				return nil
			}
			if !g.json {
				printMachinePlan(out, "Bring up", plan)
			}
			if err := confirm(em, yes, fmt.Sprintf("Bring up %d host(s)?", len(plan.Targets()))); err != nil {
				return err
			}
			opts.Deploy.HealthTimeout, opts.Deploy.RebootTimeout = rf.healthTimeout, rf.timeout
			done, err := actions.Up(ctx, em, plan, ps, opts)
			if !g.json {
				printMachineDone(out, done)
				printSlow(out, g.timings)
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "every host careen provisions")
	f.BoolVar(&planOnly, "plan", false, "show what would be done and stop")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	f.BoolVar(&opts.Deploy.AllowDirty, "allow-dirty", false, "use a flake with uncommitted changes")
	f.StringVar(&opts.Deploy.BuildOn, "build-on", "", "override where to build: local, remote or target")
	f.StringVar(&opts.Deploy.Builder, "builder", "", "override the remote builder's store URI")
	f.DurationVar(&opts.Deploy.Watchdog, "rollback-after", 5*time.Minute, "a host rolls back by itself unless careen confirms it within this time (0 turns it off)")
	f.DurationVar(&opts.Deploy.Settle, "settle", 10*time.Second, "wait this long after a switch before checking health")
	f.BoolVar(&opts.NoAgentWait, "no-agent-wait", false, "for a VM that exists, skip waiting for its QEMU guest agent and check its SSH key against the repo and known_hosts instead")
	rf.add(cmd)
	return cmd
}

func newDownCmd(g *globalFlags) *cobra.Command {
	var all, planOnly, yes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Destroy hosts' machines (VM and disks); their keys and secrets stay",
		Long: `Destroy the machines of hosts careen provisions, disks included, and forget
their keys in known_hosts. The hosts' configuration, pre-generated keys and
secrets stay in the repo, so careen up makes the same hosts again.

Hosts careen didn't provision are refused. --all is every host careen
provisions.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			if planOnly {
				g.schema, g.onError = actions.DownPlanSchema, nil
			}
			inv, settings, ps, sel, err := machineSetup(ctx, g, all)
			if err != nil {
				return err
			}
			plan, err := actions.PlanDown(ctx, inv, sel, ps, settings)
			if err != nil {
				return err
			}
			if planOnly {
				if g.json {
					return contract.WriteJSON(out, plan)
				}
				printMachinePlan(out, "Destroy", plan)
				return nil
			}
			if !g.json {
				printMachinePlan(out, "Destroy", plan)
			}
			if err := confirm(em, yes, fmt.Sprintf("Destroy %d machine(s), disks included?", len(plan.Targets()))); err != nil {
				return err
			}
			done, err := actions.Down(ctx, em, plan, ps, "")
			if !g.json {
				printMachineDone(out, done)
			}
			return err
		},
	}
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "every host careen provisions")
	f.BoolVar(&planOnly, "plan", false, "show what would be destroyed and stop")
	f.BoolVar(&yes, "yes", false, "don't ask for confirmation")
	return cmd
}

func newVMCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Show or apply hosts' machine hardware, without deploying",
	}
	var all, yes bool
	run := func(apply bool) func(cmd *cobra.Command, _ []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			if !apply {
				g.schema, g.onError = actions.VMPlanSchema, nil
			}
			inv, settings, ps, sel, err := machineSetup(ctx, g, all)
			if err != nil {
				return err
			}
			plan, err := actions.PlanVM(ctx, inv, sel, ps, settings)
			if err != nil {
				return err
			}
			if !apply {
				if g.json {
					return contract.WriteJSON(out, plan)
				}
				printMachinePlan(out, "Machines for", plan)
				return nil
			}
			if !g.json {
				printMachinePlan(out, "Apply machines for", plan)
			}
			if err := confirm(em, yes, fmt.Sprintf("Apply the machine changes for %d host(s)?", len(plan.Targets()))); err != nil {
				return err
			}
			done, err := actions.ApplyVM(ctx, em, plan, ps)
			if !g.json {
				printMachineDone(out, done)
			}
			return err
		}
	}
	plan := &cobra.Command{Use: "plan", Short: "Show what applying the inventory would change", Args: noArgs, RunE: run(false)}
	apply := &cobra.Command{Use: "apply", Short: "Make machines match the inventory (no deploy)", Args: noArgs, RunE: run(true)}
	for _, c := range []*cobra.Command{plan, apply} {
		c.Flags().BoolVar(&all, "all", false, "every host careen provisions")
	}
	apply.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	cmd.AddCommand(plan, apply)
	return cmd
}

func newTemplateCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage the base image new VMs are cloned from",
	}
	var (
		nodes        []string
		replace, yes bool
	)
	build := &cobra.Command{
		Use:   "build",
		Short: "Build the template host's Proxmox image and install it on each node",
		Long: `Build the flake's template host (pve.templateHost, default pve-base) as a Proxmox
image, then restore it on each Proxmox node (all, or --node) as that node's
template VM (pve.nodes.<node>.template), over SSH as root. A node that already
has its template keeps it unless --replace. VMs are full clones, so replacing
a template doesn't affect them.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			em := newEmitter(g, out)
			inv, err := inventory.Load(ctx, execx.Run, g.flake, true)
			if err != nil {
				return err
			}
			action := "Install"
			if replace {
				action = "Install or replace"
			}
			if !g.json {
				fmt.Fprintf(out, "%s the template on %s from %s\n", action, nodesOrAll(nodes), format.FlakeLine(inv.Flake))
			}
			if err := confirm(em, yes, action+" the templates?"); err != nil {
				return err
			}
			em.Started("template build", nil)
			px := actions.NewProvisioners(inv, decrypter(g)).Proxmox()
			res, err := px.BuildTemplates(ctx, nodes, replace, func(node, step, msg string) { em.Progress(node, step, "%s", msg) })
			em.Done(res)
			if !g.json {
				printTemplates(out, res)
			}
			if err != nil {
				return contract.Failed("template", "%v", err)
			}
			return nil
		},
	}
	build.Flags().StringSliceVar(&nodes, "node", nil, "only these Proxmox nodes (comma-separated)")
	build.Flags().BoolVar(&replace, "replace", false, "rebuild templates that already exist")
	build.Flags().BoolVar(&yes, "yes", false, "don't ask for confirmation")
	cmd.AddCommand(build)
	return cmd
}

func nodesOrAll(nodes []string) string {
	if len(nodes) == 0 {
		return "every node"
	}
	return fmt.Sprint(nodes)
}

func printMachinePlan(w io.Writer, verb string, p *actions.MachinePlan) {
	fmt.Fprintf(w, "%s %d host(s) from %s\n", verb, len(p.Hosts), format.FlakeLine(p.Flake))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, h := range p.Hosts {
		key := "-"
		if h.HostKey != "" {
			key = "key " + h.HostKey
		}
		machine := h.Machine
		if machine == provision.NoOp {
			machine = "up to date"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", h.Host, h.Address, h.Provisioner, machine, key)
	}
	tw.Flush()
	for _, s := range p.Skipped {
		fmt.Fprintf(w, "  skipping %s: %s\n", s.Host, s.Reason)
	}
}

func printMachineDone(w io.Writer, d actions.MachineDone) {
	if len(d.Hosts) == 0 {
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tMACHINE\tKEY\tDEPLOY\tNOTE")
	for _, r := range d.Hosts {
		key, dep, note := "-", "-", ""
		if r.KeyInstalled {
			key = "installed"
		}
		if r.Deploy != nil {
			dep = r.Deploy.Status
			if r.FirstDeploy {
				dep += " (first, rebooted)"
			}
		}
		if r.Error != nil {
			note = r.Error.Message
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Host, r.Machine, key, dep, note)
	}
	tw.Flush()
}

func printTemplates(w io.Writer, res []proxmox.TemplateResult) {
	if len(res) == 0 {
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tVMID\tTEMPLATE\tRESULT")
	for _, r := range res {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", r.Node, r.VMID, r.Name, r.Status)
	}
	tw.Flush()
}

// printSlow lists the commands that took long, to show where a slow run
// went.
func printSlow(w io.Writer, t *execx.Timings) {
	slow := t.Slow(2 * time.Second)
	if len(slow) == 0 {
		return
	}
	fmt.Fprintln(w, "\nSlow commands:")
	for _, it := range slow {
		fmt.Fprintf(w, "  %6s  %s\n", it.Duration.Round(100*time.Millisecond), execx.Tail(it.What, 1))
	}
}
