{
  paw,
  system,
  codexPackageLock ? ../nix/packages/codex-cli/package-lock.json,
  codexSourceSpec ? builtins.fromJSON (builtins.readFile ../nix/packages/codex-cli/source.json),
  t3codePatches ? [ ],
  t3codeSourceArchive ? null,
  t3codeSourceSpec ? builtins.fromJSON (builtins.readFile ../nix/packages/t3code/source.json),
}:

let
  packages = paw.packages.${system};
  t3codeHeadless = packages.t3code-headless.override {
    patches = t3codePatches;
    sourceArchive = t3codeSourceArchive;
    sourceSpec = t3codeSourceSpec;
  };
  codexCli = packages.codex-cli.override {
    packageLock = codexPackageLock;
    sourceSpec = codexSourceSpec;
  };
  codexRuntime = packages.codex-runtime.override {
    inherit codexCli;
  };
in
packages.paw-codex-image.override {
  imageName = "paw-custom-codex";
  inherit t3codeHeadless;
  providerPackages = [ codexRuntime ];
  providers = [ "codex" ];
}
