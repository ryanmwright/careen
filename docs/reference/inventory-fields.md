# Inventory fields

The fields careen reads from each entry of the flake's `fleet` output. The entry's attribute name
is the host's name. See [The inventory](../concepts/inventory.md) for how to generate the output.

!!! note
    A test fails when careen reads a field this page doesn't list.

| Field | Type | Meaning |
| --- | --- | --- |
| `kind` | string | What the host is. Only `"nixos"` today. |
| `address` | string | SSH destination: an IP address or DNS name. |
| `user` | string | SSH user careen connects as. Needs passwordless sudo and must be a trusted Nix user. |
| `tags` | list of strings | Labels for selecting hosts with `--tag`. May be empty. |
| `rebootPolicy` | `"auto"`, `"manual"` or `"never"` | When careen may reboot the host; see [Reboot policy](../concepts/reboot-policy.md). |
| `deploy` | bool | Whether `--all` includes the host. Hosts with `false` deploy only when named with `--only`. |
| `buildOn` | `"local"`, `"remote"` or `"target"` | Where the host's system is built; see [Deploy](../guides/deploy-and-rollback.md#where-builds-happen). |
| `builder` | string | Nix store URI of the remote builder, used when `buildOn = "remote"` (for example `ssh-ng://admin@builder.example`). |
| `operation` | `"switch"` or `"boot"` | `boot` installs the new system for the next boot instead of activating it now. |
| `provisioner` | `"existing"` or `"proxmox"` | How the machine comes to exist. Empty means `existing`: the machine is already there. `proxmox`: `careen up` creates its VM; see [Provisioning on Proxmox](../guides/provisioning-proxmox.md). |

| `rebootOrder` | int | Optional. Reboots go in this order, lowest first, ties by name; `--only`'s order overrides it, and the machine careen runs on is always last. Default 0. |
| `reboot` | attribute set | Optional. Commands careen runs around the host's reboot; see [Rolling reboots](../guides/reboots.md#rolling-reboots) and the table below. |

careen doesn't fill in defaults for most fields; give every required field a default in the module
that builds `fleet`. `provisioner`, `rebootOrder` and `reboot` may be left out.

### `reboot`

| Field | Type | Meaning |
| --- | --- | --- |
| `pre` | hook | Runs before the reboot, e.g. to take the host out of service. |
| `post` | hook | Runs once the host is back and healthy, e.g. to put it back. |
| `gate` | hook | Rerun until it succeeds before careen moves on to the next host. |
| `settle` | duration | A further wait after the gate, for things with nothing to check. |

Each hook is an attribute set:

| Field | Type | Meaning |
| --- | --- | --- |
| `run` | string | The shell command. A Go template: `{{.Name}}` and `{{.Address}}` are the host being rebooted. |
| `on` | string | Where it runs: `self` (default), `local` (the machine careen runs on) or `tag:<tag>` (the first other host with that tag that answers). |
| `timeout` | duration | How long it may take. Defaults: `pre` 10m, `post` 5m, `gate` 10m. Exit code 124 (from `timeout(1)`) also counts as timing out. |
| `onTimeout` | string | `pre` only: what to do when it times out. `ask` (default), `force`, `skip` or `abort`. |
| `force` | string | `pre` only: a command run instead when the answer to a timeout is `force`. A template like `run`. |
| `interval` | duration | `gate` only: the wait between tries. Default 10s. |

Durations are Go durations: `30s`, `5m`, `1h30m`.

## Related settings on the host

These aren't fleet fields, but careen relies on them:

- `system.configurationRevision = self.rev or self.dirtyRev or null;` so
  [drift](../concepts/drift-and-revisions.md) can be measured.
- `nix.settings.trusted-users` including careen's SSH user, so careen can copy unsigned paths.
- Passwordless sudo for that user (`security.sudo.wheelNeedsPassword = false`, or a rule).
