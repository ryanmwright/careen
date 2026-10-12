# careen

Patch, reboot, provision and inspect a NixOS fleet defined in a flake.

**Documentation: <https://ryanmwright.github.io/careen/>**

> **Status: early development.** careen runs a real homelab fleet, but its commands and JSON
> output may still change.

careen drives the tools you already have (`nix`, `ssh`, `sops`, OpenTofu) and keeps no state of
its own: your flake and the live hosts are the source of truth.

```console
$ export CAREEN_FLAKE=git+ssh://git@example.com/me/nix.git
$ careen status                      # drift, pending reboots, failed units, uptime, store use
$ careen deploy --all --reboot       # deploy, then reboot where needed, one host at a time
$ careen add web1 --address 10.0.0.20   # onboard a host: module, sops keys, secrets
$ careen up --tag cluster            # create Proxmox VMs, install their keys, deploy them
$ careen tui                         # all of it, interactively
```

- **Fleet operations:** `status`, `deploy`, `reboot`, `rollback`, `logs`, `gc`, `exec`, `vulns`
- **Onboarding and sops:** `add`, `keys`, `secrets`, `doctor`
- **Provisioning:** `up`, `down`, `vm`, `template` (Proxmox, through OpenTofu)
- **Scripting:** versioned `--json` output and event streams; a TUI over all of it

## Install

```console
$ nix run github:ryanmwright/careen -- --help
```

See [Getting started](https://ryanmwright.github.io/careen/getting-started/) to add it to a
NixOS configuration and describe your fleet.

## Development

```console
$ nix develop
$ go test ./...
$ nix flake check          # careen, its tests and the documentation site
$ scripts/docs.sh serve    # preview the docs
```

See [CONTRIBUTING.md](CONTRIBUTING.md); docs change in the same pull request as the code.
