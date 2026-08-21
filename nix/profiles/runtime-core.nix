{ pkgs, ... }:

{
  paw.profile.runtimePackages = with pkgs; [
    bashInteractive
    cacert
    coreutils
    findutils
    gitMinimal
    gnugrep
    gnused
    openssh
  ];
}
