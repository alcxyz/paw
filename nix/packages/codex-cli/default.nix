{
  buildNpmPackage,
  fetchzip,
  lib,
  packageLock ? ./package-lock.json,
  sourceSpec ? builtins.fromJSON (builtins.readFile ./source.json),
}:

# Adapted from the generic Codex CLI recipe in alcxyz/nix-packages at
# c39770a81ed44414cd7a7e318e3e0caf41391be8.
buildNpmPackage (finalAttrs: {
  pname = "codex-cli";
  inherit (sourceSpec) version;

  src = fetchzip {
    url = "https://registry.npmjs.org/@openai/codex/-/codex-${finalAttrs.version}.tgz";
    inherit (sourceSpec) hash;
  };

  inherit (sourceSpec) npmDepsHash;

  strictDeps = true;

  postPatch = ''
    cp ${packageLock} package-lock.json
  '';

  dontNpmBuild = true;

  passthru = {
    inherit packageLock sourceSpec;
  };

  meta = {
    description = "Lightweight coding agent that runs in your terminal";
    homepage = "https://github.com/openai/codex";
    license = lib.licenses.asl20;
    mainProgram = "codex";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
})
