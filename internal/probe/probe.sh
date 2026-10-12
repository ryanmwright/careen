# Run on each host through `ssh HOST sh -s`. Prints key=value lines that
# probe.Parse reads; a missing tool leaves its value empty rather than
# failing the probe. Keep it POSIX sh.
set -u

echo "hostname=$(cat /proc/sys/kernel/hostname 2>/dev/null)"
echo "nixos_version=$(nixos-version 2>/dev/null)"
echo "config_revision=$(nixos-version --configuration-revision 2>/dev/null)"

echo "current_system=$(readlink -f /run/current-system 2>/dev/null)"
echo "profile_system=$(readlink -f /nix/var/nix/profiles/system 2>/dev/null)"

for p in kernel initrd kernel-modules; do
  echo "booted_$p=$(readlink -f "/run/booted-system/$p" 2>/dev/null)"
  echo "current_$p=$(readlink -f "/run/current-system/$p" 2>/dev/null)"
done

echo "system_state=$(systemctl is-system-running 2>/dev/null)"
systemctl --failed --plain --no-legend 2>/dev/null | while read -r unit _; do
  echo "failed_unit=$unit"
done

read -r up _ < /proc/uptime
echo "uptime=$up"

df -B1 --output=size,used /nix/store 2>/dev/null | tail -n 1 | {
  read -r size used
  echo "store_size=$size"
  echo "store_used=$used"
}

echo "generations=$(ls -d /nix/var/nix/profiles/system-*-link 2>/dev/null | wc -l)"
echo "last_switch=$(stat -c %Y /nix/var/nix/profiles/system 2>/dev/null)"
echo "probe_done=1"
