{
  description = "careen example: a first fleet, one machine you can run as a local VM";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    careen = {
      url = "github:ryanmwright/careen";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    { self, nixpkgs, careen }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;

      # Each host: its module, and the local ports its VM forwards to SSH
      # and HTTP (only for trying things out; a real machine needs none).
      hosts = {
        web1 = { ssh = 2201; http = 8001; };
      };

      mkHost =
        name: _:
        lib.nixosSystem {
          modules = [
            ./fleet.nix
            ./common.nix
            ./hosts/${name}.nix
            {
              nixpkgs.hostPlatform = system;
              networking.hostName = name;
              # Lets careen tell which commit each host runs.
              system.configurationRevision = self.rev or self.dirtyRev or null;
            }
          ];
        };
    in
    {
      nixosConfigurations = lib.mapAttrs mkHost hosts;

      # What careen reads: one entry per host that enables `fleet`.
      fleet = lib.mapAttrs (_: c: { kind = "nixos"; } // builtins.removeAttrs c.config.fleet [ "enable" ]) (
        lib.filterAttrs (_: c: c.config.fleet.enable) self.nixosConfigurations
      );

      # nix run .#vm-web1 boots web1 as a local VM.
      apps.${system} = lib.mapAttrs' (
        name: ports:
        lib.nameValuePair "vm-${name}" {
          type = "app";
          program = lib.getExe (
            pkgs.callPackage ./run-vm.nix {
              inherit name;
              inherit (ports) ssh http;
              image = self.nixosConfigurations.${name}.config.system.build.image;
            }
          );
        }
      ) hosts;

      # nix develop: careen and ssh.
      devShells.${system}.default = pkgs.mkShell {
        packages = [
          careen.packages.${system}.careen
          pkgs.openssh
        ];
      };
    };
}
