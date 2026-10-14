# Environment and files

## Environment variables

| Variable | Used for |
| --- | --- |
| `CAREEN_FLAKE` | The flake that defines the fleet; the default for `--flake`. Any flake reference: `git+ssh://...`, `github:...`, a path. |
| `CAREEN_REPO` | A writable checkout of the flake, for `add`, `keys` and `secrets`; the default for `--repo`. Without either, careen looks for a `flake.nix` in the current directory and its parents. A leading `~/` is your home directory. |
| `EDITOR` | The editor `careen secrets edit` opens through sops. |
| `SOPS_AGE_KEY`, `SOPS_AGE_KEY_FILE` | The age identities sops uses; careen checks age master keys against them (and `~/.config/sops/age/keys.txt`). |
| `SHELL` | The shell the TUI returns to after ++s++ (ssh). |
| `XDG_STATE_HOME` | Where provisioner state lives (default `~/.local/state`). |
| `XDG_CACHE_HOME` | Where caches live (default `~/.cache`). |

## Files careen keeps

careen keeps no state of its own about hosts, only caches and the OpenTofu state of the machines
it provisions.

| Path | What | Safe to delete? |
| --- | --- | --- |
| `~/.cache/careen/inventory/` | The evaluated `fleet` per locked flake URL, the `careen` and `pve` outputs `careen up` reads (`eval-*.json`, also per locked URL), and the last fleet loaded per flake reference (`last-*.json`), which the TUI shows while it starts. | Yes; the next command evaluates again. |
| `~/.cache/careen/ssh/` | SSH connection sockets, one per host, shared between commands and kept open for 10 minutes. | Yes. |
| `~/.cache/careen/vulns/` | The last vulnix report per host. | Yes; the next `careen vulns` rescans. |
| `~/.local/state/careen/pve/` | The rendered OpenTofu config, careen's override (`careen_override.tf.json`, no provider reboots), the provider lock file and **OpenTofu's state** for Proxmox VMs. | **No.** Without the state, careen no longer knows the VMs it created. Back it up. |

## Files careen reads from the flake

| Path | Used by |
| --- | --- |
| The `fleet` output | every command; see [Inventory fields](inventory-fields.md) |
| The `careen` output | `add`, `keys`, `secrets`; see [Flake settings](../concepts/flake-settings.md) |
| The `pve` output and `pve-tofu`, `opentofu` packages | `up`, `down`, `vm`, `template`, Proxmox reboots; see [Provisioning on Proxmox](../guides/provisioning-proxmox.md) |
| `vulnix-whitelist.toml` at the root | `vulns` |
| `.sops.yaml` (the `sopsConfig` setting) | `add`, `keys`, `secrets`, `doctor` |

## Tools careen runs

careen orchestrates existing tools rather than reimplementing them. `careen doctor` checks for
them.

| Tool | Where | Needed for |
| --- | --- | --- |
| `nix` | where careen runs | everything |
| `ssh` | where careen runs | everything that touches a host |
| `sops` | where careen runs | `add`, `keys`, `secrets`, the Proxmox API token |
| `gpg` | where careen runs | PGP master keys |
| `age-keygen` | where careen runs | checking age master keys |
| `vulnix` | where careen runs | `vulns` |
| `opentofu` | from your flake (`.#opentofu`) | provisioning |
| `qm`, `pvesh` | on the Proxmox nodes | templates, hardware restarts |
