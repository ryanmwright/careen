{
  description = "careen example: onboarding hosts and managing their secrets with sops";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    sops-nix = {
      url = "github:Mic92/sops-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    careen = {
      url = "github:ryanmwright/careen";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    { self, nixpkgs, sops-nix, careen }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;

      # Every directory under hosts/ is a host: careen add writes
      # hosts/<name>/<name>.nix, and it's a machine right away.
      names = builtins.attrNames (lib.filterAttrs (_: t: t == "directory") (builtins.readDir ./hosts));

      mkHost =
        name:
        lib.nixosSystem {
          modules = [
            sops-nix.nixosModules.sops
            ./fleet.nix
            ./common.nix
            ./hosts/${name}/${name}.nix
            {
              nixpkgs.hostPlatform = system;
              networking.hostName = name;
              system.configurationRevision = self.rev or self.dirtyRev or null;
            }
          ];
        };
    in
    {
      nixosConfigurations = lib.genAttrs names mkHost;

      fleet = lib.mapAttrs (_: c: { kind = "nixos"; } // builtins.removeAttrs c.config.fleet [ "enable" ]) (
        lib.filterAttrs (_: c: c.config.fleet.enable) self.nixosConfigurations
      );

      # --8<-- [start:careen-settings]
      # Where careen add, keys and secrets find things in this repository.
      careen = {
        hostTemplate = "templates/host.nix.tmpl";
        hostDir = "hosts";
        secretsDir = "secrets/hosts";
        sopsConfig = ".sops.yaml";
        # The key that can always decrypt and re-key: yours (made in the
        # tutorial with age-keygen).
        masterKeys = {
          pgp = [ ];
          age = [ "admin" ];
        };
        sharedRules = [ ];
        # Secret groups (files some hosts share) go in secrets/<group>/.
        groupsDir = "secrets";
      };
      # --8<-- [end:careen-settings]

      # nix run .#vm-<host>: the hosts' VMs forward SSH to 2201, 2202, ...
      # and HTTP to 8001, 8002, ..., in alphabetical order.
      apps.${system} = builtins.listToAttrs (
        lib.imap1 (i: name: {
          name = "vm-${name}";
          value = {
            type = "app";
            program = lib.getExe (
              pkgs.callPackage ./run-vm.nix {
                inherit name;
                ssh = 2200 + i;
                http = 8000 + i;
                image = self.nixosConfigurations.${name}.config.system.build.image;
              }
            );
          };
        }) names
      );

      devShells.${system}.default = pkgs.mkShell {
        packages = [
          careen.packages.${system}.careen
          pkgs.openssh
          pkgs.sops
          pkgs.age
        ];
      };
    };
}
