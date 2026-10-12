# Boots a host's disk image as a local VM. The first run copies the image
# to .vm/<name>.qcow2; later runs boot that copy, so what careen deploys
# survives. The VM's SSH and HTTP are forwarded to local ports.
{
  writeShellApplication,
  qemu_kvm,
  name,
  image,
  ssh,
  http,
}:
writeShellApplication {
  name = "vm-${name}";
  runtimeInputs = [ qemu_kvm ];
  text = ''
    dir="''${VM_DIR:-.vm}"
    disk="$dir/${name}.qcow2"
    if [ ! -e "$disk" ]; then
      mkdir -p "$dir"
      install -m 644 ${image}/*.qcow2 "$disk"
    fi
    echo "${name}: ssh on 127.0.0.1:${toString ssh}, http on 127.0.0.1:${toString http}; Ctrl-a x stops it"
    exec qemu-system-x86_64 -enable-kvm -cpu host -smp 2 -m 1024 \
      -drive "file=$disk,if=virtio" \
      -nic "user,model=virtio,hostfwd=tcp:127.0.0.1:${toString ssh}-:22,hostfwd=tcp:127.0.0.1:${toString http}-:80" \
      -nographic
  '';
}
