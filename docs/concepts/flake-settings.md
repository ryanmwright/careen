# Flake settings

Commands that edit your flake (`add`, `keys`, `secrets`) need to know its layout: where host
modules go, where secrets live and how `.sops.yaml` is organised. The flake tells careen in a
`careen` output.

```nix
careen = {
  hostTemplate = "templates/host.nix.tmpl";
  hostDir = "hosts";
  secretsDir = "secrets/hosts";
  sopsConfig = ".sops.yaml";
  masterKeys = { pgp = [ "admin_pgp" ]; age = [ ]; };
  sharedRules = [ "secrets/common/.*\\.yaml$" ];
  groupsDir = "secrets";            # optional: secret groups, secrets/<group>/
  # optional, shown with their defaults:
  anchorSuffix = "_host";
  placeholderPrefix = "age1REPLACEME_";
};
```

Paths are relative to the repository root.

| Setting | Meaning |
| --- | --- |
| `hostTemplate` | A Go [text/template](https://pkg.go.dev/text/template) for a new host's module. `careen add` renders it with `.Name`, `.Address`, `.IP`, `.Gateway`, `.Interface` and `.Vars` (from `--set key=value`). |
| `hostDir` | Where `careen add` writes `NAME/NAME.nix`. |
| `secretsDir` | Where each host's secrets directory goes (`NAME/`), including its pre-generated host key, `NAME/host-key.yaml`. |
| `sopsConfig` | The sops configuration file careen edits. |
| `masterKeys` | Anchors in `.sops.yaml` that every host's creation rule also encrypts to, by type: `pgp` and `age`. These are the keys that can always decrypt and re-key. |
| `sharedRules` | Path regexes of existing creation rules that every new host joins, such as secrets common to all hosts. |
| `groupsDir` | Optional. Where [secret groups](../guides/onboarding.md#secret-groups) live, one directory each (`<group>/`). Without it, the group commands and `--group` aren't available. |
| `anchorSuffix` | Optional. Each host's key anchor in `.sops.yaml` is `<name><anchorSuffix>`; default `_host`. |
| `placeholderPrefix` | Optional. A host whose key isn't known yet gets `<placeholderPrefix><NAME>`; default `age1REPLACEME_`. |

## How careen names things in `.sops.yaml`

- Each host's key anchor is `&<name>_host` (`anchorSuffix`).
- A host whose key isn't known yet gets the placeholder `age1REPLACEME_<NAME>`
  (`placeholderPrefix`). `careen keys` reports it as `placeholder` until the key is registered.
- Each host gets a creation rule for `<secretsDir>/<name>/.*\.yaml$` that encrypts to its own key
  and the master keys.
- Each secret group is a creation rule for `<groupsDir>/<group>/.*\.yaml$` that encrypts to the
  master keys and the group's hosts. Any rule of that form is a group, including ones written by
  hand and shared rules under `groupsDir`.

See [Onboarding, keys and secrets](../guides/onboarding.md) for how these are used.

## Finding the checkout

These commands edit a working tree, not the flake reference careen reads the fleet from. careen
uses `--repo`, else `$CAREEN_REPO` (a leading `~/` is your home directory), else the nearest
directory at or above the current one that contains a `flake.nix`. On the machine that manages
your fleet, set `CAREEN_REPO` to your checkout so the commands work from anywhere.
