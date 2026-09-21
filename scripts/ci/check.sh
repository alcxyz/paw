#!/usr/bin/env bash
set -euo pipefail

# No live-cluster or authenticated provider operations belong in build CI.
python3 -m unittest discover -s tests -p 'test_*.py'
nix flake check --no-update-lock-file

# Evaluate the second Linux architecture; native ARM builds remain a release
# qualification task, not something an amd64 evaluation can prove.
nix eval --raw .#packages.aarch64-linux.t3code-headless.drvPath >/dev/null
nix eval --raw .#packages.aarch64-linux.paw-codex-image.drvPath >/dev/null
