#!/usr/bin/env bash

set -euo pipefail
shopt -s nullglob

namespace="paw-workspace"
workspace_created=false
workspace_uid=""
temporary_dir=""

fail() {
  printf 'live backup/restore: %s\n' "$1" >&2
  exit 1
}

namespace_uid() {
  kubectl --context "$context" get namespace "$namespace" \
    --ignore-not-found --output='jsonpath={.metadata.uid}'
}

destroy_owned_workspace() {
  local current_uid
  current_uid="$(namespace_uid)" || return 1
  if [[ -z "$current_uid" ]]; then
    workspace_created=false
    return 0
  fi
  [[ "$current_uid" == "$workspace_uid" ]] || return 1
  "$paw_binary" workspace destroy --adapter minikube --context "$context" \
    --delete-state >/dev/null 2>&1 || return 1
  current_uid="$(namespace_uid)" || return 1
  [[ -z "$current_uid" ]] || return 1
  workspace_created=false
}

cleanup() {
  if [[ -n "$temporary_dir" ]]; then
    rm -rf -- "$temporary_dir"
  fi
  if [[ "$workspace_created" == true ]] && ! destroy_owned_workspace; then
    printf 'live backup/restore: automatic cleanup failed; inspect the owned test namespace\n' >&2
    return 1
  fi
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ "${PAW_LIVE_TEST:-}" != "1" ]]; then
  fail "set PAW_LIVE_TEST=1 to acknowledge creation and deletion of disposable workspaces"
fi
if [[ $# -ne 3 ]]; then
  fail "usage: PAW_LIVE_TEST=1 scripts/check-backup-live.sh CONTEXT PAW_BINARY ARTIFACT_DIR"
fi

context="$1"
paw_binary="$2"
artifact_dir="$3"
[[ -n "$context" ]] || fail "CONTEXT must not be empty"
[[ "$paw_binary" = /* && -x "$paw_binary" ]] || fail "PAW_BINARY must be an absolute executable file"
[[ "$artifact_dir" = /* ]] || fail "ARTIFACT_DIR must be an absolute path"

for dependency in jq kubectl yq; do
  command -v "$dependency" >/dev/null || fail "$dependency is required"
done
yq --version 2>/dev/null | grep -q 'mikefarah/yq/) version v4' ||
  fail "yq must be Mike Farah yq v4; the Python jq wrapper is not supported"

[[ ! -e "$artifact_dir" && ! -L "$artifact_dir" ]] || fail "ARTIFACT_DIR must be a new path"
mkdir -- "$artifact_dir"
chmod 700 -- "$artifact_dir"
backup_dir="$artifact_dir/backup"
if [[ -e "$backup_dir" ]]; then
  fail "backup output directory already exists; refusing to overwrite artifacts"
fi

kubectl --context "$context" get nodes >/dev/null 2>&1 || fail "cannot access Kubernetes context"
existing_uid="$(namespace_uid)" || fail "cannot establish whether the test namespace exists"
[[ -z "$existing_uid" ]] || fail "namespace $namespace already exists; refusing to alter it"

"$paw_binary" workspace create --adapter minikube --context "$context" \
  --profile core --provider none >/dev/null 2>&1 ||
  fail "workspace creation failed; any partial workspace is retained for inspection"
workspace_uid="$(namespace_uid)" || fail "cannot establish ownership of the created workspace"
[[ -n "$workspace_uid" ]] || fail "created workspace namespace is missing"
workspace_created=true
kubectl --context "$context" --namespace "$namespace" rollout status \
  statefulset/workspace --timeout=240s >/dev/null 2>&1 || fail "source workspace did not become ready"

# Keep all fixture data harmless and local to the disposable workspace.
# The single-quoted scripts expand only inside the workspace shell.
# shellcheck disable=SC2016
kubectl --context "$context" --namespace "$namespace" exec workspace-0 -- sh -ceu '
  mkdir -p /workspace/work/backup-fixture
  printf "state-preserved\\n" > /workspace/state/.paw-backup-state
  printf "\\000\\001PAW\\377\\n" > /workspace/state/.paw-backup-binary
  ln -s tracked.txt /workspace/work/backup-fixture/tracked-link
  cd /workspace/work/backup-fixture
  git init -q
  git config user.email paw-live@example.invalid
  git config user.name paw-live
  printf "tracked-before\\n" > tracked.txt
  git add tracked.txt
  git commit -qm initial
  printf "tracked-after\\n" >> tracked.txt
  printf "staged\\n" > staged.txt
  git add staged.txt
  printf "untracked\\n" > untracked.txt
  mkdir -p .git/info
  printf "ignored.txt\\n" > .git/info/exclude
  printf "ignored\\n" > ignored.txt
' >/dev/null 2>&1 || fail "could not create source fixtures"

"$paw_binary" workspace backup --adapter minikube --context "$context" \
  --output "$backup_dir" >/dev/null || fail "workspace backup failed"
printf 'live backup/restore: backup verified and source resumed\n'

kubectl --context "$context" --namespace "$namespace" rollout status \
  statefulset/workspace --timeout=240s >/dev/null 2>&1 || fail "source workspace did not resume after backup"
# The single-quoted script expands only inside the restored workspace shell.
# shellcheck disable=SC2016
kubectl --context "$context" --namespace "$namespace" exec workspace-0 -- sh -ceu '
  test "$(cat /workspace/state/.paw-backup-state)" = state-preserved
  test "$(wc -c < /workspace/state/.paw-backup-binary)" = 7
  test "$(readlink /workspace/work/backup-fixture/tracked-link)" = tracked.txt
  cd /workspace/work/backup-fixture
  test "$(tail -n 1 tracked.txt)" = tracked-after
  test "$(cat staged.txt)" = staged
  git diff --cached --name-only -- staged.txt | grep -qx staged.txt
  test "$(cat untracked.txt)" = untracked
  test "$(cat ignored.txt)" = ignored
  git check-ignore -q ignored.txt
' >/dev/null 2>&1 || fail "source fixtures changed after backup"

backup_entries=("$backup_dir"/*)
(( ${#backup_entries[@]} > 0 )) || fail "backup produced no artifacts"
destroy_owned_workspace || fail "source workspace cleanup could not be verified"

temporary_dir="$(mktemp -d)"
rendered="$temporary_dir/rendered.yaml"
scaled="$temporary_dir/scaled.yaml"
namespace_manifest="$temporary_dir/namespace.yaml"
objects_manifest="$temporary_dir/objects.yaml"
"$paw_binary" workspace render --adapter minikube --profile core --provider none \
  >"$rendered" 2>/dev/null || fail "could not render canonical restore manifests"
yq eval '(select(.kind == "StatefulSet" and .metadata.name == "workspace") | .spec.replicas) = 0' \
  "$rendered" >"$scaled" || fail "could not scale restore manifest"
yq eval 'select(.kind == "Namespace")' "$scaled" >"$namespace_manifest" || fail "could not isolate namespace manifest"
yq eval 'select(.kind != "Namespace")' "$scaled" >"$objects_manifest" || fail "could not isolate workload manifests"
[[ -s "$namespace_manifest" && -s "$objects_manifest" ]] || fail "rendered manifests are incomplete"

kubectl --context "$context" create -f "$namespace_manifest" >/dev/null 2>&1 ||
  fail "could not reserve restore namespace"
workspace_uid="$(namespace_uid)" || fail "could not establish restore namespace ownership"
[[ -n "$workspace_uid" ]] || fail "restore namespace is missing"
workspace_created=true
kubectl --context "$context" apply -f "$objects_manifest" >/dev/null 2>&1 ||
  fail "could not apply empty restore workload"
replicas="$(kubectl --context "$context" --namespace "$namespace" get statefulset/workspace -o jsonpath='{.spec.replicas}')"
[[ "$replicas" == 0 ]] || fail "restore workload was not stopped before extraction"
kubectl --context "$context" --namespace "$namespace" rollout status \
  statefulset/workspace --timeout=120s >/dev/null 2>&1 || fail "stopped restore workload was not observed"
if kubectl --context "$context" --namespace "$namespace" get pod workspace-0 >/dev/null 2>&1; then
  fail "restore workload unexpectedly started a T3 pod"
fi
state_uid="$(kubectl --context "$context" --namespace "$namespace" get pvc workspace-state -o jsonpath='{.metadata.uid}')"
work_uid="$(kubectl --context "$context" --namespace "$namespace" get pvc workspace-work -o jsonpath='{.metadata.uid}')"
[[ -n "$state_uid" && -n "$work_uid" ]] || fail "restore claims are missing"

"$paw_binary" workspace restore --adapter minikube --context "$context" \
  --input "$backup_dir" --confirm-empty-restore >/dev/null || fail "workspace restore failed"
printf 'live backup/restore: restored into fresh stopped claims\n'
replicas="$(kubectl --context "$context" --namespace "$namespace" get statefulset/workspace -o jsonpath='{.spec.replicas}')"
[[ "$replicas" == 0 ]] || fail "restore started the writer unexpectedly"
if kubectl --context "$context" --namespace "$namespace" get pod workspace-0 >/dev/null 2>&1; then
  fail "restore left a T3 pod running"
fi
[[ "$(kubectl --context "$context" --namespace "$namespace" get pvc workspace-state -o jsonpath='{.metadata.uid}')" == "$state_uid" ]] || fail "state claim changed during restore"
[[ "$(kubectl --context "$context" --namespace "$namespace" get pvc workspace-work -o jsonpath='{.metadata.uid}')" == "$work_uid" ]] || fail "work claim changed during restore"

kubectl --context "$context" --namespace "$namespace" scale statefulset/workspace --replicas=1 >/dev/null 2>&1 || fail "could not start restored writer"
kubectl --context "$context" --namespace "$namespace" rollout status statefulset/workspace \
  --timeout=240s >/dev/null 2>&1 || fail "restored workspace did not become ready"
# The single-quoted script expands only inside the restored workspace shell.
# shellcheck disable=SC2016
kubectl --context "$context" --namespace "$namespace" exec workspace-0 -- sh -ceu '
  test "$(cat /workspace/state/.paw-backup-state)" = state-preserved
  test "$(wc -c < /workspace/state/.paw-backup-binary)" = 7
  test "$(readlink /workspace/work/backup-fixture/tracked-link)" = tracked.txt
  cd /workspace/work/backup-fixture
  test "$(tail -n 1 tracked.txt)" = tracked-after
  test "$(cat staged.txt)" = staged
  git diff --cached --name-only -- staged.txt | grep -qx staged.txt
  test "$(cat untracked.txt)" = untracked
  test "$(cat ignored.txt)" = ignored
  git check-ignore -q ignored.txt
' >/dev/null 2>&1 || fail "restored fixtures are incomplete"

destroy_owned_workspace || fail "restored workspace cleanup could not be verified"
printf 'live backup/restore passed for context %s\n' "$context"
