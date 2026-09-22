{
  sourceRoot,
  marker,
  mountNs,
  pidNs,
  netNs,
}:

let
  flake = builtins.getFlake sourceRoot;
  nixpkgs = flake.inputs.nixpkgs;
  pkgs = import nixpkgs { system = builtins.currentSystem; };
  lib = pkgs.lib;
  probeId = builtins.substring 0 24 (
    builtins.hashString "sha256" (
      builtins.concatStringsSep "\n" [
        sourceRoot
        marker
        mountNs
        pidNs
        netNs
      ]
    )
  );
in
pkgs.runCommand "paw-sandbox-probe-${probeId}"
  {
    preferLocalBuild = true;
    allowSubstitutes = false;
  }
  ''
    set -eu

    test ! -e ${lib.escapeShellArg marker}
    test "$(readlink /proc/self/ns/mnt)" != ${lib.escapeShellArg mountNs}
    test "$(readlink /proc/self/ns/pid)" != ${lib.escapeShellArg pidNs}
    test "$(readlink /proc/self/ns/net)" != ${lib.escapeShellArg netNs}

    touch "$out"
  ''
