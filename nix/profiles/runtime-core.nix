{ pkgs, ... }:

{
  paw.profile.runtimePackages = with pkgs; [
    bashInteractive
    cacert
    coreutils
    getent
    findutils
    (gitMinimal.override {
      curl = curlMinimal;
      nlsSupport = false;
      doInstallCheck = false;
    })
    gnugrep
    gnused
    openssh
  ];
}
