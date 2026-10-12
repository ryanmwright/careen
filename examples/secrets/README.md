# secrets

Onboarding hosts and their [sops](https://github.com/getsops/sops) secrets with careen: a master
key, hosts' keys from their SSH host keys, `careen add` from a template, and a secret served by a
host. Hosts are discovered from `hosts/`, so a host `careen add` writes is a machine (and a local VM)
right away.

```console
$ nix flake new -t github:ryanmwright/careen#secrets my-fleet
```

Walkthrough: <https://ryanmwright.github.io/careen/tutorials/secrets/>

| File | What |
| --- | --- |
| `flake.nix` | hosts from `hosts/`, the `fleet` and `careen` outputs, VMs, a dev shell with sops and age |
| `templates/host.nix.tmpl` | what `careen add` writes for a new host |
| `.sops.yaml` | who can decrypt what (the master key is a placeholder until you make one) |
| `hosts/web1/web1.nix` | a host, rendered from the template |

Needs Linux on x86_64 with KVM.
