{
  lib,
  buildGoModule,
  version ? "dev",
}:

buildGoModule {
  pname = "careen";
  inherit version;

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
      # internal/docscheck's tests check the reference pages against the code.
      ../docs
    ];
  };

  # Static, so the binary can be copied to another host while developing.
  env.CGO_ENABLED = 0;

  vendorHash = "sha256-bJ7uW8Hlvu0JTbfCZCSoLLK+53jePJWk43Yt5IRCwdw=";

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];

  meta = {
    description = "Patch, reboot and inspect a NixOS fleet defined in a flake";
    mainProgram = "careen";
    platforms = lib.platforms.linux;
  };
}
