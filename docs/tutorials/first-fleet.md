# Your first fleet

In about fifteen minutes: one NixOS machine declared in a flake, running as a VM on your computer,
that careen inspects, patches, rolls back and reboots. At the end you'll point the same flake at a
real machine.

!!! info "What you need"
    Linux on x86_64 with KVM (`/dev/kvm`), and Nix with flakes enabled. The VM uses 1 GB of memory and
    an 8 GB disk image.

## 1. Start from the template

```console
$ nix flake new -t github:ryanmwright/careen#first-fleet my-fleet
$ cd my-fleet
$ git init -q && git add -A
```

Flakes only see files git knows about, so `git add` anything new as you go.

What you got:

=== "flake.nix"

    ```nix
    --8<-- "first-fleet/flake.nix"
    ```

=== "hosts/web1.nix"

    ```nix
    --8<-- "first-fleet/hosts/web1.nix"
    ```

=== "common.nix"

    ```nix
    --8<-- "first-fleet/common.nix"
    ```

=== "fleet.nix"

    ```nix
    --8<-- "first-fleet/fleet.nix"
    ```

=== "run-vm.nix"

    ```nix
    --8<-- "first-fleet/run-vm.nix"
    ```

- **`hosts/web1.nix`** is the machine: nginx saying hello, and its `fleet` settings, which tell careen
  how to reach it.
- **`fleet.nix`** declares those settings as a NixOS option, and **`flake.nix`** collects them into the
  `fleet` output careen reads. See [The inventory](../concepts/inventory.md).
- **`common.nix`** has what careen needs on every host: a user it logs in as with passwordless sudo,
  and that user trusted to receive store paths. It also makes the host a bootable disk image.

## 2. A key and an SSH alias

careen uses plain SSH, without prompts. Make a key for the demo and let the VM accept it:

```console
$ ssh-keygen -q -t ed25519 -N '' -C careen-demo -f demo-key
$ git add demo-key.pub
$ git commit -qm "my fleet"
```

careen compares each host's system with the flake's commit, so commit before the VM's image is built
in the next step.

The VM forwards its SSH to port 2201 on your machine. A fleet `address` has no port, so give it an alias
in `~/.ssh/config` (use the full path to your `my-fleet`):

```ssh-config
Host web1-vm
  HostName 127.0.0.1
  Port 2201
  User admin
  IdentityFile ~/my-fleet/demo-key
  # A throwaway local VM: don't keep its host key.
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
```

## 3. Boot the machine

In a second terminal:

```console
$ nix run .#vm-web1
web1: ssh on 127.0.0.1:2201, http on 127.0.0.1:8001; Ctrl-a x stops it
```

The first run builds web1's disk image, which takes a few minutes. The VM keeps its disk in
`.vm/web1.qcow2`, so whatever careen deploys survives restarts. Once it's up:

```console
$ curl -s 127.0.0.1:8001
hello from web1
```

## 4. Meet careen

Back in the first terminal, get careen and tell it where the fleet is:

```console
$ nix develop
$ export CAREEN_FLAKE=$PWD
$ careen doctor
$ careen hosts
HOST  ADDRESS  USER   TAGS  REBOOT  DEPLOY  OPERATION  BUILD
web1  web1-vm  admin  web   auto    yes     switch     local
$ careen status
```

`status` shows web1 running the flake's revision (`DRIFT current`), no reboot needed and no failed
units. See [Status and drift](../concepts/drift-and-revisions.md) for every column.

## 5. Deploy a change

Change web1's greeting in `hosts/web1.nix`, say to `hello again from web1`, and commit it. careen
deploys commits, so every host can be traced back to one:

```console
$ git commit -qam "web1: new greeting"
$ careen status                  # web1 now shows DRIFT differs
$ careen deploy --all --plan     # what would happen
$ careen deploy --all
$ curl -s 127.0.0.1:8001
hello again from web1
```

careen built web1's system on your machine, copied it over, switched to it and checked the host
was still healthy. Had the switch broken the host, careen would have rolled it back on its own; see
[Deploy and roll back](../guides/deploy-and-rollback.md).

## 6. Roll back

```console
$ careen rollback web1
$ curl -s 127.0.0.1:8001
hello from web1
```

`careen status` now shows web1 one commit behind again. Deploy once more to bring it forward.

## 7. Reboot

A new kernel needs a reboot. Give web1 the latest one in `hosts/web1.nix`:

```nix
boot.kernelPackages = pkgs.linuxPackages_latest;
```

(and change its first line to `{ pkgs, ... }:`), then:

```console
$ git commit -qam "web1: latest kernel"
$ careen deploy --all
$ careen status                  # REBOOT shows kernel
$ careen reboot --plan
$ careen reboot
```

careen reboots web1, waits for it to come back on the new kernel and checks its health. With more
hosts it goes one at a time and stops at the first that doesn't come back; see
[Reboots](../guides/reboots.md).

## 8. The TUI

```console
$ careen tui
```

The same status, live, with keys for everything above: ++d++ deploy, ++r++ reboot, ++b++ roll
back, ++l++ logs, ++s++ a shell on the host, ++question++ for the rest. See [The TUI](../guides/tui.md).

## 9. Point it at a real machine

Nothing about careen changes for real hardware; only how the machine is described:

1. Install NixOS on it and give it the settings in `common.nix` (the `admin` user with your key,
   passwordless sudo, `nix.settings.trusted-users`), but not the disk-image lines, which are only
   for the VM. Use the machine's own hardware configuration instead.
2. Set `fleet.address` in its host file to its address or DNS name.
3. `careen status`, then `careen deploy`.

To add more machines, add a file under `hosts/` and an entry in `hosts` in `flake.nix`.

## Clean up

Stop the VM with ++ctrl+a++ then ++x++, and delete `.vm/` to start from a fresh image next time.
