#!/usr/bin/env bash
# Extra check for this example (scripts/check-examples.sh runs it): the
# rendered OpenTofu config is valid for the bundled provider, with no
# registry access.
set -euo pipefail
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
cp "$(nix build --no-link --print-out-paths .#pve-tofu)" "$dir/main.tf.json"
chmod u+w "$dir/main.tf.json"
tofu="$(nix build --no-link --print-out-paths .#opentofu)/bin/tofu"
"$tofu" -chdir="$dir" init -backend=false -input=false >/dev/null
"$tofu" -chdir="$dir" validate
