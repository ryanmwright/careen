#!/usr/bin/env bash
# Check that the example flakes still work: each one evaluates, every host's
# system builds, careen reads its fleet, and its own check.sh (if any)
# passes. CI runs it per example; locally:
#
#   scripts/check-examples.sh               every example
#   scripts/check-examples.sh first-fleet   one
#
# The examples' careen input points at this checkout, so they're checked
# against the careen they'll be released with.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$PWD
careen="$(nix build --no-link --print-out-paths .#careen)/bin/careen"
override=(--override-input careen "path:$root")

if [ $# -eq 0 ]; then
  mapfile -t examples < <(ls examples)
else
  examples=("$@")
fi

for ex in "${examples[@]}"; do
  dir="./examples/$ex"
  echo "== $ex"
  nix flake check "$dir" "${override[@]}" --no-build
  hosts=$(nix eval --json "$dir#nixosConfigurations" "${override[@]}" --apply builtins.attrNames 2>/dev/null || echo '[]')
  for h in $(jq -r '.[]' <<<"$hosts"); do
    echo "-- building $h"
    nix build --no-link "$dir#nixosConfigurations.$h.config.system.build.toplevel" "${override[@]}"
  done
  if nix eval "$dir#fleet" "${override[@]}" >/dev/null 2>&1; then
    "$careen" hosts --flake "$dir"
  fi
  if [ -x "$dir/check.sh" ]; then
    (cd "$dir" && ./check.sh)
  fi
done
