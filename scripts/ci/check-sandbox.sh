#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != Linux ]]; then
  echo "sandbox preflight requires Linux" >&2
  exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
source_root=${PAW_SOURCE_ROOT:-$(cd -- "$script_dir/../.." && pwd -P)}
probe_expression=$script_dir/sandbox-probe.nix

test -f "$source_root/flake.nix"
test -f "$probe_expression"

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/paw-sandbox-preflight.XXXXXXXX")
chmod 700 "$tmp_dir"
marker=$tmp_dir/marker
printf '%s\n' 'paw-sandbox-preflight-marker' >"$marker"
chmod 600 "$marker"

cleanup() {
  rm -f -- "$marker"
  rmdir -- "$tmp_dir"
}
trap cleanup EXIT

mount_ns=$(readlink /proc/self/ns/mnt)
pid_ns=$(readlink /proc/self/ns/pid)
net_ns=$(readlink /proc/self/ns/net)

status=0
nix-build \
  --no-out-link \
  --option sandbox true \
  --option sandbox-fallback false \
  --option builders "" \
  --argstr sourceRoot "$source_root" \
  --argstr marker "$marker" \
  --argstr mountNs "$mount_ns" \
  --argstr pidNs "$pid_ns" \
  --argstr netNs "$net_ns" \
  "$probe_expression" || status=$?

exit "$status"
