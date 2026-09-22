{
  codexCli,
  lib,
  nodejs-slim,
  stdenvNoCC,
}:

stdenvNoCC.mkDerivation {
  pname = "codex-runtime";
  inherit (codexCli) version;

  strictDeps = true;
  dontUnpack = true;

  installPhase = ''
    runHook preInstall

    mkdir -p "$out/bin" "$out/libexec"
    cp -R ${codexCli}/lib/node_modules/@openai/codex "$out/libexec/codex"
    chmod -R u+w "$out/libexec/codex"

    entrypoint="$out/libexec/codex/bin/codex.js"
    substituteInPlace "$entrypoint" \
      --replace-fail "$(head -n 1 "$entrypoint")" \
      '#!${lib.getExe nodejs-slim}'
    chmod 755 "$entrypoint"
    ln -s ../libexec/codex/bin/codex.js "$out/bin/codex"

    # The shared package is an immutable source of the tested npm runtime tree.
    # Its buildNpmPackage launcher points at full Node; neither that package nor
    # its interpreter should remain in this focused runtime output.
    if grep -R -F -l '${codexCli}' "$out"; then
      echo "codex-runtime retains a reference to its source package" >&2
      exit 1
    fi

    runHook postInstall
  '';

  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck
    "$out/bin/codex" --version
    "$out/bin/codex" --help >/dev/null
    runHook postInstallCheck
  '';

  passthru = {
    inherit codexCli;
    inherit (codexCli) npmDeps packageLock sourceSpec;
  };

  meta = {
    description = "Focused Codex CLI runtime for PAW workspace images";
    inherit (codexCli.meta) homepage license;
    mainProgram = "codex";
    platforms = lib.platforms.linux;
  };
}
