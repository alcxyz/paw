#!/usr/bin/env bash

set -euo pipefail

namespace="paw-workspace"
port_forward_pid=""
workspace_created=false
workspace_uid=""

fail() {
  printf 'live conformance: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  if [[ -n "$port_forward_pid" ]]; then
    kill "$port_forward_pid" 2>/dev/null || true
    wait "$port_forward_pid" 2>/dev/null || true
  fi
  if [[ "$workspace_created" == true ]]; then
    if ! destroy_owned_workspace; then
      printf 'live conformance: automatic cleanup failed; inspect the test workspace before removing it\n' >&2
      return 1
    fi
  fi
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
  if [[ "$current_uid" != "$workspace_uid" ]]; then
    printf 'live conformance: namespace ownership changed; refusing cleanup\n' >&2
    return 1
  fi
  "$paw_binary" workspace destroy \
    --adapter minikube \
    --context "$context" \
    --delete-state >/dev/null || return 1
  current_uid="$(namespace_uid)" || return 1
  [[ -z "$current_uid" ]] || return 1
  workspace_created=false
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ "${PAW_LIVE_TEST:-}" != "1" ]]; then
  fail "set PAW_LIVE_TEST=1 to acknowledge creation and deletion of an ephemeral test workspace"
fi
if [[ $# -lt 2 || $# -gt 3 ]]; then
  fail "usage: PAW_LIVE_TEST=1 scripts/check-minikube-live.sh CONTEXT PAW_BINARY [LOCAL_PORT]"
fi

context="$1"
paw_binary="$2"
local_port="${3:-43773}"
egress_probe_host="${PAW_EGRESS_PROBE_HOST:-1.1.1.1}"
egress_probe_port="${PAW_EGRESS_PROBE_PORT:-443}"

[[ -n "$context" ]] || fail "CONTEXT must not be empty"
[[ -x "$paw_binary" ]] || fail "PAW_BINARY must be an executable file"
[[ "$local_port" =~ ^[0-9]+$ ]] || fail "LOCAL_PORT must be numeric"
((local_port >= 1024 && local_port <= 65535)) || fail "LOCAL_PORT must be between 1024 and 65535"
[[ "$egress_probe_host" =~ ^[A-Za-z0-9.-]+$ ]] || fail "PAW_EGRESS_PROBE_HOST contains unsupported characters"
[[ "$egress_probe_port" =~ ^[0-9]+$ ]] || fail "PAW_EGRESS_PROBE_PORT must be numeric"
((egress_probe_port >= 1 && egress_probe_port <= 65535)) || fail "PAW_EGRESS_PROBE_PORT must be between 1 and 65535"

for dependency in curl git jq kubectl; do
  command -v "$dependency" >/dev/null || fail "$dependency is required"
done

kubectl --context "$context" get nodes >/dev/null
existing_uid="$(namespace_uid)" || fail "cannot establish whether the test namespace exists"
if [[ -n "$existing_uid" ]]; then
  fail "namespace $namespace already exists; refusing to alter a workspace not created by this test"
fi

if ! "$paw_binary" workspace create \
  --adapter minikube \
  --context "$context" \
  --profile core \
  --provider none >/dev/null; then
  fail "workspace creation failed; any partially created workspace is retained for inspection"
fi
workspace_uid="$(namespace_uid)" || fail "cannot establish ownership of the created workspace; inspect it manually"
[[ -n "$workspace_uid" ]] || fail "created workspace namespace is missing"
workspace_created=true

kubectl --context "$context" --namespace "$namespace" rollout status \
  statefulset/workspace --timeout=240s >/dev/null

kubectl --context "$context" --namespace "$namespace" apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: egress-positive-control
  labels:
    app.kubernetes.io/name: paw-live-conformance
spec:
  automountServiceAccountToken: false
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: probe
      image: paw-core:dev
      imagePullPolicy: Never
      command: ["/bin/sh", "-c", "sleep 600"]
      securityContext:
        allowPrivilegeEscalation: false
        privileged: false
        readOnlyRootFilesystem: true
        capabilities:
          drop: ["ALL"]
      resources:
        requests:
          cpu: 5m
          memory: 8Mi
        limits:
          cpu: 100m
          memory: 32Mi
EOF

kubectl --context "$context" --namespace "$namespace" wait \
  --for=condition=Ready pod/egress-positive-control --timeout=120s >/dev/null

"$paw_binary" workspace inspect \
  --adapter minikube \
  --context "$context" \
  --json |
  jq --exit-status '
    (.items | length) == 4 and
    ([.items[].kind] | sort) ==
      ["PersistentVolumeClaim", "Pod", "Service", "StatefulSet"]
  ' >/dev/null

kubectl --context "$context" --namespace "$namespace" get pod workspace-0 \
  --output json |
  jq --exit-status '
    [.spec.containers[]?, .spec.initContainers[]?,
      .spec.ephemeralContainers[]?] as $containers |
    .status.phase == "Running" and
    .status.containerStatuses[0].ready == true and
    .spec.automountServiceAccountToken == false and
    .spec.securityContext.runAsUser == 65532 and
    .spec.securityContext.runAsGroup == 65532 and
    .spec.securityContext.fsGroup == 65532 and
    .spec.securityContext.seccompProfile.type == "RuntimeDefault" and
    all($containers[];
      .securityContext.allowPrivilegeEscalation == false and
      .securityContext.privileged == false and
      .securityContext.readOnlyRootFilesystem == true and
      .securityContext.capabilities.drop == ["ALL"] and
      ((.securityContext.capabilities.add // []) | length) == 0) and
    all($containers[];
      all(.env[]?;
        .name == "HOME" or .name == "TMPDIR" or
        .name == "XDG_CACHE_HOME" or .name == "XDG_CONFIG_HOME" or
        .name == "XDG_DATA_HOME"))
  ' >/dev/null

runtime_uid="$(
  kubectl --context "$context" --namespace "$namespace" exec workspace-0 -- id -u
)"
[[ "$runtime_uid" == "65532" ]] || fail "workspace process is not running as UID 65532"

kubectl --context "$context" --namespace "$namespace" exec workspace-0 -- sh -c '
  test ! -e /var/run/secrets/kubernetes.io/serviceaccount &&
  test ! -S /var/run/docker.sock &&
  test ! -S /run/containerd/containerd.sock &&
  test ! -S /run/podman/podman.sock
' >/dev/null

secret_read="$(
  kubectl --context "$context" auth can-i get secrets \
    --namespace "$namespace" \
    --as="system:serviceaccount:$namespace:workspace" || true
)"
[[ "$secret_read" == "no" ]] || fail "workspace service account can read Secrets"

pod_create="$(
  kubectl --context "$context" auth can-i create pods \
    --namespace "$namespace" \
    --as="system:serviceaccount:$namespace:workspace" || true
)"
[[ "$pod_create" == "no" ]] || fail "workspace service account can create Pods"

secret_objects="$(
  kubectl --context "$context" --namespace "$namespace" get secrets \
    --output name
)"
[[ -z "$secret_objects" ]] || fail "workspace namespace contains Secret objects"

probe_egress() {
  local pod=$1
  local result

  # $1 and $2 in the command belong to the remote shell.
  # shellcheck disable=SC2016
  result="$(kubectl --context "$context" --namespace "$namespace" exec "$pod" -- \
    bash -ceu '
      command -v timeout >/dev/null
      command -v bash >/dev/null
      if timeout 5 bash -c "</dev/tcp/$1/$2" 2>/dev/null; then
        printf connected
      else
        result=$?
        test "$result" = 124 || exit "$result"
        printf timed-out
      fi
    ' paw-egress-probe "$egress_probe_host" "$egress_probe_port" \
    2>/dev/null)" || return 1
  [[ "$result" == connected || "$result" == timed-out ]] || return 1
  printf '%s' "$result"
}

positive_result="$(probe_egress egress-positive-control)" || fail "positive-control probe could not execute"
if [[ "$positive_result" != connected ]]; then
  fail "positive-control pod cannot reach $egress_probe_host:$egress_probe_port; egress enforcement cannot be evaluated"
fi

negative_result="$(probe_egress workspace-0)" || fail "workspace egress probe failed to execute; denial is inconclusive"
if [[ "$negative_result" == connected ]]; then
  fail "default-deny egress is not enforced"
fi
positive_result="$(probe_egress egress-positive-control)" || fail "positive-control recheck could not execute"
[[ "$positive_result" == connected ]] || fail "positive-control destination stopped responding; denial is inconclusive"

"$paw_binary" workspace connect \
  --adapter minikube \
  --context "$context" \
  --local-port "$local_port" >/dev/null 2>&1 &
port_forward_pid="$!"

connected=false
for _ in {1..30}; do
  if curl --silent --show-error --fail --max-time 2 \
    "http://127.0.0.1:$local_port/" >/dev/null 2>&1; then
    connected=true
    break
  fi
  if ! kill -0 "$port_forward_pid" 2>/dev/null; then
    break
  fi
  sleep 1
done
[[ "$connected" == true ]] || fail "loopback T3 connection did not become ready"

repository_root="$(
  git -C "$(dirname "${BASH_SOURCE[0]}")/.." rev-parse --show-toplevel
)"
repository_ref="$(git -C "$repository_root" symbolic-ref --quiet HEAD || true)"
if [[ -z "$repository_ref" ]]; then
  repository_ref="$(
    git -C "$repository_root" for-each-ref \
      --points-at HEAD \
      --format='%(refname)' \
      refs/heads refs/tags refs/remotes | sed -n '1p'
  )"
fi
[[ -n "$repository_ref" ]] || fail "PAW source HEAD is not reachable from an explicit named ref"
repository_commit="$(git -C "$repository_root" rev-parse --verify "$repository_ref^{commit}")"
"$paw_binary" workspace repository add \
  --adapter minikube \
  --context "$context" \
  --source "$repository_root" \
  --revision "$repository_ref" \
  --name paw-conformance >/dev/null

# The single-quoted script expands only inside the workspace shell.
# shellcheck disable=SC2016
kubectl --context "$context" --namespace "$namespace" exec workspace-0 -- \
  sh -ceu '
    repository=/workspace/work/paw-conformance
    test "$(git -C "$repository" rev-parse HEAD)" = "$1"
    ! git -C "$repository" symbolic-ref --quiet HEAD >/dev/null
    test -z "$(git -C "$repository" remote)"
    touch "$repository/.paw-write-test"
    rm -f -- "$repository/.paw-write-test"
  ' paw-repository-check "$repository_commit" >/dev/null

if "$paw_binary" workspace repository add \
  --adapter minikube \
  --context "$context" \
  --source "$repository_root" \
  --revision "$repository_ref" \
  --name paw-conformance >/dev/null 2>&1; then
  fail "repository materialization replaced an existing destination"
fi

pairing_id="$(
  "$paw_binary" workspace pair \
    --adapter minikube \
    --context "$context" \
    --local-port "$local_port" \
    --ttl 1m \
    --label paw-live-conformance \
    --json |
    jq --exit-status --raw-output \
      'if
        (.id | type == "string" and length > 0) and
        (.credential | type == "string" and length > 0) and
        (.pairUrl | startswith("http://127.0.0.1:"))
      then .id
      else error("invalid pairing response")
      end'
)"

"$paw_binary" workspace revoke \
  --adapter minikube \
  --context "$context" \
  --pairing-id "$pairing_id" >/dev/null

if kubectl --context "$context" --namespace "$namespace" logs workspace-0 |
  grep --extended-regexp --ignore-case \
    'pair|token|secret|credential' >/dev/null; then
  fail "credential-shaped content appeared in workload logs"
fi

kill "$port_forward_pid" 2>/dev/null || true
wait "$port_forward_pid" 2>/dev/null || true
port_forward_pid=""

destroy_owned_workspace || fail "ephemeral workspace cleanup could not be verified"

printf 'live Minikube conformance passed for context %s\n' "$context"
