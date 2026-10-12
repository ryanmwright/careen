# What every host here shares: a disk image to boot as a VM, an admin user
# careen logs in as, and the settings careen needs on a host.
{ lib, modulesPath, ... }:
{
  imports = [
    (modulesPath + "/profiles/qemu-guest.nix")
    # A bootable disk image with GRUB, so a reboot comes up on whatever
    # careen deployed (a `nixos-rebuild build-vm` VM wouldn't).
    (modulesPath + "/virtualisation/disk-image.nix")
  ];
  image.efiSupport = false;
  virtualisation.diskSize = 8192;
  boot.kernelParams = [ "console=ttyS0" ];

  services.openssh = {
    enable = true;
    settings.PasswordAuthentication = false;
  };

  users.users.admin = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    # Made in the tutorial with ssh-keygen; git add it so the flake sees it.
    openssh.authorizedKeys.keyFiles = lib.optional (builtins.pathExists ./demo-key.pub) ./demo-key.pub;
  };

  # careen switches, reboots and collects garbage with sudo, and copies
  # systems it built, which the host must trust.
  security.sudo.wheelNeedsPassword = false;
  nix.settings.trusted-users = [ "admin" ];

  system.stateVersion = "26.05";
}
