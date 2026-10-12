# The generic image every VM is cloned from: no identity of its own. careen
# template build builds it with NixOS's Proxmox image module (cloud-init on,
# so a clone gets its first IP) and installs it as each node's template.
{ lib, ... }:
{
  # Not managed by careen: it's a template, not a running host.
  fleet.enable = false;

  # The image module sets these when it builds the image; the defaults keep
  # the host a valid system on its own.
  fileSystems."/" = lib.mkDefault {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
  };
  boot.loader.grub.device = lib.mkDefault "/dev/vda";
}
