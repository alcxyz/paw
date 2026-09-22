{ pkgs, helper }:

pkgs.dockerTools.buildLayeredImage {
  name = "paw-backup-helper";
  tag = "dev";
  contents = [ helper ];
  extraCommands = ''
    mkdir -p backup/state backup/work tmp
  '';
  fakeRootCommands = ''
    chown -R 65532:65532 backup tmp
  '';
  config = {
    User = "65532:65532";
    Entrypoint = [ "/bin/paw-backup-helper" ];
    Cmd = [ "serve" ];
    WorkingDir = "/backup";
    Labels = {
      "org.opencontainers.image.title" = "PAW offline backup helper";
      "org.opencontainers.image.version" = "0.0.0-dev";
    };
  };
}
