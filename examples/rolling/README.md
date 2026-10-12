# rolling

Rolling reboots with careen's reboot hooks: `web1` and `web2` behind haproxy on `lb`. Before each
web server reboots, a hook drains it on the load balancer; after, another puts it back, and a gate
waits for haproxy's health check, so the site stays up while careen reboots them one at a time.

```console
$ nix flake new -t github:ryanmwright/careen#rolling my-site
```

Walkthrough: <https://ryanmwright.github.io/careen/tutorials/rolling-reboots/>

| File | What |
| --- | --- |
| `web.nix` | the web servers, with their `reboot` hooks and `rebootOrder` |
| `hosts/lb.nix` | haproxy, with the admin socket the hooks use |
| `flake.nix` | the three hosts and their VMs (`nix run .#vm-lb`, `vm-web1`, `vm-web2`) |

Needs Linux on x86_64 with KVM; the three VMs use about 3 GB of memory.
