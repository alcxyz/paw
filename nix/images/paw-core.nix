{
  dockerTools,
  lib,
  runtimePackages,
  t3codeHeadless,
}:

let
  uid = 65532;
  gid = 65532;
  runtimeContents = [
    t3codeHeadless
    dockerTools.binSh
    (dockerTools.fakeNss.override {
      extraPasswdLines = [
        "paw:x:${toString uid}:${toString gid}:PAW workspace:/workspace/state/home:/bin/sh"
      ];
      extraGroupLines = [ "paw:x:${toString gid}:" ];
    })
  ]
  ++ runtimePackages;
in
dockerTools.buildLayeredImage {
  name = "paw-core";
  tag = "dev";
  compressor = "gz";
  maxLayers = 80;
  contents = runtimeContents;

  extraCommands = ''
    mkdir -p tmp workspace/state/home workspace/work
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
    ];
    ExposedPorts = {
      "3773/tcp" = { };
    };
    Labels = {
      "org.opencontainers.image.title" = "PAW core workspace";
      "org.opencontainers.image.version" = "0.0.0-dev";
      "paw.alc.xyz/contract-version" = "v0";
      "paw.alc.xyz/profile" = "core";
    };
  };

  passthru = {
    inherit runtimeContents;
  };

  meta = {
    description = "Provider-free PAW core workspace image";
    platforms = lib.platforms.linux;
  };
}
