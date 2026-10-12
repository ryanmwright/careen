# Scripting

Every careen command can be driven from scripts: reads print versioned JSON, actions print a
stream of events, and exit codes say what happened.

## Reads

With `--json`, a read command prints one JSON object. Every object starts with its `schema` and
the time it was made:

```console
$ careen status --json | jq '.hosts[] | select(.drift != "current") | .host'
```

```json
{
  "schema": "careen.status/v1",
  "generatedAt": "2026-10-11T17:52:40Z",
  "...": "..."
}
```

Check `schema` before relying on a field: the version in its name is how a script can tell which
format it's reading. [JSON output](../reference/json-output.md) lists every schema.

`--watch --json` prints one full snapshot per line until interrupted; a failed tick prints an
`error` line and the stream carries on.

## Actions

Actions (`deploy`, `reboot`, `up`, `gc`, `exec`, ...) print one `careen.events/v1` JSON object
per line as they run:

```json
{"schema":"careen.events/v1","time":"...","event":"started","data":{"...":"the plan"}}
{"schema":"careen.events/v1","time":"...","event":"progress","host":"web1","step":"switch","message":"switching"}
{"schema":"careen.events/v1","time":"...","event":"done","data":{"...":"the results"}}
```

| Event | Meaning |
| --- | --- |
| `started` | The action began; `data` is its plan. |
| `progress` | A step for one host (`host`, `step`, `message`). |
| `decision` | careen needs an answer (`message`, `options`). |
| `done` | The action finished; `data` holds per-host results. |
| `error` | A host or the action failed (`error.code`, `error.message`). |

**Under `--json` an action never prompts.** Without `--yes`, it emits a `decision` event and
exits 3, so a script either passes `--yes` or inspects the plan first:

```console
$ careen deploy --all --plan --json | jq .      # the plan only
$ careen deploy --all --yes --json | jq -c .    # run it
```

The same goes for questions in the middle of a run, such as a host's `pre` hook timing out during
a reboot: the `decision` event lists the flag values that answer it (`--on-timeout force`,
`--on-timeout skip`, ...), and careen exits 3. Run again with the one you choose.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success, or nothing to do. |
| `1` | The command ran and failed. |
| `2` | Usage error: bad flags or arguments, an unknown host. |
| `3` | A precondition wasn't met: a plan needs confirmation, the master key is missing, a host isn't ready yet. |

Reads exit 0 even when a host is unreachable (it's reported in the output). `careen doctor
--strict` exits 3 when a check fails, for scripts that gate on it.

## Errors

When a read fails under `--json`, it prints an object with its usual `schema` and an `error`
instead of its normal fields. A failure before careen knows which command is running (an unknown
command, say) uses `careen.error/v1`:

```json
{"schema":"careen.status/v1","generatedAt":"...","error":{"code":"usage","message":"unknown host \"web9\""}}
```

`code` is a short machine-readable word (`usage`, `inventory`, `reboot`, ...); `message` is for
people.
