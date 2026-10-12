{
  lib,
  stdenvNoCC,
  python3,
  careen,
}:

# The documentation site (Material for MkDocs). The CLI reference page is
# generated from careen's own command tree at build time, so it can't drift.
stdenvNoCC.mkDerivation {
  pname = "careen-docs";
  inherit (careen) version;

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../mkdocs.yml
      ../docs
      ../examples
    ];
  };

  nativeBuildInputs = [
    (python3.withPackages (ps: [
      ps.mkdocs-material
      ps.pymdown-extensions
    ]))
    careen
  ];

  buildPhase = ''
    runHook preBuild
    careen gen-docs docs/reference/cli.md
    mkdocs build --strict --site-dir "$out"
    runHook postBuild
  '';

  dontInstall = true;
}
