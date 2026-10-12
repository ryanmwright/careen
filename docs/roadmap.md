# Roadmap

Planned work, roughly in order. Nothing here is built yet.

## Rolling reboots, further

[Rolling reboots](guides/reboots.md#rolling-reboots) run hooks and gates one host at a time. Still
to come: rebooting `serial` hosts at once within a group, and reusable presets for common
setups, so several flakes needn't each write the same hooks.

## Provisioning

- **OpenTofu state in S3** (MinIO), with locking and client-side encryption, instead of a local
  directory.
- **Adopting existing VMs** into OpenTofu state without changing them (`careen vm import`), and
  non-NixOS VMs in the inventory.
- **Snapshots and backups** (`careen vm snapshot`, `rollback`, `backup`, `prune`), and
  `deploy --snapshot` to snapshot before a risky switch.
- **VM state in `status`** and the TUI.
- **Safer state handling:** refuse a truncated state file instead of planning against it.
- **Stale templates:** rebuild a node's template when it's older than the template host's config.
- **Other provisioners**, such as nixos-anywhere for bare metal and VPSs.
