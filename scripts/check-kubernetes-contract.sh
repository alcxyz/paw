#!/usr/bin/env bash
set -euo pipefail

repo_root=${1:-.}
check_dir=${TMPDIR:?}/paw-kubernetes-contract
mkdir -p "$check_dir"

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
    .spec.template.spec.automountServiceAccountToken) == false and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.securityContext.runAsNonRoot) == true and
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
  ([.. | objects | select(has("hostPath"))] | length) == 0 and
  ([.. | objects | select(has("secretKeyRef") or has("secretRef"))] | length) == 0
' "$check_dir/base.json" >/dev/null

jq --exit-status '
  ([.[] | select(.kind == "Namespace")][0] |
    .metadata.labels["pod-security.kubernetes.io/enforce"]) == "restricted" and
  ([.[] | select(.kind == "StatefulSet")] | length) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] | .spec.replicas) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].image) == "paw-core:dev"
' "$check_dir/minikube.json" >/dev/null
