{
  contract,
  lib,
  module,
  pkgs,
}:

let
  evaluation = lib.evalModules {
    specialArgs = {
      inherit contract pkgs;
    };
    modules = [
      ../modules/profile.nix
      module
    ];
  };
  failedAssertions = builtins.filter (item: !item.assertion) evaluation.config.assertions;
  checkedEvaluation =
    if failedAssertions == [ ] then
      evaluation
    else
      throw ''
        PAW profile contract assertions failed:
        ${lib.concatMapStringsSep "\n" (item: "- ${item.message}") failedAssertions}
      '';
in
{
  inherit (checkedEvaluation.config.paw.profile) metadata runtimePackages;
}
