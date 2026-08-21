{
  cacert,
  fetchFromGitHub,
  fetchPnpmDeps,
  lib,
  makeBinaryWrapper,
  node-gyp,
  nodejs-slim,
  nodejs_24,
  pnpm_11,
  pnpmBuildHook,
  pnpmConfigHook,
  python3,
  rustPlatform,
  stdenv,
}:

let
  version = "0.0.33";
  src = fetchFromGitHub {
    owner = "pingdotgg";
    repo = "t3code";
    tag = "v${version}";
    hash = "sha256-qZi9hMGzqpmnpqvvVtsQvkZIiVqTgOMWv1y15MiSAYg=";
  };
  pnpm = pnpm_11;
  nodejs = nodejs_24;
  runtimeNodejs = nodejs-slim;

  resourceMonitor = rustPlatform.buildRustPackage {
    pname = "t3-resource-monitor";
    inherit version src;
    sourceRoot = "${src.name}/native/resource-monitor";
    cargoHash = "sha256-5cmG2daM1bVOA23gjjoalbx0fEL1hmqV6WZov0sUZp8=";

    meta = {
      description = "Native resource telemetry helper for T3 Code";
      license = lib.licenses.mit;
    };
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
    pnpmBuildHook
    pnpmConfigHook
    python3
  ];

  # Include T3's server and its workspace dependencies, but never the desktop
  # application. The root and scripts packages provide the Vite+ build driver.
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
    hash = "sha256-4WbrYQs/CB+tYBR+D4k03sOR4CNsjG7qLk0Q1aPmATw=";
  };

  VP_SKIP_INSTALL = "1";

  postPatch = ''
    substituteInPlace apps/web/vite.config.ts \
      --replace-fail \
        'const host = explicitHost || "localhost";' \
        'const host = explicitHost || "127.0.0.1";'
    substituteInPlace package.json \
      --replace-fail \
        '"prepare": "node scripts/clean-tsgo-backups.mjs && effect-tsgo patch && vp config --no-agent"' \
        '"prepare": "node scripts/clean-tsgo-backups.mjs && effect-tsgo patch"'
    printf '\nverifyDepsBeforeRun: false\n' >> pnpm-workspace.yaml
  '';

  preBuild = ''
    export pnpm_config_verify_deps_before_run=false
    node scripts/update-release-package-versions.ts ${version}

    export npm_config_nodedir=${nodejs}
    export ELECTRON_SKIP_BINARY_DOWNLOAD=1
    pnpm rebuild --pending "''${pnpmInstallFlags[@]}" --filter '!@t3tools/monorepo'
  '';

  # Vite+ knows the build graph: the server build depends on the web client,
  # while Electron and the desktop package are outside this filter.
  pnpmBuildScript = "build:headless";

  preBuildPhases = [ "addHeadlessBuildScript" ];
  addHeadlessBuildScript = ''
    node -e '
      const fs = require("node:fs");
      const path = "package.json";
      const pkg = JSON.parse(fs.readFileSync(path, "utf8"));
      pkg.scripts["build:headless"] = "vp run --filter t3 build";
      fs.writeFileSync(path, JSON.stringify(pkg, null, 2) + "\n");
    '
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

    # T3 always supplies the configured external Claude executable to the
    # Agent SDK. Drop the SDK's optional bundled provider binary so provider
    # executables remain separate PAW layers.
    rm -rf \
      "$out"/libexec/t3code/node_modules/.pnpm/@anthropic-ai+claude-agent-sdk-*/

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

  meta = {
    description = "Headless T3 Code server and web client";
    homepage = "https://github.com/pingdotgg/t3code";
    changelog = "https://github.com/pingdotgg/t3code/releases/tag/v${version}";
    license = lib.licenses.mit;
    mainProgram = "t3";
    platforms = lib.platforms.linux;
  };
})
