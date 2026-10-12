---
hide:
  - navigation
---

# careen

**Patch, reboot, provision and inspect a NixOS fleet defined in a flake.**

careen drives the tools you already have (`nix`, `ssh`, `sops`, OpenTofu) and keeps no state of
its own: your flake and the live hosts are the source of truth. It works the same from the
command line, from scripts (`--json`) and from a terminal UI.

!!! warning "Early development"
    careen runs a real homelab fleet, but its commands and JSON output may still change.

<div class="grid cards" markdown>

-   :material-radar:{ .lg .middle } **See the fleet**

    ---

    Drift from the flake, pending reboots, failed units, uptime and store use for every host,
    from one SSH call each.

    [:octicons-arrow-right-24: Status and drift](concepts/drift-and-revisions.md)

-   :material-rocket-launch:{ .lg .middle } **Deploy safely**

    ---

    Build, copy and switch hosts in parallel. A failed health check or a lost connection rolls
    the host back by itself.

    [:octicons-arrow-right-24: Deploy and roll back](guides/deploy-and-rollback.md)

-   :material-restart:{ .lg .middle } **Reboot one at a time**

    ---

    Only hosts that need it, per each host's reboot policy, waiting for each to come back
    healthy. Proxmox VMs with pending hardware changes are restarted through Proxmox.

    [:octicons-arrow-right-24: Reboots](guides/reboots.md)

-   :material-key-chain:{ .lg .middle } **Onboard hosts and secrets**

    ---

    Scaffold a host, record its key in `.sops.yaml`, re-key its secrets and set values without
    ever putting them on a command line.

    [:octicons-arrow-right-24: Onboarding, keys and secrets](guides/onboarding.md)

-   :material-server-plus:{ .lg .middle } **Provision VMs**

    ---

    Declare a VM next to its NixOS config; `careen up` clones it on Proxmox, installs its
    pre-generated host key and deploys it.

    [:octicons-arrow-right-24: Provisioning on Proxmox](guides/provisioning-proxmox.md)

-   :material-console:{ .lg .middle } **Script it or drive it live**

    ---

    Every read has versioned JSON output and every action an event stream. The TUI runs the
    same code with keyboard shortcuts.

    [:octicons-arrow-right-24: Scripting](guides/scripting.md) ·
    [:octicons-arrow-right-24: TUI](guides/tui.md)

</div>

## A quick tour

```console
$ export CAREEN_FLAKE=git+ssh://git@example.com/me/nix.git
$ careen status                      # what's drifted, what needs a reboot, what's failing
$ careen deploy --all --plan         # what a deploy would do
$ careen deploy --all --reboot       # deploy, then reboot where needed, per rebootPolicy
$ careen logs web1 nginx -f          # follow one unit's journal
$ careen tui                         # all of it, interactively
```

New here? Start with [Getting started](getting-started.md), or try it on a local VM in
[Your first fleet](tutorials/first-fleet.md). The [tutorials](tutorials/index.md) go on to secrets,
rolling reboots, provisioning on Proxmox and running a fleet day to day.
