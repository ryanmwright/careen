# Provisioning VMs on Proxmox

Declare a VM next to its NixOS configuration, and let careen create it, install its host key and
deploy it: `careen up`. Later, change its size in the flake and apply it, or tear it down and make it
again with nothing to clean up in sops.

!!! info "What you need"
    A Proxmox VE cluster you can reach over SSH as root and through its API, a storage pool for VM
    disks (`local-lvm` here), and an address for the new VM. This tutorial creates and destroys a real
    VM; use a test node or VMID.

## 1. Start from the template

```console
$ nix flake new -t github:ryanmwright/careen#proxmox my-fleet
$ cd my-fleet && git init -q && git add -A
$ nix develop                    # careen, sops, age, ssh
$ export CAREEN_FLAKE=$PWD
```

!!! tip "Already using careen on this machine?"
    Keep this fleet apart from yours: `export CAREEN_REPO=$PWD XDG_STATE_HOME=$PWD/.state` too, so
    `careen add` writes here and OpenTofu keeps this tutorial's state on its own.

The flake describes VMs with a `pve` option on each host, and gives careen three things:

=== "pve output (flake.nix)"

    ```nix
    --8<-- "proxmox/flake.nix:pve"
    ```

=== "hosts/app1.nix"

    ```nix
    --8<-- "proxmox/hosts/app1.nix"
    ```

=== "pve-tofu.nix"

    ```nix
    --8<-- "proxmox/pve-tofu.nix"
    ```

=== "hosts/pve-base.nix"

    ```nix
    --8<-- "proxmox/hosts/pve-base.nix"
    ```

- The **`pve` output** is the inventory careen reads: your cluster, and one entry per VM, collected
  from each host's `pve` settings.
- **`pve-tofu`** renders it to OpenTofu JSON, and **`opentofu`** is OpenTofu with the bpg/proxmox
  provider built in. careen runs them; it never changes VM hardware through the API itself.
- **`pve-base`** is the generic image every VM is cloned from.

Edit `pve` in `flake.nix` for your cluster: `endpoint`, `storage`, and each node's `address`. Edit
`hosts/app1.nix` for the VM: `node`, `vmid`, and its addresses. See
[Provisioning on Proxmox](../guides/provisioning-proxmox.md) for every field.

## 2. Keys

Your SSH key goes into the template, so every VM accepts it; careen also needs root SSH to the Proxmox
nodes.

```console
$ cp ~/.ssh/id_ed25519.pub admin-key.pub && git add admin-key.pub
$ ssh root@10.0.0.11 true        # must work without a prompt
```

A master key for sops, as in [the secrets tutorial](secrets.md#2-your-master-key):

```console
$ age-keygen -o admin.agekey
$ sed -i "s/age1REPLACEME_ADMIN/$(age-keygen -y admin.agekey)/" .sops.yaml
$ export SOPS_AGE_KEY_FILE=$PWD/admin.agekey
```

## 3. An API token for OpenTofu

On any Proxmox node, as root, make a user and token with only the rights careen needs:

```console
# pveum role add CareenTofu -privs "VM.Allocate VM.Clone VM.Audit VM.Config.CDROM VM.Config.CPU VM.Config.Cloudinit VM.Config.Disk VM.Config.HWType VM.Config.Memory VM.Config.Network VM.Config.Options VM.PowerMgmt VM.GuestAgent.Audit VM.GuestAgent.FileRead Datastore.AllocateSpace Datastore.Audit SDN.Use Sys.Audit"
# pveum user add tofu@pve
# pveum aclmod / -user tofu@pve -role CareenTofu
# pveum user token add tofu@pve careen --privsep 0
```

Store the token (`tofu@pve!careen=<uuid>`) encrypted to your master key only:

```console
$ sops secrets/pve/secrets.yaml          # proxmox_api_token: tofu@pve!careen=<uuid>
$ git add -A && git commit -qm "proxmox: cluster and token"
```

careen decrypts it only for commands that touch VMs, and only passes it to OpenTofu.

## 4. Build the template

```console
$ careen template build
```

careen builds `pve-base` as a Proxmox image once, and restores it on each node as that node's template
(VM 9000 here). Rebuild it with `--replace` after a NixOS release upgrade.

## 5. Bring app1 up

Onboard app1 first. careen makes its SSH host key now and records it in `.sops.yaml`, so the VM's
secrets can be encrypted before it exists:

```console
$ careen add app1 --yes
$ git add -A && git commit -qm "app1: onboard"
$ careen vm plan --only app1             # one VM to create
$ careen up --only app1
```

`careen up` clones the template, waits for the VM's guest agent and reads its first host key through
it, installs app1's real key over a connection pinned to that one, then deploys app1's configuration
and reboots into it. `careen status` now lists app1 like any other host.

## 6. Change it

Give app1 more memory in `hosts/app1.nix` (`memoryMB = 4096;`), commit, then:

```console
$ careen vm plan --only app1             # an in-place update
$ careen vm apply --only app1
$ careen reboot --only app1              # needs: hardware
```

careen never lets OpenTofu reboot a running VM. The change waits in Proxmox until careen restarts the
VM from outside, one at a time and per its reboot policy; see
[Reboots](../guides/reboots.md#proxmox-hardware-changes).

## 7. Tear it down

```console
$ careen down --only app1 --yes
$ careen up --only app1                  # the same host again: same key, same secrets
```

To retire a host for good, run `careen down` **before** removing it from the flake; careen only acts
on hosts it can see.

## What to keep safe

- **OpenTofu's state**, in `~/.local/state/careen/pve` on the machine you run careen on. Without it
  careen no longer knows the VMs it made.
- **Your master key**, which can decrypt the API token and every secret.
