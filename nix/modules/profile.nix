{
  config,
  contract,
  lib,
  ...
}:

let
  inherit (lib)
    mkDefault
    mkOption
    types
    ;
  cfg = config.paw.profile;
  profilesByName = lib.listToAttrs (
    map (profile: lib.nameValuePair profile.name profile) contract.profiles
  );
  authoritiesByName = lib.listToAttrs (
    map (authority: lib.nameValuePair authority.name authority) contract.authorityClasses
  );
  selectedProfile = profilesByName.${cfg.name};
  selectedAuthority = authoritiesByName.${cfg.authorityCeiling};
  requiredAdapterNames = map (adapter: adapter.name) contract.requiredAdapters;
  packageName = package: package.pname or (lib.getName package);
in
{
  options.assertions = mkOption {
    type = types.listOf (
      types.submodule {
        options = {
          assertion = mkOption { type = types.bool; };
          message = mkOption { type = types.str; };
        };
      }
    );
    default = [ ];
    internal = true;
  };

  options.paw.profile = {
    name = mkOption {
      type = types.enum (builtins.attrNames profilesByName);
      description = "Name of the v0 contract profile.";
    };

    description = mkOption {
      type = types.nonEmptyStr;
      description = "User-facing profile description.";
    };

    authorityCeiling = mkOption {
      type = types.enum (builtins.attrNames authoritiesByName);
      description = "Maximum authority the workspace may receive.";
    };

    repositorySelection = mkOption {
      type = types.enum [ "explicit" ];
      description = "How repositories enter the workspace.";
    };

    remoteGitPush = mkOption {
      type = types.bool;
      description = "Whether the profile permits pushing to a remote Git repository.";
    };

    defaultDenyEgress = mkOption {
      type = types.bool;
      description = "Whether network egress starts denied.";
    };

    allowedCapabilities = mkOption {
      type = types.listOf types.nonEmptyStr;
      description = "Capabilities allowed by the selected authority ceiling.";
    };

    egressPurposes = mkOption {
      type = types.listOf types.nonEmptyStr;
      description = "Purposes an adapter may resolve to concrete destinations.";
    };

    forbiddenCapabilities = mkOption {
      type = types.listOf types.nonEmptyStr;
      description = "Capabilities explicitly prohibited by the profile.";
    };

    requiredAdapters = mkOption {
      type = types.listOf types.nonEmptyStr;
      description = "Environment capabilities required before deployment.";
    };

    providers = mkOption {
      type = types.listOf (
        types.enum [
          "claude-code"
          "codex"
          "opencode"
        ]
      );
      default = [ ];
      description = "Provider executable layers selected by this build.";
    };

    runtimePackages = mkOption {
      type = types.listOf types.package;
      default = [ ];
      description = "Packages included in this profile's runtime closure.";
    };

    requiresPlatformIdentity = mkOption {
      type = types.bool;
      default = false;
      description = "Whether the profile requires a scoped platform identity.";
    };

    metadata = mkOption {
      type = types.attrs;
      readOnly = true;
      description = "Serializable profile contract consumed by PAW and adapters.";
    };
  };

  config = {
    paw.profile = {
      description = mkDefault selectedProfile.description;
      authorityCeiling = mkDefault selectedProfile.authorityCeiling;
      repositorySelection = mkDefault selectedProfile.repositorySelection;
      remoteGitPush = mkDefault selectedProfile.remoteGitPush;
      defaultDenyEgress = mkDefault selectedProfile.defaultDenyEgress;
      allowedCapabilities = mkDefault selectedProfile.allowedCapabilities;
      egressPurposes = mkDefault selectedProfile.egressPurposes;
      forbiddenCapabilities = mkDefault selectedProfile.forbiddenCapabilities;
      requiredAdapters = mkDefault requiredAdapterNames;

      metadata = {
        schemaVersion = 1;
        contractVersion = contract.version;
        name = cfg.name;
        inherit (cfg) description;
        authority = {
          ceiling = cfg.authorityCeiling;
          inherit (selectedAuthority)
            externalMutation
            productionAccess
            rank
            ;
        };
        repositories = {
          selection = cfg.repositorySelection;
          inherit (cfg) remoteGitPush;
        };
        network = {
          inherit (cfg) defaultDenyEgress egressPurposes;
        };
        identity = {
          platformRequired = cfg.requiresPlatformIdentity;
        };
        inherit (cfg)
          allowedCapabilities
          forbiddenCapabilities
          providers
          requiredAdapters
          ;
        runtimePackages = map packageName cfg.runtimePackages;
      };
    };

    assertions = [
      {
        assertion = cfg.description == selectedProfile.description;
        message = "Profile ${cfg.name} description must match contract/v0.json";
      }
      {
        assertion = cfg.authorityCeiling == selectedProfile.authorityCeiling;
        message = "Profile ${cfg.name} authority must match contract/v0.json";
      }
      {
        assertion = cfg.repositorySelection == selectedProfile.repositorySelection;
        message = "Profile ${cfg.name} repository selection must match contract/v0.json";
      }
      {
        assertion = cfg.remoteGitPush == selectedProfile.remoteGitPush;
        message = "Profile ${cfg.name} remote Git policy must match contract/v0.json";
      }
      {
        assertion = cfg.defaultDenyEgress == selectedProfile.defaultDenyEgress;
        message = "Profile ${cfg.name} egress policy must match contract/v0.json";
      }
      {
        assertion = cfg.allowedCapabilities == selectedProfile.allowedCapabilities;
        message = "Profile ${cfg.name} allowed capabilities must match contract/v0.json";
      }
      {
        assertion = cfg.egressPurposes == selectedProfile.egressPurposes;
        message = "Profile ${cfg.name} egress purposes must match contract/v0.json";
      }
      {
        assertion = cfg.forbiddenCapabilities == selectedProfile.forbiddenCapabilities;
        message = "Profile ${cfg.name} forbidden capabilities must match contract/v0.json";
      }
      {
        assertion = cfg.requiredAdapters == requiredAdapterNames;
        message = "Profile ${cfg.name} must declare every required v0 adapter";
      }
      {
        assertion = cfg.requiresPlatformIdentity == (cfg.name == "platform-readonly");
        message = "Only platform-readonly requires platform identity in v0";
      }
    ];
  };
}
