#!/usr/bin/env bash
# Preview or build the documentation site, from the dev shell (nix develop):
#
#   scripts/docs.sh serve    live preview at http://127.0.0.1:8000 (default)
#   scripts/docs.sh build    build into ./site, failing on broken links
#
# The CLI reference page is generated from the code first; it isn't committed.
set -euo pipefail

cd "$(dirname "$0")/.."
go run ./cmd/careen gen-docs docs/reference/cli.md
case "${1:-serve}" in
serve) exec mkdocs serve ;;
build) exec mkdocs build --strict ;;
*)
  echo "usage: $0 [serve|build]" >&2
  exit 2
  ;;
esac
