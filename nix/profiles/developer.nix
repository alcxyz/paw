{ pkgs, ... }:

{
  imports = [ ./runtime-core.nix ];

  paw.profile = {
    name = "developer";
    # The toolchain PAW's own checks need inside a workspace: Go for build,
    # test, vet, and gofmt; shellcheck, jq, and yq for the scripts. PAW has no
    # Go module dependencies, so no module proxy or package egress is needed.
    runtimePackages = with pkgs; [
      go
      jq
      shellcheck
      yq-go
    ];
  };
}
