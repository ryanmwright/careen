# Contributing

```console
nix develop
go test ./...
nix flake check          # careen, its tests and the documentation site
scripts/docs.sh serve    # preview the docs at http://127.0.0.1:8000
```

## Keep the docs in step

The documentation site (`docs/`, published from `main` to GitHub Pages) is part of the change, not
a follow-up. A pull request that changes what careen does updates the pages that describe it.

| Change | Update |
| --- | --- |
| A command's flags or help text | Nothing by hand: `docs/reference/cli.md` is generated from the command tree (`careen gen-docs`). Write the `Short`, `Long` and flag usage as you'd want them on the site. |
| A new command | Its guide page under `docs/guides/` (or a new page, added to `nav` in `mkdocs.yml`). |
| A JSON schema or event | `docs/reference/json-output.md` (a test fails if a schema is missing) and, for events, `docs/guides/scripting.md`. |
| A `fleet` field | `docs/reference/inventory-fields.md` (a test fails if it's missing) and `docs/concepts/inventory.md`. |
| A `careen` or `pve` flake setting | `docs/concepts/flake-settings.md` or `docs/guides/provisioning-proxmox.md` (tests check both). |
| Reboot reasons or policy | `docs/concepts/reboot-policy.md` and `docs/guides/reboots.md`. |
| Reboot hooks, ordering or timeout answers | `docs/guides/reboots.md` (Rolling reboots), `docs/reference/inventory-fields.md`, and `docs/guides/tui.md` for the prompt. |
| Deploy, health checks or rollback | `docs/guides/deploy-and-rollback.md`. |
| Onboarding, keys or secrets | `docs/guides/onboarding.md`. |
| Provisioning | `docs/guides/provisioning-proxmox.md`. |
| TUI keys | `docs/guides/tui.md`. |
| Paths, environment variables, tools | `docs/reference/environment.md`. |
| Planned work done, or new plans | `docs/roadmap.md`. |

`mkdocs build --strict` (run by `nix flake check`) fails on broken links and anchors.

## Examples and tutorials

`examples/` holds the flakes behind the tutorials (`docs/tutorials/`), which embed their files with
`--8<--`, so a renamed file breaks the docs build. A change that affects an example updates the
example and its tutorial together; `scripts/check-examples.sh` (and CI) must keep passing.

## Stay deployment-neutral

careen is for any NixOS flake. Use made-up names in code, tests and docs (`web1`, `db1`, `mgr`,
`*.example`, `10.0.0.x`, generated keys), never a real fleet's, and read only the flake's outputs
in Go code. `internal/docscheck` checks the latter, and the former too when
`CAREEN_PRIVATE_PATTERNS` is set (see `docs/development.md`).

## Writing docs

- Lead with what the reader can do, then how it works.
- Show real commands in `console` blocks, with `$ ` prompts.
- Use tables for options and states, admonitions (`!!! note`, `!!! warning`) sparingly.
- Describe what the code does today; plans go in the roadmap.
