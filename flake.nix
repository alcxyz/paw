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
          paw-core-image = pkgs.callPackage ./nix/images/paw-core.nix {
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).core.runtimePackages;
          };
          paw-core-closure-info = pkgs.closureInfo {
            rootPaths = paw-core-image.runtimeContents;
          };
          paw-core-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-core-image;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-core-image.runtimeContents;
          };
        in
        {
          inherit paw;
          default = paw;
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          inherit
            paw-core-closure-info
            paw-core-image
            paw-core-image-report
            t3code-headless
            t3code-headless-closure-info
            ;
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
          paw-core-closure-info = self.packages.${system}.paw-core-closure-info;
          paw-core-image-report = self.packages.${system}.paw-core-image-report;
        in
        {
          paw = self.packages.${system}.paw;

          gofmt = pkgs.runCommand "paw-gofmt-check" { nativeBuildInputs = [ pkgs.go ]; } ''
            unformatted=$(gofmt -l ${./cmd} ${./contract} ${./deploy} ${./internal})
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
                markdownlint \
                  ${./README.md} \
                  ${./AGENTS.md} \
                  ${./deploy}/README.md \
                  ${./docs}/adr/*.md
                touch "$out"
              '';

          nixfmt = pkgs.runCommand "paw-nixfmt-check" { nativeBuildInputs = [ pkgs.nixfmt ]; } ''
            nixfmt --check \
              ${./flake.nix} \
              ${./nix/images/paw-core.nix} \
              ${./nix/images/report.nix} \
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

          kubernetes-contract =
            pkgs.runCommand "paw-kubernetes-contract-check"
              {
                nativeBuildInputs = with pkgs; [
                  jq
                  kubectl
                  yq-go
                ];
              }
              ''
                bash ${./scripts/check-kubernetes-contract.sh} ${./.}
                touch "$out"
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
        // pkgs.lib.optionalAttrs (system == "x86_64-linux") {

          paw-core-image-contract =
            pkgs.runCommand "paw-core-image-contract"
              {
                nativeBuildInputs = [ pkgs.jq ];
              }
              ''
                report=${paw-core-image-report}/report.json
                closure_budget=$((565 * 1024 * 1024))
                uncompressed_budget=$((625 * 1024 * 1024))
                compressed_budget=$((180 * 1024 * 1024))
                layer_budget=80

                closure_bytes=$(jq -r '.runtimeClosure.narBytes' "$report")
                uncompressed_bytes=$(jq -r '.image.uncompressedLayerBytes' "$report")
                compressed_bytes=$(jq -r '.image.compressedRegistryBytes' "$report")
                layer_count=$(jq -r '.image.layerCount' "$report")

                if [ "$closure_bytes" -gt "$closure_budget" ]; then
                  echo "paw-core closure exceeds its budget" >&2
                  exit 1
                fi
                if [ "$uncompressed_bytes" -gt "$uncompressed_budget" ]; then
                  echo "paw-core uncompressed image exceeds its budget" >&2
                  exit 1
                fi
                if [ "$compressed_bytes" -gt "$compressed_budget" ]; then
                  echo "paw-core registry transfer exceeds its budget" >&2
                  exit 1
                fi
                if [ "$layer_count" -gt "$layer_budget" ]; then
                  echo "paw-core layer count exceeds its budget" >&2
                  exit 1
                fi

                jq --exit-status '
                  .image.architecture == "amd64" and
                  .image.runtime.user == "65532:65532" and
                  .image.runtime.workingDirectory == "/workspace/work" and
                  .image.runtime.command[0:5] ==
                    ["--log-level", "warn", "start", "--mode", "web"]
                ' "$report" >/dev/null

                forbidden='-(electron|t3code-desktop|claude-code|codex|opencode|pnpm|python3|nix)-|-nodejs-[0-9]'
                if grep -Eiq -- "$forbidden" ${paw-core-closure-info}/store-paths; then
                  echo "paw-core contains a desktop, provider, build, or Nix runtime" >&2
                  grep -Ei -- "$forbidden" ${paw-core-closure-info}/store-paths >&2
                  exit 1
                fi

                mkdir "$out"
                cp "$report" "$out/report.json"
                jq -n \
                  --argjson closure "$closure_budget" \
                  --argjson uncompressed "$uncompressed_budget" \
                  --argjson compressed "$compressed_budget" \
                  --argjson layers "$layer_budget" \
                  '{
                    closureNarBytes: $closure,
                    uncompressedLayerBytes: $uncompressed,
                    compressedRegistryBytes: $compressed,
                    layerCount: $layers
                  }' >"$out/budgets.json"
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
              jq
              kubectl
              markdownlint-cli
              minikube
              nixfmt
              yq-go
            ];
          };
        }
      );

      formatter = forAllSystems (system: nixpkgs.legacyPackages.${system}.nixfmt);
    };
}
