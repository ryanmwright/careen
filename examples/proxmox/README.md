# proxmox

Provisioning NixOS VMs on Proxmox with careen: declare a VM next to its configuration, then
`careen up` clones it from a template, installs its pre-generated host key and deploys it. OpenTofu
does the VM work, built from stock nixpkgs.

```console
$ nix flake new -t github:ryanmwright/careen#proxmox my-fleet
```

Walkthrough: <https://ryanmwright.github.io/careen/tutorials/proxmox/>

| File | What |
| --- | --- |
| `flake.nix` | the `pve` inventory, `pve-tofu` and `opentofu` packages, the `fleet` and `careen` outputs |
| `pve.nix` | the `pve` option each VM host sets (node, VMID, size, first IP) |
| `pve-tofu.nix` | the inventory rendered to OpenTofu JSON for the bpg/proxmox provider |
| `hosts/pve-base.nix` | the generic template image every VM is cloned from |
| `hosts/app1.nix` | a VM careen creates |
| `check.sh` | validates the rendered OpenTofu config (used by careen's CI) |

Needs a Proxmox VE cluster; the addresses and node names are placeholders.
