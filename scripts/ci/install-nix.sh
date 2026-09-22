#!/usr/bin/env bash
set -euo pipefail

nix_version=2.29.2
nix_system=x86_64-linux
archive_name="nix-${nix_version}-${nix_system}.tar.xz"
archive_url="https://releases.nixos.org/nix/nix-${nix_version}/${archive_name}"
archive_sha256=8132c325da027ccb158008c3b6745df7101b6883a491ecb8db5db3e281aa645b

fail() {
  printf 'install-nix: %s\n' "$1" >&2
  exit 1
}

[[ ${GITHUB_ACTIONS:-} == true ]] || fail "requires GitHub Actions"
[[ ${RUNNER_ENVIRONMENT:-} == github-hosted ]] || fail "requires a GitHub-hosted runner"
[[ ${RUNNER_OS:-} == Linux ]] || fail "requires a Linux runner"
[[ ${RUNNER_ARCH:-} == X64 ]] || fail "requires an x86_64 runner"
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail "runner platform does not match x86_64-linux"
[[ -n ${RUNNER_TEMP:-} && -d ${RUNNER_TEMP} && -w ${RUNNER_TEMP} ]] || fail "RUNNER_TEMP is not a writable directory"
[[ -n ${GITHUB_PATH:-} && -n ${GITHUB_ENV:-} ]] || fail "GitHub environment files are unavailable"
command -v nix >/dev/null 2>&1 && fail "Nix is already installed"
command -v systemctl >/dev/null 2>&1 || fail "systemctl is unavailable"
systemctl show-environment >/dev/null || fail "systemd is not running"

# The official installer does not consume GitHub credentials. Remove common
# token/config variables before both the public download and installation so a
# repository token cannot be persisted in Nix configuration.
unset GITHUB_TOKEN GH_TOKEN INPUT_GITHUB_ACCESS_TOKEN NIX_CONFIG

workdir=$(mktemp -d "${RUNNER_TEMP%/}/paw-nix-install.XXXXXXXXXX")
cleanup() {
  rm -rf -- "$workdir"
}
trap cleanup EXIT

archive="$workdir/$archive_name"
curl --disable --fail --location --silent --show-error \
  --proto '=https' --proto-redir '=https' --tlsv1.2 \
  --output "$archive" "$archive_url"

actual_sha256=$(sha256sum "$archive" | awk '{print $1}')
[[ $actual_sha256 == "$archive_sha256" ]] || fail "Nix archive SHA-256 mismatch"

extract_dir="$workdir/extract"
mkdir -p "$extract_dir"
tar -xJf "$archive" -C "$extract_dir"
installer="$extract_dir/nix-${nix_version}-${nix_system}/install"
[[ -f $installer ]] || fail "verified Nix archive did not contain its installer"

config="$workdir/nix.conf"
printf '%s\n' \
  'experimental-features = nix-command flakes' \
  'sandbox = true' \
  'sandbox-fallback = false' \
  'max-jobs = 2' \
  'cores = 2' > "$config"

sh "$installer" \
  --daemon \
  --yes \
  --no-channel-add \
  --no-modify-profile \
  --daemon-user-count 4 \
  --nix-extra-conf-file "$config"

systemctl is-active --quiet nix-daemon.socket || fail "Nix daemon socket is not active"

printf '%s\n' /nix/var/nix/profiles/default/bin >> "$GITHUB_PATH"
printf '%s\n' NIX_REMOTE=daemon >> "$GITHUB_ENV"
