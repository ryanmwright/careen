{
  description = "careen example: provisioning NixOS VMs on Proxmox";

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

      names = map (lib.removeSuffix ".nix") (builtins.attrNames (builtins.readDir ./hosts));

      mkHost =
        name:
        lib.nixosSystem {
          modules = [
            ./fleet.nix
            ./pve.nix
            ./common.nix
            ./hosts/${name}.nix
            {
              nixpkgs.hostPlatform = system;
              networking.hostName = name;
              system.configurationRevision = self.rev or self.dirtyRev or null;
            }
          ];
        };

      vms = lib.filterAttrs (_: c: c.config.pve.enable) self.nixosConfigurations;
    in
    {
      nixosConfigurations = lib.genAttrs names mkHost;

      fleet = lib.mapAttrs (_: c: { kind = "nixos"; } // builtins.removeAttrs c.config.fleet [ "enable" ]) (
        lib.filterAttrs (_: c: c.config.fleet.enable) self.nixosConfigurations
      );

      # --8<-- [start:pve]
      # The VM inventory careen reads.
      pve = {
        endpoint = "https://10.0.0.11:8006/";
        storage = "local-lvm";
        # The host every VM is cloned from (careen template build).
        templateHost = "pve-base";
        # Each Proxmox node: where careen SSHes as root, and its template's VM ID.
        nodes = {
          pve1 = { address = "10.0.0.11"; template = 9000; };
        };
        # The API token, in sops (encrypted to the master key only).
        token = { file = "secrets/pve/secrets.yaml"; key = "proxmox_api_token"; };
        hosts = lib.mapAttrs (_: c: builtins.removeAttrs c.config.pve [ "enable" ]) vms;
      };
      # --8<-- [end:pve]

      packages.${system} = {
        # The inventory as OpenTofu JSON, and OpenTofu with the provider built
        # in, so careen never needs the registry.
        pve-tofu = pkgs.callPackage ./pve-tofu.nix { inherit (self) pve; };
        opentofu = pkgs.opentofu.withPlugins (p: [ p.bpg_proxmox ]);
      };

      careen = {
        hostTemplate = "templates/host.nix.tmpl";
        hostDir = "hosts";
        secretsDir = "secrets/hosts";
        sopsConfig = ".sops.yaml";
        masterKeys = { pgp = [ ]; age = [ "admin" ]; };
        sharedRules = [ ];
      };

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
