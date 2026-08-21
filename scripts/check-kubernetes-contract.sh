#!/usr/bin/env bash
set -euo pipefail

repo_root=${1:-.}
paw_binary=${2:-}
check_dir=$(mktemp -d "${TMPDIR:-/tmp}/paw-kubernetes-contract.XXXXXX")
trap 'rm -rf -- "$check_dir"' EXIT

kubectl kustomize "$repo_root/deploy/base" >"$check_dir/base.yaml"
kubectl kustomize "$repo_root/deploy/adapters/minikube" >"$check_dir/minikube.yaml"

yq eval-all -o=json '[.]' "$check_dir/base.yaml" >"$check_dir/base.json"
yq eval-all -o=json '[.]' "$check_dir/minikube.yaml" >"$check_dir/minikube.json"

jq --exit-status '
  ([.[] | select(.kind == "StatefulSet")] | length) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] | .spec.replicas) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .metadata.annotations["paw.alc.xyz/single-writer"]) == "true" and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .metadata.annotations["paw.alc.xyz/profile"]) == "core" and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .metadata.annotations["paw.alc.xyz/provider"]) == "none" and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.automountServiceAccountToken) == false and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.securityContext.runAsNonRoot) == true and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.securityContext.runAsUser) == 65532 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.securityContext.runAsGroup) == 65532 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.securityContext.fsGroup) == 65532 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.securityContext.seccompProfile.type) == "RuntimeDefault" and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation) == false and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].securityContext.privileged) == false and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem) == true and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].securityContext.capabilities.drop |
    index("ALL")) != null and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].resources.requests.cpu) != null and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].resources.limits.memory) != null and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].image |
    test("@sha256:[0-9a-f]{64}$")) and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].args) ==
    ["--log-level", "warn", "start", "--mode", "web", "--host", "0.0.0.0",
      "--port", "3773", "--base-dir", "/workspace/state/t3", "--no-browser",
      "/workspace/work"] and
  ([.[] | select(.kind == "PersistentVolumeClaim")][0] |
    .spec.accessModes) == ["ReadWriteOnce"] and
  ([.[] | select(.kind == "Role")][0] | .rules) == [] and
  ([.[] | select(.kind == "NetworkPolicy")][0] |
    .spec.policyTypes | sort) == ["Egress", "Ingress"] and
  ([.[] | select(.kind == "NetworkPolicy")][0] | .spec.ingress) == [] and
  ([.[] | select(.kind == "NetworkPolicy")][0] | .spec.egress) == [] and
  ([.[] | select(.kind == "Secret")] | length) == 0 and
  ([.[] | select(.kind == "ConfigMap")][0] |
    .data.profile == "core" and .data.provider == "none") and
  ([.. | objects | select(has("hostPath"))] | length) == 0 and
  ([.. | objects | select(has("secretKeyRef") or has("secretRef"))] | length) == 0 and
  ([.. | objects | select(has("serviceAccountToken"))] | length) == 0 and
  ([.. | objects | select(has("envFrom"))] | length) == 0 and
  ([.. | objects | select(
    .hostNetwork == true or .hostPID == true or .hostIPC == true or
    .shareProcessNamespace == true)] | length) == 0 and
  ([.. | objects | select(has("hostPort") or has("hostIP"))] | length) == 0 and
  ([.. | objects | select(has("volumeDevices"))] | length) == 0 and
  ([.. | objects | select(has("sysctls"))] | length) == 0 and
  ([.. | objects | select(has("hostAliases"))] | length) == 0 and
  ([.. | objects | select(has("capabilities")) |
    .capabilities.add[]?] | length) == 0 and
  ([.. | objects | select(has("env")) | .env[]? |
    select(.name | test("(TOKEN|SECRET|PASSWORD|CREDENTIAL|ACCESS_KEY|PRIVATE_KEY)"; "i"))] |
    length) == 0 and
  ([.. | strings |
    select(test("(docker\\.sock|containerd\\.sock|podman\\.sock|/var/run/docker|/run/containerd)"; "i"))] |
    length) == 0
' "$check_dir/base.json" >/dev/null

jq --exit-status '
  ([.[] | select(.kind == "Namespace")][0] |
    .metadata.labels["pod-security.kubernetes.io/enforce"]) == "restricted" and
  ([.[] | select(.kind == "StatefulSet")] | length) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] | .spec.replicas) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].image) == "paw-core:dev"
' "$check_dir/minikube.json" >/dev/null

if [[ -n "$paw_binary" ]]; then
  while read -r profile provider image; do
    name=${profile}-${provider}
    "$paw_binary" workspace render \
      --adapter minikube \
      --profile "$profile" \
      --provider "$provider" >"$check_dir/$name.yaml"
    yq eval-all -o=json '[.]' "$check_dir/$name.yaml" >"$check_dir/$name.json"
    jq --exit-status \
      --arg profile "$profile" \
      --arg provider "$provider" \
      --arg image "$image:dev" '
        ([.[] | select(.kind == "StatefulSet")][0] |
          .metadata.annotations["paw.alc.xyz/profile"] == $profile and
          .metadata.annotations["paw.alc.xyz/provider"] == $provider and
          .spec.template.metadata.annotations["paw.alc.xyz/profile"] == $profile and
          .spec.template.metadata.annotations["paw.alc.xyz/provider"] == $provider and
          .spec.template.spec.containers[0].image == $image and
          .spec.template.spec.automountServiceAccountToken == false and
          .spec.template.spec.containers[0].securityContext.privileged == false and
          .spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem == true) and
        ([.[] | select(.kind == "ConfigMap")][0] |
          .data.profile == $profile and .data.provider == $provider) and
        ([.[] | select(.kind == "Secret")] | length) == 0 and
        ([.. | objects | select(has("hostPath") or has("secretKeyRef") or
          has("secretRef") or has("serviceAccountToken"))] | length) == 0
      ' "$check_dir/$name.json" >/dev/null
  done <<'EOF'
core none paw-core
core codex paw-codex
core claude-code paw-claude-code
core opencode paw-opencode
platform-readonly none paw-platform-readonly
platform-readonly codex paw-platform-readonly-codex
platform-readonly claude-code paw-platform-readonly-claude-code
platform-readonly opencode paw-platform-readonly-opencode
EOF
fi
