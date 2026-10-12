# Reboot policy

Each host's `rebootPolicy` says when careen may reboot it.

| Policy | `careen reboot` / `deploy --reboot` | With `--force` |
| --- | --- | --- |
| `auto` | rebooted when it needs it | always rebooted |
| `manual` | only when named with `--only` | rebooted |
| `never` | skipped | skipped |

Use `manual` for hosts where a reboot needs a human: cluster members, a router, the machine
careen runs on. Use `never` for hosts careen should not reboot at all.

A tag is not a name: `careen reboot --tag cluster` skips `manual` hosts. Name them with `--only`, or
use `--force`, which also reboots hosts that don't need it.

## Reboot reasons

careen reboots a host only when it needs it (unless `--force`). The reasons it reports:

| Reason | Meaning |
| --- | --- |
| `kernel` | The booted kernel differs from the current system's. |
| `initrd` | The booted initrd differs. |
| `kernel-modules` | The kernel modules changed. |
| `staged` | A new system was installed for the next boot (`operation = "boot"`). |
| `hardware` | A Proxmox VM has hardware changes waiting for a restart from outside (see [Reboots](../guides/reboots.md#proxmox-hardware-changes)). |

## Order and safety

- **One host at a time.** careen waits for the host to come back on a new boot and for its
  systemd state to be healthy before moving on, and stops at the first host that doesn't.
- **Order:** the order of `--only` when given, otherwise each host's `rebootOrder` (lowest
  first), then host name.
- **Hooks:** a host can have commands that run around its reboot (take it out of service, put it
  back, wait for things to settle). See [Rolling reboots](../guides/reboots.md#rolling-reboots).
- **The machine careen runs on** is never rebooted unless you pass `--reboot-self`, whatever its
  policy and even with `--force`. It then goes last, and careen exits.

With hooks and gates guarding each step, hosts that were `manual` only so a person could check
between reboots can often become `auto`.
