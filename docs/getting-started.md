# Getting started

careen needs three things: the binary on the machine you run it from, a `fleet` output in your
flake that lists your hosts, and SSH access to those hosts.

## Install

careen is a Nix flake. Run it without installing:

```console
$ nix run github:ryanmwright/careen -- --help
```

Or add it to the NixOS configuration of the machine you manage the fleet from:

```nix title="flake.nix"
{
  inputs.careen.url = "github:ryanmwright/careen";

  outputs = { nixpkgs, careen, ... }: {
    nixosConfigurations.mgr = nixpkgs.lib.nixosSystem {
      modules = [
        ({ pkgs, ... }: {
          environment.systemPackages = [
            careen.packages.${pkgs.stdenv.hostPlatform.system}.careen
            pkgs.sops    # careen keys and secrets
            pkgs.vulnix  # careen vulns
          ];
          environment.variables.CAREEN_FLAKE = "git+ssh://git@example.com/me/nix.git";
        })
      ];
    };
  };
}
```

`CAREEN_FLAKE` saves passing `--flake` to every command. See
[Environment and files](reference/environment.md) for the rest.

## Describe your fleet

careen reads the hosts from a `fleet` output of your flake: an attribute set of hosts with how
to reach and handle each one.

```nix
fleet.web1 = {
  kind = "nixos";
  address = "10.0.0.20";      # SSH destination
  user = "admin";
  tags = [ "web" ];
  rebootPolicy = "auto";      # auto | manual | never
  deploy = true;              # included in --all
  buildOn = "local";          # local | remote | target
  operation = "switch";       # switch | boot
};
```

Writing this by hand works, but it's easier to generate it from an option on each host, so a
host's fleet entry lives next to its configuration. [The inventory](concepts/inventory.md) shows
how, and [Inventory fields](reference/inventory-fields.md) lists every field.

Each host should also record which revision of the flake it runs, so careen can tell when it has
drifted:

```nix
system.configurationRevision = self.rev or self.dirtyRev or null;
```

## Prepare the hosts

- **SSH without prompts.** careen runs SSH in batch mode, so your key must work without a
  password or a host-key question.
- **Passwordless sudo** for careen's SSH user, for switching, rebooting and garbage collection.
- **A trusted Nix user.** careen copies unsigned store paths, so its SSH user must be in
  `nix.settings.trusted-users` on each host.

## First commands

```console
$ careen doctor        # checks tools, the flake, SSH and keys
$ careen hosts         # the inventory as careen sees it
$ careen status        # live state of every host
```

!!! tip "Rather learn by doing?"
    [Your first fleet](tutorials/first-fleet.md) walks through all of this with a local VM, in about
    fifteen minutes and without touching a real machine.

From here:

- [Deploy and roll back](guides/deploy-and-rollback.md) to patch hosts.
- [Onboarding, keys and secrets](guides/onboarding.md) to add hosts and manage sops.
- [The TUI](guides/tui.md) to do all of it interactively.
