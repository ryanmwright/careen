{ ... }:
{
  fleet = {
    enable = true;
    # An ~/.ssh/config alias for the local VM (see the tutorial). For a
    # real machine, its address: "10.0.0.21" or "web1.example".
    address = "web1-vm";
    tags = [ "web" ];
  };

  users.motd = "web1, managed by careen\n";

  services.nginx = {
    enable = true;
    virtualHosts.default.locations."/".extraConfig = ''
      default_type text/plain;
      return 200 "hello from web1\n";
    '';
  };
  networking.firewall.allowedTCPPorts = [ 80 ];
}
