#!/usr/bin/env bash

set -euo pipefail

namespace="paw-workspace"
port_forward_pid=""
workspace_created=false

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
    "$paw_binary" workspace destroy \
      --adapter minikube \
      --context "$context" \
      --delete-state >/dev/null 2>&1 || true
  fi
}

trap cleanup EXIT INT TERM

if [[ "${PAW_LIVE_TEST:-}" != "1" ]]; then
  fail "set PAW_LIVE_TEST=1 to acknowledge creation and deletion of an ephemeral test workspace"
fi
if [[ $# -lt 2 || $# -gt 3 ]]; then
  fail "usage: PAW_LIVE_TEST=1 scripts/check-minikube-live.sh CONTEXT PAW_BINARY [LOCAL_PORT]"
fi

context="$1"
paw_binary="$2"
local_port="${3:-43773}"

[[ -n "$context" ]] || fail "CONTEXT must not be empty"
[[ -x "$paw_binary" ]] || fail "PAW_BINARY must be an executable file"
[[ "$local_port" =~ ^[0-9]+$ ]] || fail "LOCAL_PORT must be numeric"
((local_port >= 1024 && local_port <= 65535)) || fail "LOCAL_PORT must be between 1024 and 65535"

for dependency in curl jq kubectl; do
  command -v "$dependency" >/dev/null || fail "$dependency is required"
done

kubectl --context "$context" get nodes >/dev/null
if kubectl --context "$context" get namespace "$namespace" >/dev/null 2>&1; then
  fail "namespace $namespace already exists; refusing to alter a workspace not created by this test"
fi

workspace_created=true
"$paw_binary" workspace create \
  --adapter minikube \
  --context "$context" \
  --profile core \
  --provider none >/dev/null

kubectl --context "$context" --namespace "$namespace" rollout status \
  statefulset/workspace --timeout=240s >/dev/null

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
    .status.phase == "Running" and
    .status.containerStatuses[0].ready == true and
    .spec.automountServiceAccountToken == false and
    .spec.securityContext.runAsUser == 65532 and
    .spec.securityContext.runAsGroup == 65532 and
    .spec.securityContext.fsGroup == 65532 and
    .spec.securityContext.seccompProfile.type == "RuntimeDefault" and
    .spec.containers[0].securityContext.allowPrivilegeEscalation == false and
    .spec.containers[0].securityContext.privileged == false and
    .spec.containers[0].securityContext.readOnlyRootFilesystem == true and
    .spec.containers[0].securityContext.capabilities.drop == ["ALL"] and
    ([.spec.containers[].env[]?.name |
      select(test("(TOKEN|SECRET|PASSWORD|CREDENTIAL|ACCESS_KEY|PRIVATE_KEY)"; "i"))] |
      length) == 0
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

if kubectl --context "$context" --namespace "$namespace" exec workspace-0 -- \
  curl --silent --show-error --fail --max-time 5 https://example.com \
  >/dev/null 2>&1; then
  fail "default-deny egress is not enforced"
fi

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

"$paw_binary" workspace destroy \
  --adapter minikube \
  --context "$context" \
  --delete-state >/dev/null

if kubectl --context "$context" get namespace "$namespace" >/dev/null 2>&1; then
  fail "ephemeral workspace namespace still exists after destroy"
fi
workspace_created=false

printf 'live Minikube conformance passed for context %s\n' "$context"
