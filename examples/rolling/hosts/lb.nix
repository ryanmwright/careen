# The load balancer: haproxy in front of web1 and web2, with an admin socket
# the web servers' reboot hooks use to take them out and put them back.
{ hosts, pkgs, ... }:
{
  fleet = {
    enable = true;
    address = "lb-vm";
    tags = [ "lb" ];
    # Rebooting it takes the site down; only when you name it.
    rebootPolicy = "manual";
  };

  services.haproxy = {
    enable = true;
    config = ''
      global
        stats socket /run/haproxy/haproxy.sock mode 600 level admin

      defaults
        mode http
        timeout connect 2s
        timeout client 30s
        timeout server 30s

      frontend site
        bind :80
        default_backend web

      backend web
        option httpchk GET /
        server web1 10.0.2.2:${toString hosts.web1.http} check inter 1s
        server web2 10.0.2.2:${toString hosts.web2.http} check inter 1s
    '';
  };
  networking.firewall.allowedTCPPorts = [ 80 ];

  # The hooks talk to haproxy's admin socket with socat.
  environment.systemPackages = [ pkgs.socat ];
}
