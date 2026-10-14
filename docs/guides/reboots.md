# Reboots

`careen reboot` reboots the hosts that need it, one at a time, following each host's
[reboot policy](../concepts/reboot-policy.md).

```console
$ careen reboot --plan                 # who needs a reboot, and whether careen would do it
$ careen reboot                        # auto hosts that need it
$ careen reboot --only db1,db2         # also manual hosts, in this order
$ careen reboot --tag web --force      # every selected host, needed or not (never still skips)
```

The plan lists each host with what it needs and what careen will do:

```text
Reboot (policy), one at a time in this order:
  db1   needs: kernel    reboot
  db2   needs: -         skip: no reboot needed
  web1  needs: staged    skip: rebootPolicy is manual; name it with --only
```

For each host, careen notes the current boot id, reboots it, waits up to `--reboot-timeout` (10m)
for a new boot id, and then checks its health within `--health-timeout` (2m). It stops at the
first host that doesn't come back healthy; the rest are left untouched.

`careen deploy --reboot` runs the same reboots after a deploy.

## Rolling reboots

A host that serves something (behind a load balancer, in a cluster, as a database replica) needs
more than a reboot: take it out of service first, put it back after, and wait until everything has
settled before touching the next one. Give the host `reboot` hooks in its fleet entry and careen
runs them around every reboot, from `careen reboot`, `deploy --reboot` and the TUI.

```nix
fleet.web1 = {
  # ...
  rebootOrder = 20;              # after anything with a lower order
  reboot = {
    pre = {
      run = "timeout 4m lbctl drain {{.Name}}";   # take it out of the pool
      on = "tag:lb";                              # on a load balancer
      timeout = "5m";
      force = "lbctl disable {{.Name}}";          # if draining takes too long
    };
    post = { run = "lbctl enable {{.Name}}"; on = "tag:lb"; };
    gate = {
      run = "curl -fsS http://{{.Address}}/healthz";
      on = "local";                               # from the machine careen runs on
      timeout = "5m";
      interval = "5s";
    };
    settle = "10s";
  };
};
```

For each host, in order:

1. **pre** runs; if it fails, the run stops before the reboot.
2. careen reboots the host and waits for it to come back healthy.
3. **post** runs.
4. **gate** runs every `interval` until it succeeds, for up to its `timeout`.
5. careen waits **settle**, then moves on to the next host.

A failing `post`, or a gate that doesn't pass in time, stops the run: the remaining hosts are left
alone. A cluster node fits the same shape (drain it, uncordon it, gate on the cluster being
healthy); the commands are yours, careen only runs them.

`run` and `force` are Go templates with `{{.Name}}` and `{{.Address}}` of the host being rebooted.
`on` says where a hook runs: `self` (the default; a fresh connection, since the host reboots in
between), `local`, or `tag:<tag>`, the first *other* host with that tag that answers. careen checks
every hook (durations, `on`, templates) when it makes the plan, so a mistake stops the run before
anything happens. The plan lists each host's hooks.

### When pre times out

| Answer | What careen does |
| --- | --- |
| wait | Runs `pre` again for another `timeout`. Only offered when asked interactively. |
| force | Runs the host's `force` command instead, then reboots. The summary notes it. Offered only when `force` is set. |
| skip | Runs `post` to undo `pre`, leaves this host unrebooted, and goes on to the next. |
| abort | Stops the run and leaves the host as `pre` left it; the summary shows the `post` command to run by hand. |

Who answers:

- `--on-timeout force|skip|abort` answers for every host in the run;
- otherwise the host's `onTimeout`, if it isn't `ask`;
- otherwise careen asks: on the terminal, or in the TUI's prompt. Under `--json`, or without a
  terminal, it emits a `decision` event listing the `--on-timeout` values and exits 3, so a script
  can choose and run again.

A hook counts as timed out when it outlasts its `timeout`, or when it exits 124, the code
[`timeout(1)`](https://man7.org/linux/man-pages/man1/timeout.1.html) uses. Prefer the second: wrap
the command in `timeout`, a little shorter than the hook's `timeout` (as `timeout 4m` above). When
careen's own timeout fires it can only stop waiting and close its connection, and the command may
keep running on the host; `timeout(1)` ends it there cleanly. A command's own timeout option
usually exits 1, which careen treats as a failure, not a timeout.

### Leaving hooks out

- `--no-hooks` reboots without running any hooks, for when the hooks themselves can't work (the
  service they talk to is down). The plan says so.
- `careen up` never runs them for a machine's first reboot into its own system: it isn't in service
  yet.
- For the machine careen runs on, `pre` runs but `post` and `gate` can't: careen stops when it
  reboots.

## Proxmox hardware changes

Some VM hardware changes, such as a disk's IO thread or the boot order, only apply when the VM is
restarted from *outside*: a reboot inside the guest keeps the same QEMU process. careen applies
hardware changes without rebooting a running VM (see
[Provisioning on Proxmox](provisioning-proxmox.md#day-2-changing-hardware)), so such changes wait
in Proxmox as pending.

For hosts with `provisioner = "proxmox"`, `careen reboot` and `deploy --reboot` ask the VM's
Proxmox node for pending changes, over SSH as root:

- A VM with pending changes needs a reboot with the reason `hardware`.
- careen restarts it with `qm reboot` on its node, which shuts it down and starts it with the new
  configuration, instead of rebooting it over SSH. A host whose config doesn't run the guest
  agent gets an ACPI power off from its node instead (`qm reboot` would wait on the agent), and is
  started once it has stopped, within three minutes.
- Everything else is the same: one at a time, policy respected, health checked.

If careen can't reach a VM's node, that host is skipped with "can't check its machine".

## The machine careen runs on

It's never rebooted unless you pass `--reboot-self`, whatever its policy and even with `--force`.
It then goes last, and careen exits when it goes down.
