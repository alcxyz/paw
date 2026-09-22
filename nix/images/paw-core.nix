{
  dockerTools,
  imageName ? "paw-core",
  lib,
  profileName ? "core",
  providerPackages ? [ ],
  providers ? [ ],
  runtimePackages,
  t3codeHeadless,
}:

assert builtins.length providerPackages == builtins.length providers;

let
  uid = 65532;
  gid = 65532;
  accountFiles = dockerTools.fakeNss.override {
    extraPasswdLines = [
      "paw:x:${toString uid}:${toString gid}:PAW workspace:/workspace/state/home:/bin/sh"
    ];
    extraGroupLines = [ "paw:x:${toString gid}:" ];
  };
  providerVersions = map (
    package: "${package.pname or (lib.getName package)}-${package.version or "unknown"}"
  ) providerPackages;
  runtimeContents = [
    t3codeHeadless
    dockerTools.binSh
    accountFiles
  ]
  ++ runtimePackages
  ++ providerPackages;
in
dockerTools.buildLayeredImage {
  name = imageName;
  tag = "dev";
  compressor = "gz";
  maxLayers = 80;
  contents = runtimeContents;

  extraCommands = ''
    mkdir -p tmp workspace/state/home workspace/work
    # containerd 2.2 user/group lookup rejects absolute account-file symlinks.
    # Keep the generated identities, but materialize them in the image root.
    for account_file in passwd group; do
      cp --remove-destination ${accountFiles}/etc/"$account_file" etc/"$account_file"
      chmod 0444 etc/"$account_file"
      test -f etc/"$account_file" && test ! -L etc/"$account_file"
    done
  '';
  fakeRootCommands = ''
    chown -R ${toString uid}:${toString gid} tmp workspace
  '';

  config = {
    User = "${toString uid}:${toString gid}";
    WorkingDir = "/workspace/work";
    Entrypoint = [ "${lib.getExe t3codeHeadless}" ];
    Cmd = [
      "--log-level"
      "warn"
      "start"
      "--mode"
      "web"
      "--host"
      "0.0.0.0"
      "--port"
      "3773"
      "--base-dir"
      "/workspace/state/t3"
      "--no-browser"
      "/workspace/work"
    ];
    Env = [
      "HOME=/workspace/state/home"
      "PATH=${lib.makeBinPath runtimeContents}"
      "TMPDIR=/tmp"
      "XDG_CACHE_HOME=/workspace/state/cache"
      "XDG_CONFIG_HOME=/workspace/state/config"
      "XDG_DATA_HOME=/workspace/state/data"
    ]
    # Provider login state lives on the in-memory session volume for one pod
    # lifetime (ADR-014), never on the persistent state claim.
    ++ lib.optional (builtins.elem "codex" providers) "CODEX_HOME=/workspace/session/codex"
    ++ lib.optional (builtins.elem "claude-code" providers) "CLAUDE_CONFIG_DIR=/workspace/session/claude"
    # Keep Claude Code to its provider endpoints: no telemetry, error
    # reporting, update checks, or marketplace traffic under bounded egress.
    ++ lib.optional (builtins.elem "claude-code" providers) "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1";
    ExposedPorts = {
      "3773/tcp" = { };
    };
    Labels = {
      "org.opencontainers.image.title" = "PAW ${profileName} workspace";
      "org.opencontainers.image.version" = "0.0.0-dev";
      "paw.alc.xyz/contract-version" = "v0";
      "paw.alc.xyz/profile" = profileName;
      "paw.alc.xyz/provider-packages" = lib.concatStringsSep "," providerVersions;
      "paw.alc.xyz/providers" = lib.concatStringsSep "," providers;
      "paw.alc.xyz/t3-version" = t3codeHeadless.version;
      "paw.alc.xyz/t3-source-revision" = t3codeHeadless.sourceRevision;
    };
  };

  passthru = {
    imageTag = "dev";
    inherit
      imageName
      providerPackages
      providers
      runtimeContents
      ;
  };

  meta = {
    description = "Provider-free PAW core workspace image";
    platforms = lib.platforms.linux;
  };
}
