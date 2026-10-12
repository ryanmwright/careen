# The inventory

careen knows your hosts from one place: the `fleet` output of your flake. There is no careen
config file and no database. Change the flake and careen sees the change.

## The `fleet` output

`fleet` is an attribute set: one entry per host, named after the host.

```nix
fleet = {
  web1 = { kind = "nixos"; address = "10.0.0.20"; user = "admin"; tags = [ "web" ]; /* ... */ };
  db1  = { kind = "nixos"; address = "10.0.0.21"; user = "admin"; tags = [ "db" ];  /* ... */ };
};
```

Every field is described in [Inventory fields](../reference/inventory-fields.md).

### Generating it from your hosts

Keeping the entry next to each host's configuration is easier than maintaining a separate list.
Declare a `fleet` option in a NixOS module, set it on each host, and build `fleet` from every
configuration that enables it:

```nix title="modules/fleet.nix"
{ self, lib, ... }:
{
  # in each host: fleet = { enable = true; address = "10.0.0.20"; tags = [ "web" ]; };
  flake.nixosModules.fleet = { lib, ... }: {
    options.fleet = {
      enable = lib.mkEnableOption "managing this host with careen";
      address = lib.mkOption { type = lib.types.str; };
      user = lib.mkOption { type = lib.types.str; default = "admin"; };
      tags = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ ]; };
      rebootPolicy = lib.mkOption { type = lib.types.enum [ "auto" "manual" "never" ]; default = "manual"; };
      deploy = lib.mkOption { type = lib.types.bool; default = true; };
      buildOn = lib.mkOption { type = lib.types.enum [ "local" "remote" "target" ]; default = "local"; };
      builder = lib.mkOption { type = lib.types.str; default = ""; };
      operation = lib.mkOption { type = lib.types.enum [ "switch" "boot" ]; default = "switch"; };
      provisioner = lib.mkOption { type = lib.types.enum [ "existing" "proxmox" ]; default = "existing"; };
    };
  };

  flake.fleet = lib.mapAttrs
    (_: c: { kind = "nixos"; } // builtins.removeAttrs c.config.fleet [ "enable" ])
    (lib.filterAttrs (_: c: c.config.fleet.enable) self.nixosConfigurations);
}
```

The defaults in this example are a sensible starting point; they come from your module, not
from careen.

## Selecting hosts

Every command takes the same selectors:

| Flag | Selects |
| --- | --- |
| `--only a,b` | only these hosts, in this order |
| `--except x` | every host but these |
| `--tag t,u` | hosts with any of these tags |

Naming a host that isn't in the inventory is a usage error (exit 2). Actions that change hosts
also take `--all`, which means every host with `deploy = true` (for `deploy`), or every host
careen provisions (for `up`, `down` and `vm`).

`--only` also matters for safety: hosts with `rebootPolicy = "manual"` are only rebooted when
named with it. See [Reboot policy](reboot-policy.md).

## Caching

Evaluating `fleet` means evaluating every host's NixOS configuration, which takes seconds. careen
caches the result per locked flake URL in `~/.cache/careen/inventory/`, so a repeated command
against the same commit doesn't evaluate again. A working tree with uncommitted changes is never
cached.

Actions that change hosts, and `status`, ask Nix to re-fetch the flake first (`--refresh`), so
they always use the head of the branch rather than a copy Nix fetched up to an hour ago.
