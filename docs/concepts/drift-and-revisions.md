# Status and drift

`careen status` answers "is every host running what the flake says, and is it healthy?" with
one SSH call per host.

```console
$ careen status
$ careen status --watch              # redraw every --interval (30s)
$ careen status --tag web --json     # for scripts
```

For each host it shows:

| Column | Meaning |
| --- | --- |
| `REVISION` | The flake revision the host's running system was built from. |
| `DRIFT` | `current`, `differs` or `unknown` (below). |
| `REBOOT` | Whether a reboot is pending, and why (see [reboot reasons](reboot-policy.md#reboot-reasons)). |
| `STATE` | systemd's overall state (`running`, `degraded`, ...). |
| `FAILED` | How many units are in the `failed` state. |
| `UPTIME` | Time since the last boot. |
| `STORE` | Use of the filesystem holding `/nix/store`. |
| `GENS` | Number of system generations. |
| `SWITCHED` | When the host last switched to a new system. |

An unreachable host is a row with an error; the command still exits 0, so a status check never
fails just because one host is down.

## How drift is measured

careen resolves your flake reference to a commit (re-fetching it, so a branch's newest head
counts) and compares it with the revision each host reports in
`system.configurationRevision`. Set it in every host:

```nix
system.configurationRevision = self.rev or self.dirtyRev or null;
```

- **`current`:** the host runs the flake's commit.
- **`differs`:** the host runs another commit, usually an older one; deploying updates it.
- **`unknown`:** the host doesn't report a revision.

A host built from an uncommitted tree reports its revision with a `-dirty` suffix; careen
compares the commit underneath and marks the host as dirty.

Deploys build from the exact commit careen resolved, so every host in one run gets the same
revision.

## Uncommitted changes

careen refuses to deploy from a working tree with uncommitted changes unless you pass
`--allow-dirty`, because the result couldn't be traced back to a commit. Such a tree is also
never cached.

## How the probe works

`status` runs a small shell script on each host over a single SSH connection; it reads the
current and booted systems, the kernel and initrd, failed units, uptime and store use. SSH
connections are shared per host (in `~/.cache/careen/ssh/`) and stay open for 10 minutes after the
last use, so the many short commands careen runs during a deploy, and commands run soon after
each other, reuse one connection.
