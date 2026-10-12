# web1 and web2: the same web server, rolled one at a time. Their reboot
# hooks run on the load balancer (tag:lb): take the server out of haproxy
# before the reboot, put it back after, and wait until haproxy's health
# check says it's up again before moving on.
{ config, ... }:
let
  name = config.networking.hostName;
  # Send one command to haproxy's admin socket on lb.
  haproxy = cmd: "echo '${cmd}' | sudo socat stdio unix:/run/haproxy/haproxy.sock";
in
{
  fleet = {
    enable = true;
    address = "${name}-vm";
    tags = [ "web" ];
    rebootOrder = 10;
    reboot = {
      pre = {
        # Stop new connections, give current ones 5s to finish, then take it
        # out. timeout(1) ends it on lb if that ever hangs; its exit 124
        # tells careen it timed out.
        run = "timeout 20s sh -c \"${haproxy "set server web/{{.Name}} state drain"} && sleep 5 && ${haproxy "set server web/{{.Name}} state maint"}\"";
        on = "tag:lb";
        timeout = "30s";
        # When pre times out and you answer force: take it out at once.
        force = haproxy "set server web/{{.Name}} state maint";
      };
      post = {
        run = haproxy "set server web/{{.Name}} state ready";
        on = "tag:lb";
      };
      gate = {
        # haproxy's own health check has to see it UP.
        run = "${haproxy "show stat"} | grep '^web,{{.Name}},' | cut -d, -f18 | grep -q UP";
        on = "tag:lb";
        timeout = "2m";
        interval = "2s";
      };
      settle = "5s";
    };
  };

  services.nginx = {
    enable = true;
    virtualHosts.default.locations."/".extraConfig = ''
      default_type text/plain;
      return 200 "hello from ${name}\n";
    '';
  };
  networking.firewall.allowedTCPPorts = [ 80 ];
}
