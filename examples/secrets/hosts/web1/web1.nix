# web1: rendered from templates/host.nix.tmpl.
{ ... }:
{
  fleet = {
    enable = true;
    address = "web1-vm";
    tags = [ "web" ];
  };

  services.nginx = {
    enable = true;
    virtualHosts.default.locations."/".extraConfig = ''
      default_type text/plain;
      return 200 "hello from web1\n";
    '';
    # Serves the greeting secret, once there is one (see the tutorial).
    virtualHosts.default.locations."= /greeting".extraConfig = ''
      default_type text/plain;
      alias /run/secrets/greeting;
    '';
  };
  networking.firewall.allowedTCPPorts = [ 80 ];

  # Uncomment once `careen secrets set web1 greeting` has made the file:
  # sops.defaultSopsFile = ../../secrets/hosts/web1/secrets.yaml;
  # sops.secrets.greeting.owner = "nginx";
}
