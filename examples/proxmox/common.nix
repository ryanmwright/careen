# What every host here shares: the admin user careen logs in as, and the
# QEMU guest agent careen reads a new VM's host key through.
{ lib, ... }:
{
  services.openssh = {
    enable = true;
    settings.PasswordAuthentication = false;
  };
  users.users.admin = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    openssh.authorizedKeys.keyFiles = lib.optional (builtins.pathExists ./admin-key.pub) ./admin-key.pub;
  };
  security.sudo.wheelNeedsPassword = false;
  nix.settings.trusted-users = [ "admin" ];
  services.qemuGuest.enable = true;
  system.stateVersion = "26.05";
}
