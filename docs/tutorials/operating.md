# Running a fleet day to day

The other tutorials each show one thing. This one puts them together: what running a handful of
NixOS machines with careen looks like week to week, from the machine you manage them from.

## The management machine

Pick one machine to run careen from (a small VM is plenty): it holds your SSH keys, a checkout of
the flake, OpenTofu's state if you provision VMs, and access to your master key. Give it careen and
the tools it drives:

```nix
{ pkgs, inputs, ... }:
{
  environment.systemPackages = [
    inputs.careen.packages.${pkgs.stdenv.hostPlatform.system}.careen
    pkgs.sops
    pkgs.age
    pkgs.vulnix
  ];
  environment.variables = {
    CAREEN_FLAKE = "git+ssh://git@example.com/me/fleet.git";
    CAREEN_REPO = "~/fleet";   # a writable checkout, for add, keys and secrets
  };
}
```

`CAREEN_FLAKE` is what careen deploys (the pushed branch); `CAREEN_REPO` is where it edits.

## Every day: look

```console
$ careen tui
```

The TUI is the dashboard: drift, pending reboots and failed units for every host, refreshed every
30 seconds, with a key for each fix. When something's red, ++l++ follows its logs and ++s++ opens a
shell there.

## Changing something

The loop is always the same: edit, commit, push, deploy.

```console
$ git commit -qam "web: raise worker count" && git push
$ careen deploy --tag web --plan
$ careen deploy --tag web
```

careen builds each host from the pushed commit, switches it, checks it, and rolls it back on its own
if it isn't healthy. If you'd rather review before anything restarts, use `operation = "boot"` on
those hosts and roll them with `--reboot`.

## Weekly: patch

Update nixpkgs, deploy everywhere, and reboot only what needs it, one host at a time and with each
host's hooks:

```console
$ nix flake update && git commit -qam "flake.lock: update" && git push
$ careen deploy --all --reboot
$ careen vulns                  # what's still known-vulnerable
$ careen gc --all --older-than 30d
```

Hosts with `rebootPolicy = "manual"` (the router, the machine careen runs on) are left for you:
`careen reboot --only router` when it suits.

## Automate it

The same commands run unattended. A systemd timer on the management machine patches every Sunday
night and stops at the first host that has trouble; a timed-out drain is aborted rather than
forced:

```nix
systemd.services.careen-patch = {
  path = [ /* careen, nix, git, openssh */ ];
  environment.CAREEN_FLAKE = "git+ssh://git@example.com/me/fleet.git";
  serviceConfig = { Type = "oneshot"; User = "admin"; };
  script = "careen deploy --all --reboot --yes --on-timeout abort --json > /var/log/careen-patch.json";
};
systemd.timers.careen-patch = {
  wantedBy = [ "timers.target" ];
  timerConfig.OnCalendar = "Sun 02:00";
};
```

Under `--json` careen never prompts: questions become `decision` events and exit code 3, and every
step is an event you can feed to monitoring. See [Scripting](../guides/scripting.md).

A check in CI that fails when anything has drifted or failed:

```bash
careen status --json | jq -e '[.hosts[] | select(.error != null or .drift != "current" or (.failedUnits | length) > 0)] | length == 0'
```

## Adding and retiring machines

- **A new machine:** `careen add NAME --address ADDR`, commit, then `careen deploy --only NAME`
  (or `careen up --only NAME` for a VM careen creates). See
  [Onboarding hosts and secrets](secrets.md).
- **A reinstalled machine:** `careen keys` shows `drift`; `careen keys register NAME --replace`,
  then deploy.
- **Retiring one:** for a VM, `careen down --only NAME` first; then remove it from the flake and
  `careen keys rekey --all` so its key no longer opens anything.

## A NixOS release upgrade

```console
$ # point nixpkgs at the new release in flake.nix, fix what the release notes ask for
$ nix flake update nixpkgs && git commit -qam "nixos: 26.11" && git push
$ careen deploy --tag web --reboot      # a group you can afford to lose first
$ careen deploy --all --reboot
$ careen template build --replace       # new VMs start from the new release
```

## What to back up

| What | Where | Why |
| --- | --- | --- |
| The flake | its git remote | everything careen does comes from it |
| Your master key | wherever you keep it (a security key, a password manager) | it decrypts every secret and re-keys hosts |
| OpenTofu's state | `~/.local/state/careen/pve` on the management machine | without it careen loses track of the VMs it made |

Everything else (caches, SSH sockets, vulnerability reports) careen rebuilds on its own; see
[Environment and files](../reference/environment.md).
