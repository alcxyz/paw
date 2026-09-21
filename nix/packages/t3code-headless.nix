{
  applyPatches,
  cacert,
  fetchFromGitHub,
  fetchPnpmDeps,
  lib,
  makeBinaryWrapper,
  node-gyp,
  nodejs-slim,
  nodejs_24,
  pnpm_11,
  pnpmConfigHook,
  python3,
  rustPlatform,
  sourceArchive ? null,
  sourceSpec ? builtins.fromJSON (builtins.readFile ./t3code/source.json),
  stdenv,
  patches ? [ ],
}:

let
  upstreamSrc =
    if sourceArchive != null then
      sourceArchive
    else
      fetchFromGitHub {
        inherit (sourceSpec)
          hash
          owner
          repo
          rev
          ;
      };
  src =
    if patches == [ ] then
      upstreamSrc
    else
      applyPatches {
        name = "t3code-${sourceSpec.version}-source";
        src = upstreamSrc;
        inherit patches;
      };
  resourceMonitor = rustPlatform.buildRustPackage {
    pname = "t3-resource-monitor";
    inherit src;
    inherit (sourceSpec) version cargoHash;
    sourceRoot = "${src.name}/native/resource-monitor";
  };
  inherit (sourceSpec) version;
  pnpm = pnpm_11;
  nodejs = nodejs_24;
  runtimeNodejs = nodejs-slim;

  spdxLicenseListData = fetchFromGitHub {
    owner = "spdx";
    repo = "license-list-data";
    rev = "c4a7237ec8f4654e867546f9f409749300f1bf4c";
    hash = "sha256-FbeeEBAg9ih6DkAsXdU6ruZwkC7A2u2zYBvblpl54q0=";
  };
in
stdenv.mkDerivation (finalAttrs: {
  pname = "t3code-headless";
  inherit version src;

  strictDeps = true;
  __structuredAttrs = true;

  nativeBuildInputs = [
    cacert
    makeBinaryWrapper
    node-gyp
    nodejs
    pnpm
    pnpmConfigHook
    python3
  ];

  # Include T3's server and its workspace dependencies, but never the desktop
  # application. Root and scripts packages provide the build/release drivers.
  pnpmWorkspaces = [
    "@t3tools/monorepo"
    "t3..."
    "@t3tools/scripts..."
  ];

  pnpmDeps = fetchPnpmDeps {
    inherit pnpm;
    inherit (finalAttrs)
      pname
      version
      src
      pnpmWorkspaces
      ;
    fetcherVersion = 4;
    hash = sourceSpec.pnpmDepsHash;
  };

  VP_SKIP_INSTALL = "1";

  postPatch = ''
    substituteInPlace apps/web/vite.config.ts \
      --replace-fail \
        'const host = explicitHost || "localhost";' \
        'const host = explicitHost || "127.0.0.1";'
    substituteInPlace package.json \
      --replace-fail \
        ' && vp config --no-agent' \
        ""
    printf '\nverifyDepsBeforeRun: false\n' >> pnpm-workspace.yaml
    mkdir -p .generated/third-party-licenses/spdx
    ln -s ${spdxLicenseListData}/json/details \
      .generated/third-party-licenses/spdx/v3.28.0
  '';

  preBuild = ''
    export pnpm_config_verify_deps_before_run=false
    node scripts/update-release-package-versions.ts ${version}

    export npm_config_nodedir=${nodejs}
    export ELECTRON_SKIP_BINARY_DOWNLOAD=1
    pnpm rebuild --pending "''${pnpmInstallFlags[@]}" --filter '!@t3tools/monorepo'
  '';

  # Build deliverables directly so a nested task failure cannot be hidden.
  buildPhase = ''
    runHook preBuild
    pnpm --dir apps/web exec vp build
    pnpm --dir apps/server exec node scripts/cli.ts build --verbose
    runHook postBuild
  '';

  postBuild = ''
    pnpm vp cache clean
  '';

  dontPatchELF = true;
  noAuditTmpdir = true;

  installPhase = ''
    runHook preInstall

    mkdir -p "$out/libexec"
    pnpm --config.inject-workspace-packages=true \
      --filter t3 deploy --prod --offline "$out/libexec/t3code"

    # The server bundles the SDK's JavaScript and supplies an external Claude
    # executable. Drop the redundant package and any optional provider payload.
    rm -rf \
      "$out"/libexec/t3code/node_modules/.pnpm/@anthropic-ai+claude-agent-sdk@*/

    # node-pty has been built for the host. Foreign prebuilds and build-system
    # metadata are neither executable nor needed at runtime.
    find "$out/libexec/t3code/node_modules/.pnpm" \
      -path '*/node-pty/prebuilds' -prune -exec rm -rf {} +
    find "$out/libexec/t3code/node_modules/.pnpm" \
      -path '*/node-pty/build/*' -type f \
      ! -path '*/node-pty/build/Release/*' -delete

    install -Dm755 ${resourceMonitor}/bin/t3-resource-monitor \
      "$out/libexec/t3code/dist/resource-monitor/t3-resource-monitor"

    find "$out/libexec/t3code" -xtype l -delete

    makeWrapper ${lib.getExe runtimeNodejs} "$out/bin/t3" \
      --add-flags "$out/libexec/t3code/dist/bin.mjs" \
      --set-default T3CODE_RESOURCE_MONITOR_PATH \
        "$out/libexec/t3code/dist/resource-monitor/t3-resource-monitor"

    runHook postInstall
  '';

  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck
    "$out/bin/t3" --version
    "$out/bin/t3" --help >/dev/null
    runHook postInstallCheck
  '';

  passthru = {
    inherit
      patches
      resourceMonitor
      sourceArchive
      sourceSpec
      ;
    sourceRevision = sourceSpec.rev;
  };

  meta = {
    description = "Headless T3 Code server and web client";
    homepage = "https://github.com/${sourceSpec.owner}/${sourceSpec.repo}";
    changelog = "https://github.com/${sourceSpec.owner}/${sourceSpec.repo}/commit/${sourceSpec.rev}";
    license = lib.licenses.mit;
    mainProgram = "t3";
    platforms = lib.platforms.linux;
  };
})
