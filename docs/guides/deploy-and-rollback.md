# Deploy and roll back

`careen deploy` builds each host's system from the flake, copies it to the host and switches to
it, then checks the host and rolls back if it isn't healthy.

```console
$ careen deploy --all --plan          # show what would happen
$ careen deploy --all                 # patch, no reboot
$ careen deploy --all --reboot        # then reboot where needed, per rebootPolicy
$ careen deploy --all --reboot=force  # then reboot every deployed host (not never)
$ careen deploy --only web1           # one host, even with deploy = false
```

`--all` is every host with `deploy = true`; other hosts deploy only when named with `--only`.
Every deploy shows its plan and asks before starting; `--plan` stops after the plan and `--yes`
skips the question.

## Where builds happen

Each host's `buildOn` decides where its system is built:

| `buildOn` | Builds on | Then |
| --- | --- | --- |
| `local` | the machine careen runs on | `nix copy` to the host |
| `remote` | the host's `builder` (a Nix store URI) | copied from there to the host |
| `target` | the host itself | nothing to copy |

`--build-on` and `--builder` override it for one run. Up to `--parallel` hosts (4) build and
switch at once; the machine careen runs on always goes last, on its own.

!!! note "Trusted users"
    careen copies unsigned store paths, so its SSH user must be a trusted Nix user on every
    host (`nix.settings.trusted-users`).

## How a switch is guarded

A deploy can't leave a host broken without noticing:

1. **Watchdog.** Before switching, careen arms a timer on the host that rolls back by itself
   after `--rollback-after` (5m) unless careen confirms in time. A switch that cuts careen off,
   such as a broken network config, still recovers. `--rollback-after 0` turns it off.
2. **Failed activation.** If `switch-to-configuration` fails, careen rolls back at once.
3. **Health check.** Otherwise careen waits `--settle` (10s), then checks the host over a *new*
   SSH connection: the system must be running and no unit may have newly failed within
   `--health-timeout` (2m). If not, it rolls back.

Hosts with `operation = "boot"` install the new system for the next boot instead of switching
now; they then need a reboot (reason `staged`).

## Rolling back by hand

```console
$ careen rollback web1
```

switches the host back to its previous system generation and checks its health the same way.

## First deploys

A machine installed from a generic image may not have your user yet. `--as` connects as another
user for that deploy:

```console
$ careen deploy --only web1 --as root
```

Hosts careen provisions handle this themselves; see
[Provisioning on Proxmox](provisioning-proxmox.md).
