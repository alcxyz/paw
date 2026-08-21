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
  providerVersions = map (
    package: "${package.pname or (lib.getName package)}-${package.version or "unknown"}"
  ) providerPackages;
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
      "org.opencontainers.image.title" = "PAW ${profileName} workspace";
      "org.opencontainers.image.version" = "0.0.0-dev";
      "paw.alc.xyz/contract-version" = "v0";
      "paw.alc.xyz/profile" = profileName;
      "paw.alc.xyz/provider-packages" = lib.concatStringsSep "," providerVersions;
      "paw.alc.xyz/providers" = lib.concatStringsSep "," providers;
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
