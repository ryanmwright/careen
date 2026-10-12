# Logs, garbage collection, commands and CVEs

## Logs

```console
$ careen logs web1                 # the last 200 entries of the host's journal
$ careen logs web1 nginx -n 50     # one unit, the last 50 entries
$ careen logs web1 nginx -f        # keep following until Ctrl-C
```

With `--json --follow`, each entry is one JSON line (`careen.logs.entry/v1`), handy for piping
into `jq`.

## Garbage collection

```console
$ careen gc --all                    # delete system generations older than 14 days
$ careen gc --tag web --older-than 3d
```

runs `nix-collect-garbage --delete-older-than` with sudo on each host and reports the space
freed. The current and booted systems are never removed. This is for when a disk is filling up
now; for routine collection, set `nix.gc.automatic` on your hosts.

## Running a command everywhere

```console
$ careen exec --all -- 'df -h /nix | tail -1'
$ careen exec --tag web --sudo -- systemctl restart nginx
```

runs a shell command on the selected hosts in parallel (`--parallel`, 8) and shows each host's
output and exit code. One argument is run as written, so pipes work; several arguments are
quoted one by one. `--sudo` runs it as root (`sudo -n`), and `--timeout` (1m) gives up on a slow
host. Like other actions, it shows the plan and asks first.

## Known CVEs

```console
$ careen vulns
$ careen vulns --only web1 --refresh
```

scans each host's *running* system with [vulnix](https://github.com/nix-community/vulnix)
against the NVD. The scan runs where careen runs, not on the hosts: a system that isn't in the
local store is copied in first, and the first scan downloads the NVD data (a few hundred MB).

- Reports are cached per host and system for `--max-age` (24h); `--refresh` rescans.
- CVEs listed in `vulnix-whitelist.toml` at the flake's root are left out, so accepted findings
  and false positives live in git.
- Findings don't change the exit code.
