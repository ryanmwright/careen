# Each host's careen settings, next to the rest of its configuration. The
# flake turns them into the `fleet` output careen reads. careen doesn't
# provide this module; copy it and set the defaults that suit you.
{ lib, ... }:
let
  inherit (lib) mkOption types;
in
{
  options.fleet = {
    enable = lib.mkEnableOption "managing this host with careen";
    address = mkOption {
      type = types.str;
      description = "SSH destination: an address, a DNS name or an ~/.ssh/config alias";
    };
    user = mkOption {
      type = types.str;
      default = "admin";
    };
    tags = mkOption {
      type = types.listOf types.str;
      default = [ ];
    };
    rebootPolicy = mkOption {
      type = types.enum [ "auto" "manual" "never" ];
      default = "auto";
    };
    deploy = mkOption {
      type = types.bool;
      default = true;
    };
    buildOn = mkOption {
      type = types.enum [ "local" "remote" "target" ];
      default = "local";
    };
    operation = mkOption {
      type = types.enum [ "switch" "boot" ];
      default = "switch";
    };
    rebootOrder = mkOption {
      type = types.int;
      default = 0;
      description = "careen reboots lower orders first";
    };
    reboot = mkOption {
      type = types.attrs;
      default = { };
      description = "careen's reboot hooks: pre, post, gate, settle";
    };
  };
}
