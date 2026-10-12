# Run on a host as `sh -s -- ACTION ARGS...`. Everything that needs root
# uses `sudo -n`, so a missing sudo rule fails instead of hanging.
set -eu

profile=/nix/var/nix/profiles/system
action=$1
shift

failed_units() {
  systemctl --failed --plain --no-legend 2>/dev/null | while read -r unit _; do
    echo "failed_unit=$unit"
  done
}

case "$action" in
state)
  echo "current=$(readlink -f /run/current-system)"
  echo "profile=$(readlink -f "$profile")"
  failed_units
  ;;

switch) # NEW PREV OPERATION WATCHDOG_SECONDS UNIT
  new=$1 prev=$2 op=$3 secs=$4 unit=$5
  if [ "$secs" -gt 0 ]; then
    # Rolls back on its own unless careen stops the timer after a healthy
    # check, e.g. when the switch cuts careen off from the host.
    sudo -n systemd-run --quiet --unit "$unit" --on-active="${secs}s" \
      --timer-property=AccuracySec=1s \
      /bin/sh -c "$prev/sw/bin/nix-env -p $profile --set $prev && $prev/bin/switch-to-configuration switch"
  fi
  sudo -n nix-env -p "$profile" --set "$new"
  sudo -n "$new/bin/switch-to-configuration" "$op"
  ;;

health) # TIMEOUT_SECONDS
  state=$(timeout "$1" systemctl is-system-running --wait 2>/dev/null || true)
  echo "system_state=$state"
  echo "current=$(readlink -f /run/current-system)"
  failed_units
  ;;

confirm) # UNIT
  sudo -n systemctl stop "$1.timer" 2>/dev/null || true
  ;;

rollback) # PREV UNIT
  sudo -n systemctl stop "$2.timer" 2>/dev/null || true
  sudo -n nix-env -p "$profile" --set "$1"
  sudo -n "$1/bin/switch-to-configuration" switch
  ;;

rollback-generation)
  sudo -n nix-env -p "$profile" --rollback
  sudo -n "$profile/bin/switch-to-configuration" switch
  ;;

gc) # OLDER_THAN, e.g. 14d
  gens() { ls -d /nix/var/nix/profiles/system-*-link 2>/dev/null | wc -l; }
  echo "generations_before=$(gens)"
  if ! out=$(sudo -n nix-collect-garbage --delete-older-than "$1" 2>&1); then
    printf '%s\n' "$out" | tail -n 3 >&2
    exit 1
  fi
  printf '%s\n' "$out" | tail -n 5 | sed 's/^/gc_output=/'
  echo "generations_after=$(gens)"
  ;;

boot-id)
  cat /proc/sys/kernel/random/boot_id
  ;;

reboot)
  sudo -n systemctl reboot
  ;;

sudo-check)
  sudo -n true
  ;;

*)
  echo "unknown action $action" >&2
  exit 2
  ;;
esac
