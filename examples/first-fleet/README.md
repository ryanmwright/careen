# first-fleet

careen's hello world: one NixOS machine, `web1`, declared in a flake and run as a local VM, so you
can try `careen status`, `deploy`, `rollback`, `reboot` and the TUI without real hardware.

```console
$ nix flake new -t github:ryanmwright/careen#first-fleet my-fleet
```

Walkthrough: <https://ryanmwright.github.io/careen/tutorials/first-fleet/>

| File | What |
| --- | --- |
| `flake.nix` | the hosts, the `fleet` output careen reads, `nix run .#vm-web1` |
| `hosts/web1.nix` | the machine, and its `fleet` settings |
| `fleet.nix` | the `fleet` option each host sets (copy and adapt it) |
| `common.nix` | what careen needs on every host, plus a bootable disk image for the VM |
| `run-vm.nix` | boots a host's image with QEMU, SSH and HTTP forwarded to local ports |

Needs Linux on x86_64 with KVM.
