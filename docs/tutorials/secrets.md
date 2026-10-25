# Onboarding hosts and secrets

Give your hosts secrets with [sops](https://github.com/getsops/sops) and
[sops-nix](https://github.com/Mic92/sops-nix), and let careen do the bookkeeping: each host's key
in `.sops.yaml`, which files it can open, and adding a new host from a template.

!!! info "What you need"
    Everything from [Your first fleet](first-fleet.md); this tutorial runs two VMs (2 GB of memory).

## 1. Start from the template

```console
$ nix flake new -t github:ryanmwright/careen#secrets my-fleet
$ cd my-fleet && git init -q && git add -A
$ nix develop                    # careen, sops, age, ssh
$ export CAREEN_FLAKE=$PWD
```

It's the first-fleet example plus three things careen's onboarding uses:

=== "careen settings (flake.nix)"

    ```nix
    --8<-- "secrets/flake.nix:careen-settings"
    ```

=== "templates/host.nix.tmpl"

    ```nix
    --8<-- "secrets/templates/host.nix.tmpl"
    ```

=== ".sops.yaml"

    ```yaml
    --8<-- "secrets/.sops.yaml"
    ```

- The **`careen` output** tells careen where hosts and secrets live; see
  [Flake settings](../concepts/flake-settings.md).
- The **host template** is what `careen add` writes for a new host.
- **`.sops.yaml`** says who can decrypt what: every host's files open with the master key (yours)
  and that host's own key, which comes from its SSH host key. web1's key is still a placeholder.

Hosts are discovered from `hosts/`: every directory there is a machine, and VM `n` (alphabetically)
forwards SSH to port `2200+n` and HTTP to `8000+n`.

## 2. Your master key

The master key is what lets you decrypt and re-key everything:

```console
$ age-keygen -o admin.agekey
$ sed -i "s/age1REPLACEME_ADMIN/$(age-keygen -y admin.agekey)/" .sops.yaml
$ export SOPS_AGE_KEY_FILE=$PWD/admin.agekey
```

`admin.agekey` is ignored by git. Keep it safe: anyone with it can read every secret.

## 3. Register web1's key

Make the demo key and SSH alias as in [Your first fleet](first-fleet.md#2-a-key-and-an-ssh-alias),
plus a second alias `web2-vm` for port 2202. Commit (`git commit -qm "my fleet"`), boot web1
(`nix run .#vm-web1` in another terminal), then:

```console
$ careen keys
HOST  STATE        RECORDED            LIVE               NOT RE-KEYED
web1  placeholder  age1REPLACEME_WEB1  age1…               -
$ careen keys register web1
$ git commit -qam "web1: register its key"
```

careen read web1's SSH host key, turned it into an age key, recorded it as `&web1_host` and
re-encrypted web1's files to it (there are none yet).

## 4. A secret for web1

```console
$ echo "hello, secretly" | careen secrets set web1 greeting
$ careen secrets
web1
  secrets/hosts/web1/secrets.yaml, changed …
    greeting
```

The value went to sops on stdin; it never appears in a command line or your shell history. Now let
web1 use it: in `hosts/web1/web1.nix`, uncomment the two `sops` lines, then:

```console
$ git add -A && git commit -qm "web1: serve the greeting"
$ careen deploy --all
$ curl -s 127.0.0.1:8001/greeting
hello, secretly
```

web1 decrypted the file with its own host key; careen never sent it the value.

## 5. Onboard a new host

Add web2 before its machine exists:

```console
$ careen add web2 --address web2-vm --yes
  ✓ host module          wrote hosts/web2/web2.nix
  ✓ .sops.yaml entries   added &web2_host, its rule, and joined the shared rules
  ✓ secrets directory    created secrets/hosts/web2
  … host key registered  waiting for web2-vm to answer on SSH; run again once it's up
  ✓ system evaluates     nixosConfigurations.web2 evaluates
```

It stops at the key (exit 3) because web2 isn't running yet. Boot it (`nix run .#vm-web2`), then run
the same command again to finish, and commit:

```console
$ careen add web2 --address web2-vm --yes
$ git add -A && git commit -qm "web2: onboard"
$ careen status
```

Steps already done are skipped, so `careen add` is safe to rerun at any point.

## 6. A host that was reinstalled

A reinstalled host gets a new SSH host key and can no longer open its secrets. Simulate it: stop
web2, delete `.vm/web2.qcow2` and boot it again. Then:

```console
$ careen keys
web2  drift  age1…  age1…  -
$ careen keys register web2 --replace
$ git commit -qam "web2: new host key"
$ careen deploy --only web2
```

## Going further

- Secrets every host shares: add a rule for `secrets/common/.*\.yaml$` and list it in
  `sharedRules`; `careen add` joins new hosts to it.
- Secrets only some hosts share: add a rule for, say, `secrets/backup/.*\.yaml$` with those hosts'
  anchors. `careen secrets` lists the file, and `careen keys` re-keys it with its hosts' files.
- A master key on a security key: use a PGP key in `masterKeys.pgp` instead; careen asks you to touch
  it when it needs it. See [Onboarding, keys and secrets](../guides/onboarding.md).
- `careen doctor` checks the whole setup, including whether your master key is available.
