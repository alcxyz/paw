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
      pkgsFor =
        system:
        import nixpkgs {
          inherit system;
          config.allowUnfreePredicate = package: nixpkgs.lib.getName package == "claude-code";
        };
      contract = builtins.fromJSON (builtins.readFile ./contract/v0.json);
      profileEvaluations =
        system:
        let
          pkgs = pkgsFor system;
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
          pkgs = pkgsFor system;
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
          paw-codex-image = pkgs.callPackage ./nix/images/paw-core.nix {
            imageName = "paw-codex";
            providerPackages = [ pkgs.codex ];
            providers = [ "codex" ];
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).core.runtimePackages;
          };
          paw-claude-code-image = pkgs.callPackage ./nix/images/paw-core.nix {
            imageName = "paw-claude-code";
            providerPackages = [ pkgs.claude-code ];
            providers = [ "claude-code" ];
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).core.runtimePackages;
          };
          paw-opencode-image = pkgs.callPackage ./nix/images/paw-core.nix {
            imageName = "paw-opencode";
            providerPackages = [ pkgs.opencode ];
            providers = [ "opencode" ];
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).core.runtimePackages;
          };
          paw-platform-readonly-image = pkgs.callPackage ./nix/images/paw-core.nix {
            imageName = "paw-platform-readonly";
            profileName = "platform-readonly";
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).platform-readonly.runtimePackages;
          };
          paw-platform-readonly-codex-image = pkgs.callPackage ./nix/images/paw-core.nix {
            imageName = "paw-platform-readonly-codex";
            profileName = "platform-readonly";
            providerPackages = [ pkgs.codex ];
            providers = [ "codex" ];
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).platform-readonly.runtimePackages;
          };
          paw-platform-readonly-claude-code-image = pkgs.callPackage ./nix/images/paw-core.nix {
            imageName = "paw-platform-readonly-claude-code";
            profileName = "platform-readonly";
            providerPackages = [ pkgs.claude-code ];
            providers = [ "claude-code" ];
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).platform-readonly.runtimePackages;
          };
          paw-platform-readonly-opencode-image = pkgs.callPackage ./nix/images/paw-core.nix {
            imageName = "paw-platform-readonly-opencode";
            profileName = "platform-readonly";
            providerPackages = [ pkgs.opencode ];
            providers = [ "opencode" ];
            t3codeHeadless = t3code-headless;
            runtimePackages = (profileEvaluations system).platform-readonly.runtimePackages;
          };
          paw-core-closure-info = pkgs.closureInfo {
            rootPaths = paw-core-image.runtimeContents;
          };
          paw-codex-closure-info = pkgs.closureInfo {
            rootPaths = paw-codex-image.runtimeContents;
          };
          paw-claude-code-closure-info = pkgs.closureInfo {
            rootPaths = paw-claude-code-image.runtimeContents;
          };
          paw-opencode-closure-info = pkgs.closureInfo {
            rootPaths = paw-opencode-image.runtimeContents;
          };
          paw-platform-readonly-closure-info = pkgs.closureInfo {
            rootPaths = paw-platform-readonly-image.runtimeContents;
          };
          paw-platform-readonly-codex-closure-info = pkgs.closureInfo {
            rootPaths = paw-platform-readonly-codex-image.runtimeContents;
          };
          paw-platform-readonly-claude-code-closure-info = pkgs.closureInfo {
            rootPaths = paw-platform-readonly-claude-code-image.runtimeContents;
          };
          paw-platform-readonly-opencode-closure-info = pkgs.closureInfo {
            rootPaths = paw-platform-readonly-opencode-image.runtimeContents;
          };
          paw-core-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-core-image;
            name = paw-core-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-core-image.runtimeContents;
          };
          paw-codex-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-codex-image;
            name = paw-codex-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-codex-image.runtimeContents;
          };
          paw-claude-code-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-claude-code-image;
            name = paw-claude-code-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-claude-code-image.runtimeContents;
          };
          paw-opencode-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-opencode-image;
            name = paw-opencode-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-opencode-image.runtimeContents;
          };
          paw-platform-readonly-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-platform-readonly-image;
            name = paw-platform-readonly-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-platform-readonly-image.runtimeContents;
          };
          paw-platform-readonly-codex-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-platform-readonly-codex-image;
            name = paw-platform-readonly-codex-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-platform-readonly-codex-image.runtimeContents;
          };
          paw-platform-readonly-claude-code-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-platform-readonly-claude-code-image;
            name = paw-platform-readonly-claude-code-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-platform-readonly-claude-code-image.runtimeContents;
          };
          paw-platform-readonly-opencode-image-report = pkgs.callPackage ./nix/images/report.nix {
            image = paw-platform-readonly-opencode-image;
            name = paw-platform-readonly-opencode-image.imageName;
            reportScript = ./scripts/report-image.py;
            runtimeContents = paw-platform-readonly-opencode-image.runtimeContents;
          };
        in
        {
          inherit paw;
          default = paw;
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          inherit
            paw-claude-code-closure-info
            paw-claude-code-image
            paw-claude-code-image-report
            paw-codex-closure-info
            paw-codex-image
            paw-codex-image-report
            paw-core-closure-info
            paw-core-image
            paw-core-image-report
            paw-opencode-closure-info
            paw-opencode-image
            paw-opencode-image-report
            paw-platform-readonly-closure-info
            paw-platform-readonly-claude-code-closure-info
            paw-platform-readonly-claude-code-image
            paw-platform-readonly-claude-code-image-report
            paw-platform-readonly-codex-closure-info
            paw-platform-readonly-codex-image
            paw-platform-readonly-codex-image-report
            paw-platform-readonly-image
            paw-platform-readonly-image-report
            paw-platform-readonly-opencode-closure-info
            paw-platform-readonly-opencode-image
            paw-platform-readonly-opencode-image-report
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
          pkgs = pkgsFor system;
          profilesJSON = pkgs.writeText "paw-profiles-${system}.json" (
            builtins.toJSON self.pawProfiles.${system}
          );
          t3code-headless = self.packages.${system}.t3code-headless;
          t3code-headless-closure-info = self.packages.${system}.t3code-headless-closure-info;
          paw-core-closure-info = self.packages.${system}.paw-core-closure-info;
          paw-core-image-report = self.packages.${system}.paw-core-image-report;
          paw-codex-closure-info = self.packages.${system}.paw-codex-closure-info;
          paw-codex-image-report = self.packages.${system}.paw-codex-image-report;
          paw-claude-code-closure-info = self.packages.${system}.paw-claude-code-closure-info;
          paw-claude-code-image-report = self.packages.${system}.paw-claude-code-image-report;
          paw-opencode-closure-info = self.packages.${system}.paw-opencode-closure-info;
          paw-opencode-image-report = self.packages.${system}.paw-opencode-image-report;
          paw-platform-readonly-closure-info = self.packages.${system}.paw-platform-readonly-closure-info;
          paw-platform-readonly-image-report = self.packages.${system}.paw-platform-readonly-image-report;
          paw-platform-readonly-codex-closure-info =
            self.packages.${system}.paw-platform-readonly-codex-closure-info;
          paw-platform-readonly-codex-image-report =
            self.packages.${system}.paw-platform-readonly-codex-image-report;
          paw-platform-readonly-claude-code-closure-info =
            self.packages.${system}.paw-platform-readonly-claude-code-closure-info;
          paw-platform-readonly-claude-code-image-report =
            self.packages.${system}.paw-platform-readonly-claude-code-image-report;
          paw-platform-readonly-opencode-closure-info =
            self.packages.${system}.paw-platform-readonly-opencode-closure-info;
          paw-platform-readonly-opencode-image-report =
            self.packages.${system}.paw-platform-readonly-opencode-image-report;
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
                  (."platform-readonly".runtimePackages | index("opentofu")) != null and
                  (."platform-readonly".runtimePackages | index("kubectl")) != null and
                  (."platform-readonly".runtimePackages | index("kubernetes-helm")) != null and
                  (."platform-readonly".runtimePackages | index("kustomize")) != null and
                  (."platform-readonly".runtimePackages | index("jq")) != null and
                  (."platform-readonly".runtimePackages | index("yq-go")) != null and
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
                bash ${./scripts/check-kubernetes-contract.sh} \
                  ${./.} \
                  ${self.packages.${system}.paw}/bin/paw
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
                  .image.runtime.labels["paw.alc.xyz/providers"] == "" and
                  .image.runtime.labels["paw.alc.xyz/provider-packages"] == "" and
                  (.image.runtime.environment | map(split("=")[0]) | sort) ==
                    ["HOME", "PATH", "TMPDIR", "XDG_CACHE_HOME",
                      "XDG_CONFIG_HOME", "XDG_DATA_HOME"] and
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

          provider-image-contract =
            pkgs.runCommand "paw-provider-image-contract"
              {
                nativeBuildInputs = [ pkgs.jq ];
              }
              ''
                export HOME="$TMPDIR/home"
                export XDG_CACHE_HOME="$TMPDIR/cache"
                export XDG_CONFIG_HOME="$TMPDIR/config"
                export XDG_DATA_HOME="$TMPDIR/data"
                mkdir -p "$HOME" "$XDG_CACHE_HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME"

                check_image() {
                  name=$1
                  provider=$2
                  package_pattern=$3
                  report=$4
                  closure_info=$5
                  closure_budget=$6
                  uncompressed_budget=$7
                  compressed_budget=$8

                  closure_bytes=$(jq -r '.runtimeClosure.narBytes' "$report")
                  uncompressed_bytes=$(jq -r '.image.uncompressedLayerBytes' "$report")
                  compressed_bytes=$(jq -r '.image.compressedRegistryBytes' "$report")
                  layer_count=$(jq -r '.image.layerCount' "$report")

                  if [ "$closure_bytes" -gt "$closure_budget" ]; then
                    echo "$name closure exceeds its budget" >&2
                    exit 1
                  fi
                  if [ "$uncompressed_bytes" -gt "$uncompressed_budget" ]; then
                    echo "$name uncompressed image exceeds its budget" >&2
                    exit 1
                  fi
                  if [ "$compressed_bytes" -gt "$compressed_budget" ]; then
                    echo "$name registry transfer exceeds its budget" >&2
                    exit 1
                  fi
                  if [ "$layer_count" -gt 80 ]; then
                    echo "$name layer count exceeds its budget" >&2
                    exit 1
                  fi

                  jq --exit-status \
                    --arg name "$name" \
                    --arg provider "$provider" '
                      .image.name == $name and
                      .image.architecture == "amd64" and
                      .image.runtime.user == "65532:65532" and
                      .image.runtime.workingDirectory == "/workspace/work" and
                      .image.runtime.labels["paw.alc.xyz/profile"] == "core" and
                      .image.runtime.labels["paw.alc.xyz/providers"] == $provider and
                      (.image.runtime.labels["paw.alc.xyz/provider-packages"] | length) > 0 and
                      (.image.runtime.environment | map(split("=")[0]) | sort) ==
                        ["HOME", "PATH", "TMPDIR", "XDG_CACHE_HOME",
                          "XDG_CONFIG_HOME", "XDG_DATA_HOME"] and
                      .image.runtime.command[0:5] ==
                        ["--log-level", "warn", "start", "--mode", "web"]
                    ' "$report" >/dev/null

                  if ! grep -Eq -- "$package_pattern" "$closure_info/store-paths"; then
                    echo "$name does not contain its declared provider package" >&2
                    exit 1
                  fi

                  forbidden='-(electron|t3code-desktop|pnpm|python3|nix)-|-nodejs-[0-9]'
                  case "$provider" in
                    codex) forbidden="$forbidden|-claude-code-|-opencode-" ;;
                    claude-code) forbidden="$forbidden|-codex-|-opencode-" ;;
                    opencode) forbidden="$forbidden|-claude-code-|-codex-" ;;
                  esac
                  if grep -Eiq -- "$forbidden" "$closure_info/store-paths"; then
                    echo "$name contains a desktop, another provider, build, or Nix runtime" >&2
                    grep -Ei -- "$forbidden" "$closure_info/store-paths" >&2
                    exit 1
                  fi

                  mkdir -p "$out/$name"
                  cp "$report" "$out/$name/report.json"
                  jq -n \
                    --argjson closure "$closure_budget" \
                    --argjson uncompressed "$uncompressed_budget" \
                    --argjson compressed "$compressed_budget" \
                    '{
                      closureNarBytes: $closure,
                      uncompressedLayerBytes: $uncompressed,
                      compressedRegistryBytes: $compressed,
                      layerCount: 80
                    }' >"$out/$name/budgets.json"
                }

                ${pkgs.codex}/bin/codex --version >/dev/null
                ${pkgs.claude-code}/bin/claude --version >/dev/null
                ${pkgs.opencode}/bin/opencode --version >/dev/null

                check_image \
                  paw-codex codex '-codex-' \
                  ${paw-codex-image-report}/report.json \
                  ${paw-codex-closure-info} \
                  $((1080 * 1024 * 1024)) \
                  $((1150 * 1024 * 1024)) \
                  $((365 * 1024 * 1024))
                check_image \
                  paw-claude-code claude-code '-claude-code-' \
                  ${paw-claude-code-image-report}/report.json \
                  ${paw-claude-code-closure-info} \
                  $((920 * 1024 * 1024)) \
                  $((990 * 1024 * 1024)) \
                  $((295 * 1024 * 1024))
                check_image \
                  paw-opencode opencode '-opencode-' \
                  ${paw-opencode-image-report}/report.json \
                  ${paw-opencode-closure-info} \
                  $((775 * 1024 * 1024)) \
                  $((840 * 1024 * 1024)) \
                  $((255 * 1024 * 1024))
              '';

          platform-readonly-image-contract =
            pkgs.runCommand "paw-platform-readonly-image-contract"
              {
                nativeBuildInputs = [ pkgs.jq ];
              }
              ''
                report=${paw-platform-readonly-image-report}/report.json
                closure_budget=$((875 * 1024 * 1024))
                uncompressed_budget=$((940 * 1024 * 1024))
                compressed_budget=$((285 * 1024 * 1024))
                layer_budget=80

                closure_bytes=$(jq -r '.runtimeClosure.narBytes' "$report")
                uncompressed_bytes=$(jq -r '.image.uncompressedLayerBytes' "$report")
                compressed_bytes=$(jq -r '.image.compressedRegistryBytes' "$report")
                layer_count=$(jq -r '.image.layerCount' "$report")

                if [ "$closure_bytes" -gt "$closure_budget" ]; then
                  echo "platform-readonly closure exceeds its budget" >&2
                  exit 1
                fi
                if [ "$uncompressed_bytes" -gt "$uncompressed_budget" ]; then
                  echo "platform-readonly uncompressed image exceeds its budget" >&2
                  exit 1
                fi
                if [ "$compressed_bytes" -gt "$compressed_budget" ]; then
                  echo "platform-readonly registry transfer exceeds its budget" >&2
                  exit 1
                fi
                if [ "$layer_count" -gt "$layer_budget" ]; then
                  echo "platform-readonly layer count exceeds its budget" >&2
                  exit 1
                fi

                jq --exit-status '
                  .image.name == "paw-platform-readonly" and
                  .image.architecture == "amd64" and
                  .image.runtime.user == "65532:65532" and
                  .image.runtime.workingDirectory == "/workspace/work" and
                  .image.runtime.labels["paw.alc.xyz/profile"] ==
                    "platform-readonly" and
                  .image.runtime.labels["paw.alc.xyz/providers"] == "" and
                  (.image.runtime.environment | map(split("=")[0]) | sort) ==
                    ["HOME", "PATH", "TMPDIR", "XDG_CACHE_HOME",
                      "XDG_CONFIG_HOME", "XDG_DATA_HOME"] and
                  .image.runtime.command[0:5] ==
                    ["--log-level", "warn", "start", "--mode", "web"]
                ' "$report" >/dev/null

                for package_pattern in \
                  '-jq-' \
                  '-kubectl-' \
                  '-kubernetes-helm-' \
                  '-kustomize-' \
                  '-opentofu-' \
                  '-yq-go-'; do
                  if ! grep -Eq -- "$package_pattern" \
                    ${paw-platform-readonly-closure-info}/store-paths; then
                    echo "platform-readonly lacks $package_pattern" >&2
                    exit 1
                  fi
                done

                forbidden='-(electron|t3code-desktop|claude-code|codex|opencode|pnpm|python3|nix|azure-cli|awscli2|google-cloud-sdk)-|-nodejs-[0-9]'
                if grep -Eiq -- "$forbidden" \
                  ${paw-platform-readonly-closure-info}/store-paths; then
                  echo "platform-readonly contains a provider, cloud-specific, build, desktop, or Nix runtime" >&2
                  grep -Ei -- "$forbidden" \
                    ${paw-platform-readonly-closure-info}/store-paths >&2
                  exit 1
                fi

                export HOME="$TMPDIR/home"
                mkdir -p "$HOME"
                ${pkgs.jq}/bin/jq --version >/dev/null
                ${pkgs.kubectl}/bin/kubectl version --client >/dev/null
                ${pkgs.kubernetes-helm}/bin/helm version --short >/dev/null
                ${pkgs.kustomize}/bin/kustomize version >/dev/null
                ${pkgs.opentofu}/bin/tofu version >/dev/null
                ${pkgs.yq-go}/bin/yq --version >/dev/null

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

          platform-provider-image-contract =
            pkgs.runCommand "paw-platform-provider-image-contract"
              {
                nativeBuildInputs = [ pkgs.jq ];
              }
              ''
                check_image() {
                  name=$1
                  provider=$2
                  package_pattern=$3
                  report=$4
                  closure_info=$5
                  closure_budget=$6
                  uncompressed_budget=$7
                  compressed_budget=$8

                  closure_bytes=$(jq -r '.runtimeClosure.narBytes' "$report")
                  uncompressed_bytes=$(jq -r '.image.uncompressedLayerBytes' "$report")
                  compressed_bytes=$(jq -r '.image.compressedRegistryBytes' "$report")
                  layer_count=$(jq -r '.image.layerCount' "$report")

                  if [ "$closure_bytes" -gt "$closure_budget" ]; then
                    echo "$name closure exceeds its budget" >&2
                    exit 1
                  fi
                  if [ "$uncompressed_bytes" -gt "$uncompressed_budget" ]; then
                    echo "$name uncompressed image exceeds its budget" >&2
                    exit 1
                  fi
                  if [ "$compressed_bytes" -gt "$compressed_budget" ]; then
                    echo "$name registry transfer exceeds its budget" >&2
                    exit 1
                  fi
                  if [ "$layer_count" -gt 80 ]; then
                    echo "$name layer count exceeds its budget" >&2
                    exit 1
                  fi

                  jq --exit-status \
                    --arg name "$name" \
                    --arg provider "$provider" '
                      .image.name == $name and
                      .image.architecture == "amd64" and
                      .image.runtime.user == "65532:65532" and
                      .image.runtime.workingDirectory == "/workspace/work" and
                      .image.runtime.labels["paw.alc.xyz/profile"] ==
                        "platform-readonly" and
                      .image.runtime.labels["paw.alc.xyz/providers"] == $provider and
                      (.image.runtime.labels["paw.alc.xyz/provider-packages"] | length) > 0 and
                      (.image.runtime.environment | map(split("=")[0]) | sort) ==
                        ["HOME", "PATH", "TMPDIR", "XDG_CACHE_HOME",
                          "XDG_CONFIG_HOME", "XDG_DATA_HOME"] and
                      .image.runtime.command[0:5] ==
                        ["--log-level", "warn", "start", "--mode", "web"]
                    ' "$report" >/dev/null

                  for required_pattern in \
                    "$package_pattern" \
                    '-jq-' \
                    '-kubectl-' \
                    '-kubernetes-helm-' \
                    '-kustomize-' \
                    '-opentofu-' \
                    '-yq-go-'; do
                    if ! grep -Eq -- "$required_pattern" "$closure_info/store-paths"; then
                      echo "$name lacks $required_pattern" >&2
                      exit 1
                    fi
                  done

                  forbidden='-(electron|t3code-desktop|pnpm|python3|nix|azure-cli|awscli2|google-cloud-sdk)-|-nodejs-[0-9]'
                  case "$provider" in
                    codex) forbidden="$forbidden|-claude-code-|-opencode-" ;;
                    claude-code) forbidden="$forbidden|-codex-|-opencode-" ;;
                    opencode) forbidden="$forbidden|-claude-code-|-codex-" ;;
                  esac
                  if grep -Eiq -- "$forbidden" "$closure_info/store-paths"; then
                    echo "$name contains another provider, cloud-specific, build, desktop, or Nix runtime" >&2
                    grep -Ei -- "$forbidden" "$closure_info/store-paths" >&2
                    exit 1
                  fi

                  mkdir -p "$out/$name"
                  cp "$report" "$out/$name/report.json"
                  jq -n \
                    --argjson closure "$closure_budget" \
                    --argjson uncompressed "$uncompressed_budget" \
                    --argjson compressed "$compressed_budget" \
                    '{
                      closureNarBytes: $closure,
                      uncompressedLayerBytes: $uncompressed,
                      compressedRegistryBytes: $compressed,
                      layerCount: 80
                    }' >"$out/$name/budgets.json"
                }

                check_image \
                  paw-platform-readonly-codex codex '-codex-' \
                  ${paw-platform-readonly-codex-image-report}/report.json \
                  ${paw-platform-readonly-codex-closure-info} \
                  $((1390 * 1024 * 1024)) \
                  $((1460 * 1024 * 1024)) \
                  $((470 * 1024 * 1024))
                check_image \
                  paw-platform-readonly-claude-code claude-code '-claude-code-' \
                  ${paw-platform-readonly-claude-code-image-report}/report.json \
                  ${paw-platform-readonly-claude-code-closure-info} \
                  $((1230 * 1024 * 1024)) \
                  $((1300 * 1024 * 1024)) \
                  $((400 * 1024 * 1024))
                check_image \
                  paw-platform-readonly-opencode opencode '-opencode-' \
                  ${paw-platform-readonly-opencode-image-report}/report.json \
                  ${paw-platform-readonly-opencode-closure-info} \
                  $((1085 * 1024 * 1024)) \
                  $((1150 * 1024 * 1024)) \
                  $((360 * 1024 * 1024))
              '';
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
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

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);
    };
}
