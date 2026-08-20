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
        in
        {
          inherit paw;
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

      checks = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
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
            nixfmt --check ${./flake.nix}
            touch "$out"
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
