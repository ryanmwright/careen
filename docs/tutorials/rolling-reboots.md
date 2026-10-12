# Rolling reboots behind a load balancer

Reboot web servers one at a time without dropping the site: take each out of the load balancer,
reboot it, put it back, and wait until the load balancer sees it healthy before touching the next.
careen runs the commands; you say what they are.

!!! info "What you need"
    Everything from [Your first fleet](first-fleet.md); this tutorial runs three VMs (3 GB of memory).

## 1. Start from the template

```console
$ nix flake new -t github:ryanmwright/careen#rolling my-site
$ cd my-site && git init -q && git add -A
$ nix develop
$ export CAREEN_FLAKE=$PWD
```

Three hosts: `lb` runs haproxy in front of `web1` and `web2`. The interesting part is the web
servers' `reboot` hooks:

=== "web.nix"

    ```nix
    --8<-- "rolling/web.nix"
    ```

=== "hosts/lb.nix"

    ```nix
    --8<-- "rolling/hosts/lb.nix"
    ```

- **`pre`** drains the server in haproxy, then takes it out. It runs `on = "tag:lb"`: on the load
  balancer, not on the server being rebooted.
- **`post`** puts it back once it has rebooted and is healthy.
- **`gate`** waits until haproxy's own health check reports it `UP`, so careen never takes out the
  second server while the first is still on its way back.
- **`rebootOrder`** and `lb`'s `rebootPolicy = "manual"` keep the load balancer out of a routine
  reboot.

Every field is described in [Inventory fields](../reference/inventory-fields.md#reboot).

## 2. Boot the site

Make the demo key as in [Your first fleet](first-fleet.md#2-a-key-and-an-ssh-alias), and add three
SSH aliases: `lb-vm` (port 2201), `web1-vm` (2202) and `web2-vm` (2203). Commit, so the images carry
the flake's revision (`git commit -qm "my site"`), then boot each in its own terminal:

```console
$ nix run .#vm-lb
$ nix run .#vm-web1
$ nix run .#vm-web2
```

The site is lb's port 80, forwarded to 8001. Leave a loop running to watch it:

```console
$ while sleep 0.5; do curl -s 127.0.0.1:8001 || echo DOWN; done
hello from web1
hello from web2
...
```

## 3. Roll the web servers

```console
$ careen reboot --tag web --force --plan
Reboot (force), one at a time in this order:
  web1  needs: -  reboot
      hooks: pre on tag:lb (force available), post on tag:lb, gate on tag:lb (up to 2m0s), settle 5s
  web2  needs: -  reboot
      hooks: pre on tag:lb (force available), post on tag:lb, gate on tag:lb (up to 2m0s), settle 5s
$ careen reboot --tag web --force
```

`--force` reboots them even though nothing requires it. Watch the curl loop: while web1 is out, every
answer comes from web2, and the other way round. It never says `DOWN`.

In real use you'd drop `--force`: careen then reboots only the servers that need it (a new kernel,
say), in the same careful way, for example after `careen deploy --tag web --reboot`.

## 4. When a drain takes too long

Real drains can hang: a long download, a stuck connection. Simulate one by making web1's `pre` sleep
longer than its timeout in `web.nix`:

```nix
pre = {
  run = "sleep 60";      # was the haproxy drain
  timeout = "10s";
  # ...
};
```

Commit, then reboot just web1:

```console
$ git commit -qam "web: a slow drain"
$ careen reboot --only web1 --force
web1: pre timed out after 10s
  [w]ait, [f]orce, [s]kip, [a]bort? f
```

- **wait** runs `pre` again for another 10 seconds;
- **force** runs the host's `force` command instead (take it out at once), then reboots;
- **skip** puts it back with `post` and leaves web1 alone;
- **abort** stops, leaving web1 as `pre` left it; careen prints the `post` command to undo it.

Scripts can't answer a prompt. Under `--json` careen emits a `decision` event and exits 3, and you
answer up front instead: `--yes` for the confirmation, `--on-timeout` for the timeout:

```console
$ careen reboot --only web1 --force --yes --json >/dev/null; echo "exit $?"
exit 3
$ careen reboot --only web1 --force --yes --on-timeout force
```

The TUI asks too: run `careen tui`, mark web1 with ++space++, press ++r++, then ++f++ in the dialog
to force the reboot, and ++y++ to start it. When the question appears under the activity log, press
++w++, ++f++, ++s++ or ++a++.

Revert the slow drain when you're done (`git revert HEAD`).

## What carries over

The same shape fits anything that serves traffic or holds data:

| Service | pre | post | gate |
| --- | --- | --- | --- |
| A load-balanced web server | drain it | enable it | the balancer's health check |
| A cluster node | cordon and drain it | uncordon it | the cluster and its storage are healthy |
| A database replica | stop sending it reads | resume reads | replication has caught up |

careen doesn't know which one it's running; see [Rolling reboots](../guides/reboots.md#rolling-reboots)
for everything the hooks can do.
