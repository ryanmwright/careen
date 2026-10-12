# A VM careen creates: its pve settings say where and how big; the rest is a
# normal NixOS host.
{ modulesPath, ... }:
{
  imports = [ (modulesPath + "/profiles/qemu-guest.nix") ];

  fleet = {
    enable = true;
    address = "10.0.0.31";
    provisioner = "proxmox";
    tags = [ "app" ];
  };

  pve = {
    enable = true;
    node = "pve1";
    vmid = 131;
    cores = 2;
    memoryMB = 2048;
    ipv4 = "10.0.0.31/24";
    gateway = "10.0.0.1";
  };

  # The clone's disk, as the template made it.
  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
    autoResize = true;
  };
  boot.growPartition = true;
  boot.loader.grub.device = "/dev/vda";

  networking = {
    useDHCP = false;
    interfaces.ens18.ipv4.addresses = [ { address = "10.0.0.31"; prefixLength = 24; } ];
    defaultGateway = "10.0.0.1";
    nameservers = [ "10.0.0.1" ];
  };
}
