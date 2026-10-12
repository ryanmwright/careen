# End-to-end: careen on one VM (mgr) manages another (web1) the way the
# first-fleet tutorial does: status, deploy, a second deploy, rollback, and
# a reboot with reboot hooks. mgr carries every build dependency, so careen
# builds web1's systems offline, as nixpkgs' nixos-rebuild target-host test
# does.
{
  pkgs,
  careen,
}:
let
  inherit (import "${pkgs.path}/nixos/tests/ssh-keys.nix" pkgs) snakeOilPrivateKey snakeOilPublicKey;

  # What web1 runs: its own test configuration plus what careen needs. The
  # deployed configurations keep the test VM's modules, so the test driver
  # can still talk to web1 after a switch.
  targetConfig = {
    documentation.enable = false;
    services.openssh.enable = true;
    users.users.admin = {
      isNormalUser = true;
      extraGroups = [ "wheel" ];
      openssh.authorizedKeys.keys = [ snakeOilPublicKey ];
    };
    security.sudo.wheelNeedsPassword = false;
    nix.settings.trusted-users = [ "admin" ];
    systemd.tmpfiles.rules = [ "d /var/lib/careen-e2e 0777 root root -" ];
  };
in
pkgs.testers.runNixOSTest {
  name = "careen-e2e";

  node.pkgsReadOnly = false;
  defaults.nix.enable = true;

  nodes = {
    mgr =
      {
        lib,
        pkgs,
        nodes,
        ...
      }:
      let
        # web1 as the fleet flake deploys it (web1.nix below, give or take
        # the marker file and the hardware configuration). Its build closure on mgr
        # holds every source and tool those small differences need.
        web1Deployed =
          (import "${pkgs.path}/nixos/lib/eval-config.nix" {
            system = null;
            modules = [
              (
                { modulesPath, ... }:
                {
                  imports = [
                    (modulesPath + "/virtualisation/qemu-vm.nix")
                    (modulesPath + "/virtualisation/guest-networking-options.nix")
                    (modulesPath + "/testing/test-instrumentation.nix")
                  ];
                }
              )
              targetConfig
              nodes.web1.system.build.networkConfig
              {
                nixpkgs.hostPlatform = pkgs.stdenv.hostPlatform.system;
                boot.loader.grub = {
                  enable = true;
                  device = "/dev/vda";
                  forceInstall = true;
                };
                networking.hostName = "web1";
                system.switch.enable = true;
                environment.etc.careen-e2e.text = "marker";
              }
            ];
          }).config.system.build.toplevel;
      in
      {
        environment.systemPackages = [
          careen
          pkgs.git
          pkgs.jq
        ];
        nix.settings = {
          substituters = lib.mkForce [ ];
          hashed-mirrors = null;
          connect-timeout = 1;
          experimental-features = [
            "nix-command"
            "flakes"
          ];
        };
        # Build web1's systems offline.
        system.includeBuildDependencies = true;
        # The nixpkgs the fleet flake evaluates (a path input), and web1's
        # system, so its build dependencies are on mgr too.
        system.extraDependencies = [
          pkgs.path
          web1Deployed
        ];
        virtualisation = {
          cores = 2;
          memorySize = 3072;
          diskSize = 20480;
        };
      };

    web1 =
      { lib, ... }:
      {
        config = lib.mkMerge [
          targetConfig
          {
            system.build = { inherit targetConfig; };
            system.switch.enable = true;
            networking.hostName = "web1";
          }
        ];
      };
  };

  testScript =
    { nodes, ... }:
    let
      sshConfig = pkgs.writeText "ssh-config" ''
        Host web1
          User admin
          IdentityFile /root/.ssh/id_ecdsa
          StrictHostKeyChecking no
          UserKnownHostsFile /dev/null
      '';
      targetConfigJSON = pkgs.writeText "target-configuration.json" (builtins.toJSON targetConfig);
      targetNetworkJSON = pkgs.writeText "target-network.json" (
        builtins.toJSON nodes.web1.system.build.networkConfig
      );

      # The fleet: one host, with reboot hooks that leave marks to check.
      flake = pkgs.writeText "flake.nix" ''
        {
          inputs.nixpkgs.url = "path:${pkgs.path}";
          outputs = { self, nixpkgs }: {
            nixosConfigurations.web1 = nixpkgs.lib.nixosSystem {
              modules = [
                ./web1.nix
                {
                  nixpkgs.hostPlatform = "${pkgs.stdenv.hostPlatform.system}";
                  system.configurationRevision = self.rev or self.dirtyRev or null;
                }
              ];
            };
            fleet.web1 = {
              kind = "nixos";
              address = "web1";
              user = "admin";
              tags = [ "web" ];
              rebootPolicy = "auto";
              deploy = true;
              buildOn = "local";
              operation = "switch";
              reboot = {
                pre = { run = "touch /var/lib/careen-e2e/pre-{{.Name}}"; };
                post = { run = "touch /var/lib/careen-e2e/post-{{.Name}}"; };
                gate = { run = "systemctl is-active sshd"; on = "self"; timeout = "2m"; interval = "2s"; };
                settle = "1s";
              };
            };
          };
        }
      '';
      web1Module = pkgs.writeText "web1.nix" ''
        { lib, modulesPath, ... }:
        {
          imports = [
            (modulesPath + "/virtualisation/qemu-vm.nix")
            (modulesPath + "/virtualisation/guest-networking-options.nix")
            (modulesPath + "/testing/test-instrumentation.nix")
            (lib.modules.importJSON ./target-configuration.json)
            (lib.modules.importJSON ./target-network.json)
            ./hardware-configuration.nix
          ];
          boot.loader.grub = { enable = true; device = "/dev/vda"; forceInstall = true; };
          networking.hostName = "web1";
          system.switch.enable = true;
          environment.etc.careen-e2e.text = builtins.readFile ./motd.txt;
        }
      '';
    in
    ''
      import json

      def careen(args):
          return mgr.succeed(f"cd /root/fleet && careen --flake /root/fleet {args}")

      def events(out):
          return [json.loads(l) for l in out.splitlines() if l.startswith("{")]

      mgr.start()
      # The driver starts QEMU with -no-reboot unless told otherwise, and
      # careen reboots web1.
      web1.start(allow_reboot=True)
      web1.wait_for_open_port(22)
      mgr.wait_until_succeeds("ping -c1 web1")

      mgr.succeed("install -Dm 600 ${snakeOilPrivateKey} /root/.ssh/id_ecdsa")
      mgr.succeed("install ${sshConfig} /root/.ssh/config")

      web1.succeed("nixos-generate-config")
      mgr.succeed("mkdir -p /root/fleet")
      mgr.succeed("scp web1:/etc/nixos/hardware-configuration.nix /root/fleet/hardware-configuration.nix")
      mgr.copy_from_host("${flake}", "/root/fleet/flake.nix")
      mgr.copy_from_host("${web1Module}", "/root/fleet/web1.nix")
      mgr.copy_from_host("${targetConfigJSON}", "/root/fleet/target-configuration.json")
      mgr.copy_from_host("${targetNetworkJSON}", "/root/fleet/target-network.json")
      mgr.succeed("echo 'motd one' > /root/fleet/motd.txt")
      mgr.succeed("cd /root/fleet && git init -q && git add -A && nix flake lock && git add -A && git -c user.email=e2e@example -c user.name=e2e commit -qm one")

      with subtest("careen reads the fleet and reaches web1"):
          hosts = json.loads(careen("hosts --json"))
          assert [h["name"] for h in hosts["hosts"]] == ["web1"], hosts
          status = json.loads(careen("status --json"))
          assert status["hosts"][0].get("error") is None, status

      with subtest("deploy"):
          careen("deploy --all --yes")
          web1.succeed("grep -q 'motd one' /etc/careen-e2e")
          status = json.loads(careen("status --json"))
          assert status["hosts"][0]["drift"] == "current", status

      with subtest("a second deploy, then roll it back"):
          mgr.succeed("cd /root/fleet && echo 'motd two' > motd.txt && git -c user.email=e2e@example -c user.name=e2e commit -qam two")
          careen("deploy --all --yes")
          web1.succeed("grep -q 'motd two' /etc/careen-e2e")
          careen("rollback web1 --yes")
          web1.succeed("grep -q 'motd one' /etc/careen-e2e")

      with subtest("reboot with hooks"):
          plan = json.loads(careen("reboot --force --plan --json"))
          assert plan["hosts"][0]["hooks"], plan
          before = web1.succeed("cat /proc/sys/kernel/random/boot_id").strip()
          out = careen("reboot --force --yes --json")
          done = [e for e in events(out) if e.get("event") == "done"][0]
          host = done["data"]["hosts"][0]
          assert host["rebooted"], host
          assert [r["hook"] for r in host["hookRuns"]] == ["pre", "post", "gate"], host
          # careen rebooted web1 behind the test driver's back: reconnect.
          web1.connected = False
          web1.wait_for_unit("multi-user.target")
          after = web1.succeed("cat /proc/sys/kernel/random/boot_id").strip()
          assert before != after, "web1 didn't reboot"
          web1.succeed("test -e /var/lib/careen-e2e/pre-web1")
          web1.succeed("test -e /var/lib/careen-e2e/post-web1")
    '';
}
