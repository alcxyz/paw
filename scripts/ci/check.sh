#!/usr/bin/env bash
set -euo pipefail

# No live-cluster or authenticated provider operations belong in build CI.
if [[ $(uname -s) == Linux ]]; then
  bash scripts/ci/check-sandbox.sh
fi
# The dependency-automation flake check runs the Python suite with pinned Python.
nix flake check --no-update-lock-file

# Evaluate the second Linux architecture; native ARM builds remain a release
# qualification task, not something an amd64 evaluation can prove.
nix eval --raw .#packages.aarch64-linux.t3code-headless.drvPath >/dev/null
nix eval --raw .#packages.aarch64-linux.paw-codex-image.drvPath >/dev/null
