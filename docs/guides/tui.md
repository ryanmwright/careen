# The TUI

```console
$ careen tui
$ careen tui --tag web --interval 10s
```

`careen tui` shows the [`status`](../concepts/drift-and-revisions.md) table live, with a detail
pane for the selected host, and runs every action from the keyboard. Each dialog shows the same
plan `--plan` prints, and actions run the same code as the commands, so anything you do here you
can also script.

On start the TUI probes the hosts of the fleet it loaded last, so the table shows at once, and marks
it *refreshing…* until the flake has been fetched and evaluated. Until then, drift is measured against
the commit it saw last. After a new commit, especially one that changes `flake.lock`, that
evaluation can take a while.

## Keys

| Key | Action |
| --- | --- |
| ++up++ ++down++ / ++j++ ++k++ | Move (or pick a failed unit after ++tab++) |
| ++space++ | Mark a host; actions use marked hosts in the order marked, else the host under the cursor |
| ++a++ / ++shift+a++ | Mark all / clear marks |
| ++d++ | Deploy; choose the reboot mode in the dialog (none, per policy, force) |
| ++r++ | Reboot hosts that need it, per `rebootPolicy`; ++f++ in the dialog forces |
| ++b++ | Roll the host back to its previous generation |
| ++tab++ | Move between the host table and the host's failed units |
| ++l++ | Follow the logs of the picked failed unit, or of the whole host |
| ++s++ | Open a shell on the host (ssh); exit it to come back |
| ++shift+g++ | Collect garbage: generations older than 14 days and unreachable store paths |
| ++x++ | Run a shell command on the hosts; ++tab++ in the prompt runs it as root |
| ++o++ | Show the last exec's output again |
| ++v++ | Scan the hosts for known CVEs with vulnix; ++r++ rescans |
| ++plus++ | Add a host: a form, a preview of the files and the `.sops.yaml` diff, then the steps |
| ++shift+k++ | sops keys: recorded vs live, files not re-keyed; register and re-key |
| ++shift+s++ | Secrets by host (names only): edit, add, change, remove, re-key |
| ++g++ | Refresh now |
| ++question++ | Help |
| ++q++ | Quit |

## When a reboot needs an answer

If a host's `pre` [hook](reboots.md#when-pre-times-out) times out during a reboot or a deploy
with reboots, the TUI shows the question under the activity log and waits:

| Key | Answer |
| --- | --- |
| ++w++ | wait: run `pre` again |
| ++f++ | force: run the host's `force` command instead (when it has one) |
| ++s++ | skip: undo `pre` with `post` and leave this host |
| ++a++ | abort: stop, leaving the host as `pre` left it |
| ++esc++ | no answer: stop, like abort |

Other keys are ignored while the question is open, so a stray key can't answer it. The reboot and
deploy dialogs list each host's hooks before you confirm.

## Defaults

The TUI's flags set the defaults its actions use, the same as the matching commands'
flags: `--parallel`, `--settle`, `--health-timeout`, `--rollback-after`, `--reboot-timeout`,
`--build-on`, `--builder`, `--older-than` (gc), `--exec-timeout`, and `--on-timeout` and
`--no-hooks` for [rolling reboots](reboots.md#rolling-reboots). See the
[CLI reference](../reference/cli.md#careen-tui).
