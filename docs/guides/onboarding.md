# Onboarding, keys and secrets

careen manages the [sops](https://github.com/getsops/sops) side of each host: its key in
`.sops.yaml`, which files are encrypted to it, and the secret values themselves.

These commands edit a writable checkout of your flake (see
[finding the checkout](../concepts/flake-settings.md#finding-the-checkout)) and leave every change
**uncommitted** for you to review. New files are marked with `git add --intent-to-add` so the
flake can evaluate them before you commit.

They rely on the layout in your flake's [`careen` output](../concepts/flake-settings.md).

## Adding a host

```console
$ careen add web1 --address 10.0.0.20 --ip 10.0.0.20 --gateway 10.0.0.1
$ careen add web1 --status          # where onboarding stands
```

`careen add` runs these steps, skipping any that are already done:

1. Write `<hostDir>/web1/web1.nix` from the flake's host template.
2. Add web1's key anchor (a placeholder), creation rule and shared-rule membership to
   `.sops.yaml`.
3. Create web1's secrets directory.
4. Check that web1's system evaluates.
5. Once web1 answers on SSH: record its host key (as an age key) and re-key the files it should
   open. This step needs the master key.

Because finished steps are skipped, you can run it before the machine exists (steps 1-4; it exits
3, "waiting") and again once it has booted (step 5). `--deploy [--as USER]` deploys the host
straight from the working tree afterwards.

A host already defined in the flake, say in a cluster's node table, keeps its module: no file is
written, and its address comes from its inventory entry. Such hosts can be selected in bulk:

```console
$ careen add --tag cluster --yes
```

### Pre-generated host keys

For hosts careen provisions (`provisioner = "proxmox"`), or any host with `--pregen-key`, careen
makes the SSH host key itself instead of waiting to read it from the machine:

- the key is stored in `<secretsDir>/NAME/host-key.yaml`, its private half encrypted, its public
  half in clear so careen can check it without the master key;
- its age recipient goes into `.sops.yaml` right away;
- `careen up` installs it on the machine.

Onboarding then finishes before the machine exists. Every secret can be encrypted to the host up
front, and destroying and recreating the host needs no sops changes.

## Keys

```console
$ careen keys                    # recorded vs live keys, files not re-keyed
$ careen keys register web1      # record web1's live key, re-key its files
$ careen keys register web1 --replace   # after a reinstall changed its key
$ careen keys rekey --all        # re-encrypt every host's files to its current keys
```

`careen keys` compares each host's age key in `.sops.yaml` with the key its SSH host key gives
now. A host's files are its own secrets directory and every other file under the secrets root
whose creation rule lists the host's anchor: the shared rules, and rules for files only some
hosts share. `rekey` and `register` re-key all of them.

| State | Meaning | Fix |
| --- | --- | --- |
| `ok` | Recorded, live and every file agree. | |
| `placeholder` | Never registered (`age1REPLACEME_...`). | `careen keys register HOST` |
| `drift` | The host was reinstalled; it can't decrypt its secrets. | `careen keys register HOST --replace` |
| `needs-rekey` | Some files aren't encrypted to it yet. | `careen keys rekey HOST` |
| `no-anchor` | In the inventory but not in `.sops.yaml`. | `careen add HOST` |
| `unreachable` | careen couldn't fetch the live key. | |

## Secrets

```console
$ careen secrets                       # secret names per host and shared file; nothing is decrypted
$ careen secrets set web1 api_token    # value from stdin, or a hidden prompt
$ careen secrets set web1 api_token --file app.yaml < token.txt
$ careen secrets unset web1 api_token
$ careen secrets edit web1             # sops opens the file in $EDITOR
```

- Values only ever reach sops on stdin, never on a command line. One trailing newline is dropped
  unless `--keep-newline`.
- The default file is `secrets.yaml` in the host's secrets directory; `--file` picks another.
- `careen secrets` also lists the shared files: every file under the secrets root that a creation
  rule covers and that isn't a host's own. `set`, `unset` and `edit` change only host files.

## The master key

Re-keying and changing existing secrets need a master key from `masterKeys`. careen checks for it
first and stops with exit 3 if it's missing:

- **PGP master keys** must be usable through `gpg` here: a local key, a security key, or a
  forwarded gpg-agent.
- **age master keys** must be among the identities sops reads (`SOPS_AGE_KEY`,
  `SOPS_AGE_KEY_FILE` or `~/.config/sops/age/keys.txt`). Keys sops gets from a command
  (`SOPS_AGE_KEY_CMD`) or plugin recipients can't be checked in advance and are assumed present.

Creating a *new* secrets file needs only public keys.

When a decryption waits on your security key (a touch, or a PIN), careen says so with a bell
(`>>> Waiting for your security key (touch it if it's blinking): ...`) and offers to retry a missed
touch. It can't tell in advance whether a touch is needed (the key may have a recent touch cached),
so it only speaks up once a decryption has waited 0.75 seconds; one that needs no touch
stays quiet.

## Checking your setup

```console
$ careen doctor            # tools, flake, checkout, master key, SSH
$ careen doctor --strict   # exit 3 if any check fails, for scripts
```
