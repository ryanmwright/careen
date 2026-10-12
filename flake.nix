{
  description = "careen: patch, reboot and inspect a NixOS fleet";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        careen = pkgs.callPackage ./nix/package.nix {
          version = "0.1.0-${self.shortRev or self.dirtyShortRev or "dev"}";
        };
        default = careen;
        # The documentation site; see docs/ and mkdocs.yml.
        docs = pkgs.callPackage ./nix/docs.nix { inherit careen; };
      });

      # nix flake new -t github:ryanmwright/careen#first-fleet my-fleet
      templates = {
        first-fleet = {
          path = ./examples/first-fleet;
          description = "careen hello world: one machine you can run as a local VM";
        };
        rolling = {
          path = ./examples/rolling;
          description = "careen rolling reboots: web servers behind haproxy, one at a time";
        };
        proxmox = {
          path = ./examples/proxmox;
          description = "careen provisioning: NixOS VMs on Proxmox with OpenTofu from stock nixpkgs";
        };
        secrets = {
          path = ./examples/secrets;
          description = "careen onboarding and sops secrets, with hosts you can run as local VMs";
        };
      };

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            openssh
            gopls
            gotools
            golangci-lint
            jq
            sops
            age
            git
            # scripts/docs.sh: preview the documentation site
            (python3.withPackages (ps: [
              ps.mkdocs-material
              ps.pymdown-extensions
            ]))
          ];
        };
      });

      checks = forAllSystems (pkgs: {
        careen = self.packages.${pkgs.stdenv.hostPlatform.system}.careen;
        docs = self.packages.${pkgs.stdenv.hostPlatform.system}.docs;
      });

      # careen managing one VM from another: nix build .#tests.e2e. Not a
      # check, since it needs KVM; CI runs it.
      legacyPackages.x86_64-linux.tests.e2e = import ./nix/e2e.nix {
        pkgs = nixpkgs.legacyPackages.x86_64-linux;
        careen = self.packages.x86_64-linux.careen;
      };

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
