{ pkgs, ... }:

{
  imports = [ ./runtime-core.nix ];

  paw.profile = {
    name = "platform-readonly";
    requiresPlatformIdentity = true;
    runtimePackages = with pkgs; [
      jq
      kubectl
      kubernetes-helm
      kustomize
      opentofu
      yq-go
    ];
  };
}
