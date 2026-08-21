{ pkgs, ... }:

{
  imports = [ ./runtime-core.nix ];

  paw.profile = {
    name = "platform-readonly";
    requiresPlatformIdentity = true;
    runtimePackages = [ pkgs.jq ];
  };
}
