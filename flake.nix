{
  description = "PAW — portable AI workspaces";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      contract = builtins.fromJSON (builtins.readFile ./contract/v0.json);
      profileEvaluations =
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          evalProfile =
            module:
            import ./nix/lib/eval-profile.nix {
              inherit contract module pkgs;
              inherit (pkgs) lib;
            };
        in
        {
          core = evalProfile ./nix/profiles/core.nix;
          platform-readonly = evalProfile ./nix/profiles/platform-readonly.nix;
        };
      profileMetadata =
        system: nixpkgs.lib.mapAttrs (_: value: value.metadata) (profileEvaluations system);
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          paw = pkgs.buildGoModule {
            pname = "paw";
            version = "0.0.0-dev";
            src = ./.;
            vendorHash = null;
            subPackages = [ "cmd/paw" ];
            checkPhase = ''
              runHook preCheck
              go test ./...
              runHook postCheck
            '';
            ldflags = [
              "-s"
              "-w"
              "-X git.alc.xyz/alcxyz/paw/internal/buildinfo.Version=0.0.0-dev"
              "-X git.alc.xyz/alcxyz/paw/internal/buildinfo.Commit=${
                self.shortRev or self.dirtyShortRev or "unknown"
              }"
            ];
            meta = {
              description = "Portable AI workspace operator";
              homepage = "https://git.alc.xyz/alcxyz/paw";
              mainProgram = "paw";
            };
          };
          t3code-headless = pkgs.callPackage ./nix/packages/t3code-headless.nix { };
          t3code-headless-closure-info = pkgs.closureInfo {
            rootPaths = [ t3code-headless ];
          };
        in
        {
          inherit paw;
          inherit t3code-headless;
          inherit t3code-headless-closure-info;
          default = paw;
        }
      );

      apps = forAllSystems (system: {
        paw = {
          type = "app";
          program = "${self.packages.${system}.paw}/bin/paw";
          meta = self.packages.${system}.paw.meta;
        };
        default = self.apps.${system}.paw;
      });

      pawProfiles = forAllSystems profileMetadata;

      checks = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          profilesJSON = pkgs.writeText "paw-profiles-${system}.json" (
            builtins.toJSON self.pawProfiles.${system}
          );
          t3code-headless = self.packages.${system}.t3code-headless;
          t3code-headless-closure-info = self.packages.${system}.t3code-headless-closure-info;
        in
        {
          paw = self.packages.${system}.paw;

          gofmt = pkgs.runCommand "paw-gofmt-check" { nativeBuildInputs = [ pkgs.go ]; } ''
            unformatted=$(gofmt -l ${./cmd} ${./internal})
            if [ -n "$unformatted" ]; then
              echo "The following Go files are not formatted:" >&2
              echo "$unformatted" >&2
              exit 1
            fi
            touch "$out"
          '';

          govet = pkgs.runCommand "paw-govet-check" { nativeBuildInputs = [ pkgs.go ]; } ''
            export HOME="$TMPDIR"
            cp -R ${./.} source
            chmod -R u+w source
            cd source
            go vet ./...
            touch "$out"
          '';

          markdown =
            pkgs.runCommand "paw-markdown-check"
              {
                nativeBuildInputs = [ pkgs.markdownlint-cli ];
              }
              ''
                markdownlint ${./README.md} ${./AGENTS.md} ${./docs}/adr/*.md
                touch "$out"
              '';

          nixfmt = pkgs.runCommand "paw-nixfmt-check" { nativeBuildInputs = [ pkgs.nixfmt ]; } ''
            nixfmt --check \
              ${./flake.nix} \
              ${./nix/lib/eval-profile.nix} \
              ${./nix/modules/profile.nix} \
              ${./nix/packages/t3code-headless.nix} \
              ${./nix/profiles/core.nix} \
              ${./nix/profiles/platform-readonly.nix} \
              ${./nix/profiles/runtime-core.nix}
            touch "$out"
          '';

          profile-contract =
            pkgs.runCommand "paw-profile-contract-check"
              {
                nativeBuildInputs = [ pkgs.jq ];
              }
              ''
                jq --exit-status '
                  .core.contractVersion == "v0" and
                  .core.authority.ceiling == "workspace-only" and
                  .core.authority.externalMutation == false and
                  .core.authority.productionAccess == false and
                  .core.repositories.selection == "explicit" and
                  .core.repositories.remoteGitPush == false and
                  .core.network.defaultDenyEgress == true and
                  .core.identity.platformRequired == false and
                  ."platform-readonly".authority.ceiling == "read-only" and
                  ."platform-readonly".authority.externalMutation == false and
                  ."platform-readonly".authority.productionAccess == false and
                  ."platform-readonly".repositories.remoteGitPush == false and
                  ."platform-readonly".network.defaultDenyEgress == true and
                  ."platform-readonly".identity.platformRequired == true and
                  (.core.requiredAdapters | length) == 7 and
                  (."platform-readonly".requiredAdapters | length) == 7
                ' ${profilesJSON} >/dev/null

                if grep -Fq '/nix/store/' ${profilesJSON}; then
                  echo "Profile metadata contains a Nix store path" >&2
                  exit 1
                fi

                mkdir "$out"
                cp ${profilesJSON} "$out/profiles.json"
              '';
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          inherit t3code-headless;

          t3code-headless-runtime-contract = pkgs.runCommand "t3code-headless-runtime-contract" { } ''
            closure_bytes=$(<${t3code-headless-closure-info}/total-nar-size)
            closure_budget=$((450 * 1024 * 1024))

            if [ "$closure_bytes" -gt "$closure_budget" ]; then
              echo "T3 headless closure is $closure_bytes bytes; budget is $closure_budget" >&2
              exit 1
            fi

            forbidden='-(electron|t3code-desktop|claude-code|codex|opencode|pnpm|python3)-|-nodejs-[0-9]'
            if grep -Eiq -- "$forbidden" \
              ${t3code-headless-closure-info}/store-paths; then
              echo "T3 headless closure contains a desktop, provider, or build dependency:" >&2
              grep -Ei -- "$forbidden" \
                ${t3code-headless-closure-info}/store-paths >&2
              exit 1
            fi

            test -f ${t3code-headless}/libexec/t3code/dist/client/index.html
            test -x \
              ${t3code-headless}/libexec/t3code/dist/resource-monitor/t3-resource-monitor
            test ! -e ${t3code-headless}/bin/t3code-desktop

            if find ${t3code-headless}/libexec/t3code \
              -path '*/node_modules/.pnpm/@anthropic-ai+claude-agent-sdk-*' \
              -print -quit | grep -q .; then
              echo "T3 headless contains the Agent SDK's bundled Claude executable" >&2
              exit 1
            fi

            if find ${t3code-headless}/libexec/t3code \
              -path '*/node-pty/prebuilds' -print -quit | grep -q .; then
              echo "T3 headless contains foreign node-pty prebuilds" >&2
              exit 1
            fi

            mkdir "$out"
            cp ${t3code-headless-closure-info}/store-paths "$out/store-paths"
            printf '%s\n' "$closure_bytes" > "$out/total-nar-size"
            printf '%s\n' "$closure_budget" > "$out/closure-budget"
          '';
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShellNoCC {
            packages = with pkgs; [
              go
              gopls
              gotools
              kubectl
              markdownlint-cli
              minikube
              nixfmt
            ];
          };
        }
      );

      formatter = forAllSystems (system: nixpkgs.legacyPackages.${system}.nixfmt);
    };
}
