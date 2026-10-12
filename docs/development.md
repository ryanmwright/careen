# Development

careen is a Go program built with Nix. Everything you need is in the dev shell.

```console
$ nix develop                       # Go, gopls, golangci-lint, sops, age, mkdocs, ...
$ go test ./...
$ nix build                         # ./result/bin/careen
$ nix flake check                   # builds careen and this documentation site
```

## Trying changes on a real fleet

careen is a single static binary, so a development build can run anywhere: build it, copy it to
the machine that manages your fleet, and run it there against a branch of your flake.

```console
$ nix build
$ scp result/bin/careen mgr:/tmp/careen-dev
$ ssh -t mgr CAREEN_FLAKE='git+ssh://git@example.com/me/nix.git?ref=my-branch' /tmp/careen-dev status
```

Wrap this in a script of your own if you do it often; careen doesn't ship one, since the host and
flake are yours.

## Keeping careen deployment-neutral

careen works with any flake, so nothing in it may refer to one particular deployment, and its Go
code reads only the flake's outputs (`fleet`, `careen`, `pve`), never a host's own NixOS options.
`internal/docscheck` enforces the second rule. For the first, set `CAREEN_PRIVATE_PATTERNS` to a
regular expression matching your own domains and subnets; the same test then fails on any file
that mentions them.

## Layout

| Path | What |
| --- | --- |
| `cmd/careen/` | The CLI: one file per command (cobra), flag parsing and output formatting. |
| `internal/actions/` | The work behind each command (deploy, reboot, gc, exec, onboarding, provisioning), shared by the CLI and the TUI. |
| `internal/inventory/` | Reads the `fleet` output; host selection. |
| `internal/probe/`, `internal/status/` | The one-SSH-call host probe and the `status` report. |
| `internal/build/`, `internal/activate/` | Building a system closure and switching to it with a rollback watchdog. |
| `internal/reboot/` | Reboot decisions, ordering and the reboot itself. |
| `internal/provision/` | The provisioner seam; `provision/proxmox/` drives OpenTofu and the Proxmox nodes. |
| `internal/repo/`, `internal/sopsconf/`, `internal/sopsfile/`, `internal/agekey/`, `internal/hostkey/` | The flake checkout, `.sops.yaml`, sops files and host keys. |
| `internal/contract/` | The JSON envelope, events, errors and exit codes. |
| `internal/tui/` | The terminal UI (Bubble Tea). |
| `internal/sshx/`, `internal/execx/`, `internal/sshtest/` | Running commands over SSH and locally; a throwaway sshd for tests. |
| `internal/docscheck/` | Tests that keep the reference pages complete. |

## This documentation

The site is built with [Material for MkDocs](https://squidfunk.github.io/mkdocs-material/) from `docs/` and
`mkdocs.yml`, and published to GitHub Pages from `main`.

```console
$ scripts/docs.sh serve     # live preview at http://127.0.0.1:8000
$ nix build .#docs          # the built site in ./result
```

- **The CLI reference is generated** from the commands' help text by `careen gen-docs`, at build
  time. To change it, change the command's `Short`, `Long` or flag usage.
- **The JSON and inventory references are checked.** `internal/docscheck` fails when the code
  prints a schema, or reads a flake field, that the docs don't mention.
- **Everything else is written by hand.** A change to a command's behaviour updates the matching
  guide in the same commit; see `CONTRIBUTING.md`.
