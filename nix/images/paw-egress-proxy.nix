{ pkgs, proxy }:

pkgs.dockerTools.buildLayeredImage {
  name = "paw-egress-proxy";
  tag = "dev";
  contents = [ proxy ];
  extraCommands = ''
    mkdir -p etc/paw-egress tmp
  '';
  fakeRootCommands = ''
    chown -R 65532:65532 tmp
  '';
  config = {
    User = "65532:65532";
    Entrypoint = [ "/bin/paw-egress-proxy" ];
    Cmd = [
      "--listen"
      ":3128"
      "--destinations"
      "/etc/paw-egress/destinations"
    ];
    ExposedPorts = {
      "3128/tcp" = { };
    };
    Labels = {
      "org.opencontainers.image.title" = "PAW per-workspace egress proxy";
      "org.opencontainers.image.version" = "0.0.0-dev";
    };
  };
}
