# Run on a host as `sh -s -- LINES UNIT FOLLOW`. Prints the journal as JSON,
# one entry per line. wheel members can read the system journal.
set -eu
n=$1 unit=$2 follow=$3
set -- --no-pager -o json -n "$n"
[ -n "$unit" ] && set -- "$@" -u "$unit"
[ "$follow" = 1 ] && set -- "$@" --follow
exec journalctl "$@"
