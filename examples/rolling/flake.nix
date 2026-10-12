{
  description = "careen example: rolling reboots of web servers behind a load balancer";

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

      # Local VMs can't reach each other, but each reaches the host at
      # 10.0.2.2, so lb balances web1 and web2 through their HTTP ports here.
      hosts = {
        lb = { ssh = 2201; http = 8001; };
        web1 = { ssh = 2202; http = 8002; };
        web2 = { ssh = 2203; http = 8003; };
      };

      mkHost =
        name: _:
        lib.nixosSystem {
          specialArgs = { inherit hosts; };
          modules = [
            ./fleet.nix
            ./common.nix
            (if lib.hasPrefix "web" name then ./web.nix else ./hosts/${name}.nix)
            {
              nixpkgs.hostPlatform = system;
              networking.hostName = name;
              system.configurationRevision = self.rev or self.dirtyRev or null;
            }
          ];
        };
    in
    {
      nixosConfigurations = lib.mapAttrs mkHost hosts;

      fleet = lib.mapAttrs (_: c: { kind = "nixos"; } // builtins.removeAttrs c.config.fleet [ "enable" ]) (
        lib.filterAttrs (_: c: c.config.fleet.enable) self.nixosConfigurations
      );

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

      devShells.${system}.default = pkgs.mkShell {
        packages = [
          careen.packages.${system}.careen
          pkgs.openssh
          pkgs.curl
        ];
      };
    };
}
