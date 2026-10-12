# JSON output

Every JSON object careen prints names its format in `schema`. This page lists them all; see
[Scripting](../guides/scripting.md) for how to use them, the event stream and exit codes.

!!! note
    A test fails when the code prints a schema this page doesn't list, so this list is complete.

## Reads

Printed once by a read command with `--json` (or once per line with `--watch --json`).

| Schema | Printed by | Contents |
| --- | --- | --- |
| `careen.hosts/v1` | `careen hosts` | The inventory: the flake and every host's fleet entry. |
| `careen.status/v1` | `careen status` | The flake's revision and, per host, drift, reboot reasons, failed units, uptime and store use, or an `error`. |
| `careen.logs/v1` | `careen logs` | Journal entries of a host or unit. |
| `careen.logs.entry/v1` | `careen logs --follow` | One line per journal entry, streamed. |
| `careen.vulns/v1` | `careen vulns` | Per host: the scanned system and its CVEs. |
| `careen.keys/v1` | `careen keys` | Per host: recorded and live keys, their state and files not re-keyed. |
| `careen.secrets/v1` | `careen secrets` | Per host: secrets files and the names in them (never values). |
| `careen.add/v1` | `careen add` | Each onboarding step per host and whether it's done (also with `--status`). |
| `careen.doctor/v1` | `careen doctor` | Each check and its result. |
| `careen.error/v1` | any command | An error before a command chose its own schema (an unknown command, for example). |

## Plans

Printed by an action with `--plan --json`, and as the `data` of its `started` event.

| Schema | Printed by | Contents |
| --- | --- | --- |
| `careen.deploy.plan/v1` | `careen deploy` | Per host: address, where it builds, the operation; hosts skipped and why; the reboot mode. |
| `careen.reboot.plan/v1` | `careen reboot` | Hosts in reboot order with their reasons and whether each is rebooted or skipped (and why). |
| `careen.gc.plan/v1` | `careen gc` | The hosts and the age limit. |
| `careen.exec.plan/v1` | `careen exec` | The hosts and the command. |
| `careen.up.plan/v1` | `careen up` | Per host: its provisioner, the machine change (create, update, no-op) and the steps left. |
| `careen.down.plan/v1` | `careen down` | The machines to destroy. |
| `careen.vm.plan/v1` | `careen vm plan`, `vm apply` | Per host: the machine change OpenTofu would make. |

## Events

| Schema | Printed by | Contents |
| --- | --- | --- |
| `careen.events/v1` | every action | One object per line: `started`, `progress`, `decision`, `done`, `error`. See [Scripting](../guides/scripting.md#actions). |

## Example: `careen.status/v1`

```json
{
  "schema": "careen.status/v1",
  "generatedAt": "2026-10-09T12:00:00Z",
  "flake": {
    "ref": "git+ssh://example/nix?ref=main",
    "revision": "3c1e9a0b7d2f4e6a8c0b1d3f5a7c9e1b2d4f6a8c",
    "dirty": false
  },
  "hosts": [
    {
      "host": "app1",
      "address": "10.0.0.40",
      "revision": "7a2b4c6d8e0f1a3b5c7d9e1f3a5b7c9d1e3f5a7b",
      "dirty": true,
      "drift": "differs",
      "rebootPending": true,
      "rebootReasons": ["kernel", "kernel-modules"],
      "systemState": "degraded",
      "failedUnits": ["nginx.service"],
      "uptimeSeconds": 273600,
      "storeUsedBytes": 42949672960,
      "storeSizeBytes": 107374182400,
      "generations": 14,
      "lastSwitch": "2026-10-08T14:02:00Z",
      "nixosVersion": "26.05"
    },
    {
      "host": "mgr",
      "address": "mgr.example",
      "error": {
        "code": "unreachable",
        "message": "ssh: connect to host mgr.example port 22: No route to host"
      }
    }
  ]
}
```

## Reboot plans and results

Each host in a `careen.reboot.plan/v1` (and in a reboot's `done` event, and a deploy result's
`reboot`) also has:

| Field | Meaning |
| --- | --- |
| `rebootOrder` | The host's `rebootOrder`. |
| `hooks` | What runs around its reboot, e.g. `"pre on tag:lb (force available)"`, `"gate on local (up to 5m0s)"`. |
| `hookRuns` | After the run: each hook run, with `hook` (`pre`, `force`, `post`, `gate`), `on`, `seconds`, `exitCode`, `timedOut` and the last line of `output`. |
| `forced` | `pre` timed out and the host's `force` command ran instead. |
| `manual` | What's left to do by hand, e.g. the `post` command after an aborted `pre`. |

The plan's `noHooks` is set when `--no-hooks` turns the hooks off.

## Deploy results

The `done` event of a deploy carries one result per host. Its `status` is one of `switched`,
`staged` (installed for the next boot), `unchanged`, `rolled-back` or `failed`, with
`rebootPending` listing why the host needs a reboot afterwards and `reboot` its outcome when
`--reboot` was given.
