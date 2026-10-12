# Each host's VM, next to its configuration: where it runs and how big it is.
# The flake collects these into the `pve` output careen reads, and renders
# them to OpenTofu JSON (pve-tofu.nix). careen doesn't provide this module.
{ lib, ... }:
let
  inherit (lib) mkOption types;
in
{
  options.pve = {
    enable = lib.mkEnableOption "a Proxmox VM for this host, created by careen up";
    node = mkOption {
      type = types.str;
      description = "The Proxmox node it runs on";
    };
    vmid = mkOption { type = types.int; };
    cores = mkOption {
      type = types.int;
      default = 2;
    };
    memoryMB = mkOption {
      type = types.int;
      default = 2048;
    };
    diskGB = mkOption {
      type = types.int;
      default = 20;
    };
    bridge = mkOption {
      type = types.str;
      default = "vmbr0";
    };
    # cloud-init's first-boot address; the host's own config takes over
    # once careen deploys it.
    ipv4 = mkOption {
      type = types.str;
      example = "10.0.0.31/24";
    };
    gateway = mkOption { type = types.str; };
  };
}
