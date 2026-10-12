# The `pve` inventory rendered to OpenTofu JSON for the bpg/proxmox provider,
# with plain builtins.toJSON. careen runs it with the flake's `opentofu`.
{ writeText, pve }:
let
  vm = name: h: {
    inherit name;
    node_name = h.node;
    vm_id = h.vmid;
    # A full clone of the node's template (careen template build).
    clone = {
      vm_id = pve.nodes.${h.node}.template;
      full = true;
    };
    agent = {
      enabled = true;
      timeout = "1m";
    };
    boot_order = [ "virtio0" ];
    cpu = {
      inherit (h) cores;
      type = "host";
    };
    memory.dedicated = h.memoryMB;
    disk = [
      {
        interface = "virtio0";
        datastore_id = pve.storage;
        size = h.diskGB;
        file_format = "raw";
      }
    ];
    # An attribute in this provider, so JSON spells out every field,
    # including the deprecated `enabled` (validate warns about it).
    network_device = [
      {
        bridge = h.bridge;
        model = "virtio";
        enabled = true;
        disconnected = false;
        firewall = false;
        mac_address = "";
        vlan_id = 0;
        mtu = 0;
        queues = 0;
        rate_limit = 0;
        trunks = "";
      }
    ];
    initialization = {
      datastore_id = pve.storage;
      ip_config = [ { ipv4 = { address = h.ipv4; gateway = h.gateway; }; } ];
    };
    stop_on_destroy = true;
    # The clone source and first-boot IP only matter when the VM is made.
    lifecycle.ignore_changes = [ "clone" "initialization" ];
  };
in
writeText "main.tf.json" (
  builtins.toJSON {
    terraform.required_providers.proxmox.source = "bpg/proxmox";
    provider.proxmox = {
      inherit (pve) endpoint;
      # Proxmox's self-signed certificate. The API token comes from careen,
      # in PROXMOX_VE_API_TOKEN.
      insecure = true;
    };
    resource.proxmox_virtual_environment_vm = builtins.mapAttrs vm pve.hosts;
  }
)
