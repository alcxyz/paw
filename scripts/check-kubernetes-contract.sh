#!/usr/bin/env bash
set -euo pipefail

repo_root=${1:-.}
paw_binary=${2:-}
check_dir=$(mktemp -d "${TMPDIR:-/tmp}/paw-kubernetes-contract.XXXXXX")
trap 'rm -rf -- "$check_dir"' EXIT

assert_security_contract() {
  jq --exit-status '
    [.. | objects |
      select(has("containers") or has("initContainers") or
        has("ephemeralContainers")) |
      (.containers[]?, .initContainers[]?, .ephemeralContainers[]?)] as $containers |
    ($containers | length) > 0 and
    all($containers[];
      .securityContext.allowPrivilegeEscalation == false and
      .securityContext.privileged == false and
      .securityContext.readOnlyRootFilesystem == true and
      (.securityContext.capabilities.drop | index("ALL")) != null and
      ((.securityContext.capabilities.add // []) | length) == 0) and
    all($containers[];
      all(.env[]?;
        .name == "HOME" or .name == "HTTPS_PROXY" or .name == "NO_PROXY" or
        .name == "TMPDIR" or .name == "XDG_CACHE_HOME" or
        .name == "XDG_CONFIG_HOME" or .name == "XDG_DATA_HOME")) and
    ([.. | objects | select(has("hostPath"))] | length) == 0 and
    ([.. | objects | select(has("secretKeyRef") or has("secretRef") or
      has("serviceAccountToken"))] | length) == 0 and
    ([.. | objects | select(has("volumes")) | .volumes[]? |
      select(has("secret") or has("csi"))] | length) == 0 and
    ([.. | objects | select(has("projected")) | .projected.sources[]? |
      select(has("secret"))] | length) == 0 and
    ([.. | objects | select(has("envFrom"))] | length) == 0 and
    ([.. | objects | select(
      .hostNetwork == true or .hostPID == true or .hostIPC == true or
      .shareProcessNamespace == true)] | length) == 0 and
    ([.. | objects | select(has("hostPort") or has("hostIP"))] | length) == 0 and
    ([.. | objects | select(has("volumeDevices"))] | length) == 0 and
    ([.. | objects | select(has("sysctls"))] | length) == 0 and
    ([.. | objects | select(has("hostAliases"))] | length) == 0 and
    ([.. | strings | select(test(
      "(docker\\.sock|containerd\\.sock|podman\\.sock|/var/run/docker|/run/containerd)";
      "i"))] | length) == 0
  ' "$1" >/dev/null
}

expect_security_rejection() {
  local description=$1
  local manifest=$2

  if assert_security_contract "$manifest"; then
    echo "security contract accepted $description" >&2
    exit 1
  fi
}

assert_storage_layout() {
  jq --exit-status '
    ([.[] | select(.kind == "PersistentVolumeClaim")] | length) == 2 and
    ([.[] | select(.kind == "PersistentVolumeClaim") | .metadata.name] | sort) ==
      ["workspace-state", "workspace-work"] and
    all([.[] | select(.kind == "PersistentVolumeClaim")][];
      .metadata.labels["paw.alc.xyz/managed-by"] == "paw" and
      .metadata.annotations["paw.alc.xyz/state-policy"] == "retain-until-destroy" and
      .spec.accessModes == ["ReadWriteOnce"] and
      .spec.resources.requests.storage == "10Gi") and
    ([.[] | select(.kind == "StatefulSet")] | length) == 1 and
    ([.[] | select(.kind == "StatefulSet")][0].spec | has("volumeClaimTemplates") | not) and
    ([.[] | select(.kind == "StatefulSet")][0] |
      .metadata.annotations["paw.alc.xyz/storage-layout"] == "persistent-v1" and
      .spec.template.metadata.annotations["paw.alc.xyz/storage-layout"] == "persistent-v1" and
      [.spec.template.spec.containers[] | select(.name == "t3") |
        .volumeMounts[] | select(.name == "state") | .mountPath] == ["/workspace/state"] and
      [.spec.template.spec.containers[] | select(.name == "t3") |
        .volumeMounts[] | select(.name == "work") | .mountPath] == ["/workspace/work"] and
      [.spec.template.spec.volumes[] | select(.name == "state") |
        .persistentVolumeClaim.claimName] == ["workspace-state"] and
      [.spec.template.spec.volumes[] | select(.name == "work") |
        .persistentVolumeClaim.claimName] == ["workspace-work"] and
      [.spec.template.spec.volumes[] | select(.name == "tmp") | has("emptyDir")] == [true] and
      ([.spec.template.spec.volumes[] | select(has("emptyDir")) | .name] == ["tmp", "session"]) and
      ([.spec.template.spec.volumes[] | select(.name == "session") | .emptyDir.medium] == ["Memory"]))
  ' "$1" >/dev/null
}

expect_storage_rejection() {
  local description=$1
  local manifest=$2

  if assert_storage_layout "$manifest"; then
    echo "storage contract accepted $description" >&2
    exit 1
  fi
}

# The per-workspace egress proxy (ADR-013): one Deployment reachable only from
# workspace pods, reaching only DNS and public TCP 443; workspace pods reach
# only the proxy; the destination list is the resolved purpose table.
assert_egress_contract() {
  jq --exit-status --arg destinations "$2" '
    ([.[] | select(.kind == "Deployment")] | length) == 1 and
    ([.[] | select(.kind == "Deployment")][0] |
      .metadata.name == "egress" and
      .metadata.labels["app.kubernetes.io/component"] == "egress" and
      .spec.replicas == 1 and
      .spec.template.spec.automountServiceAccountToken == false and
      .spec.template.spec.enableServiceLinks == false and
      .spec.template.spec.serviceAccountName == "egress" and
      .spec.template.spec.securityContext.runAsNonRoot == true and
      .spec.template.spec.securityContext.runAsUser == 65532 and
      .spec.template.spec.securityContext.seccompProfile.type == "RuntimeDefault" and
      (.spec.template.spec.containers | length) == 1 and
      .spec.template.spec.containers[0].name == "proxy" and
      (.spec.template.spec.containers[0].image | test("(^paw-egress-proxy:dev$|@sha256:[0-9a-f]{64}$)")) and
      .spec.template.spec.containers[0].args ==
        ["--listen", ":3128", "--destinations", "/etc/paw-egress/destinations"] and
      ([.spec.template.spec.containers[0].env[]?] | length) == 0 and
      [.spec.template.spec.containers[0].volumeMounts[] | .mountPath] == ["/etc/paw-egress"] and
      [.spec.template.spec.volumes[] | .configMap.name] == ["egress-destinations"]) and
    ([.[] | select(.kind == "ServiceAccount" and .metadata.name == "egress")][0] |
      .automountServiceAccountToken == false) and
    ([.[] | select(.kind == "Service" and .metadata.name == "egress")][0] |
      .spec.selector["app.kubernetes.io/component"] == "egress" and
      [.spec.ports[] | .port] == [3128]) and
    ([.[] | select(.kind == "ConfigMap" and .metadata.name == "egress-destinations")][0] |
      .data.destinations == $destinations) and
    ([.[] | select(.kind == "NetworkPolicy" and .metadata.name == "egress-proxy")][0] |
      .spec.podSelector.matchLabels["app.kubernetes.io/component"] == "egress" and
      (.spec.policyTypes | sort) == ["Egress", "Ingress"] and
      (.spec.ingress | length) == 1 and
      [.spec.ingress[0].from[] | .podSelector.matchLabels["app.kubernetes.io/component"]] == ["workspace"] and
      [.spec.ingress[0].ports[] | .port] == ["proxy"] and
      (.spec.egress | length) == 2 and
      [.spec.egress[0].to[] | .podSelector.matchLabels["k8s-app"]] == ["kube-dns"] and
      [.spec.egress[0].to[] | .namespaceSelector.matchLabels["kubernetes.io/metadata.name"]] == ["kube-system"] and
      ([.spec.egress[0].ports[] | .port] | unique) == [53] and
      [.spec.egress[1].ports[] | .port] == [443] and
      all(.spec.egress[1].to[]; has("ipBlock")) and
      ([.spec.egress[1].to[] | .ipBlock.except[]] |
        index("10.0.0.0/8") != null and index("172.16.0.0/12") != null and
        index("192.168.0.0/16") != null and index("169.254.0.0/16") != null and
        index("127.0.0.0/8") != null and index("fc00::/7") != null)) and
    ([.[] | select(.kind == "NetworkPolicy" and .metadata.name == "workspace-egress-proxy")][0] |
      .spec.podSelector.matchLabels["app.kubernetes.io/component"] == "workspace" and
      .spec.policyTypes == ["Egress"] and
      (.spec.egress | length) == 1 and
      [.spec.egress[0].to[] | .podSelector.matchLabels["app.kubernetes.io/component"]] == ["egress"] and
      [.spec.egress[0].ports[] | .port] == ["proxy"]) and
    ([.[] | select(.kind == "StatefulSet")][0] |
      .spec.template.spec.enableServiceLinks == true and
      [.spec.template.spec.containers[0].env[] | select(.name == "HTTPS_PROXY") | .value] ==
        ["http://$(EGRESS_SERVICE_HOST):$(EGRESS_SERVICE_PORT)"] and
      [.spec.template.spec.containers[0].env[] | select(.name == "NO_PROXY") | .value] ==
        ["localhost,127.0.0.1"])
  ' "$1" >/dev/null
}

kubectl kustomize "$repo_root/deploy/base" >"$check_dir/base.yaml"
kubectl kustomize "$repo_root/deploy/adapters/kubernetes" >"$check_dir/kubernetes.yaml"
kubectl kustomize "$repo_root/deploy/adapters/minikube" >"$check_dir/minikube.yaml"

yq eval-all -o=json '[.]' "$check_dir/base.yaml" >"$check_dir/base.json"
yq eval-all -o=json '[.]' "$check_dir/kubernetes.yaml" >"$check_dir/kubernetes.json"
yq eval-all -o=json '[.]' "$check_dir/minikube.yaml" >"$check_dir/minikube.json"

assert_storage_layout "$check_dir/base.json"
assert_storage_layout "$check_dir/kubernetes.json"
assert_storage_layout "$check_dir/minikube.json"

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
    .metadata.annotations["paw.alc.xyz/storage-layout"]) == "persistent-v1" and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.metadata.annotations["paw.alc.xyz/storage-layout"]) == "persistent-v1" and
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
  ([.[] | select(.kind == "PersistentVolumeClaim")] | length) == 2 and
  ([.[] | select(.kind == "PersistentVolumeClaim") | .metadata.name] | sort) ==
    ["workspace-state", "workspace-work"] and
  all([.[] | select(.kind == "PersistentVolumeClaim")][];
    .metadata.labels["app.kubernetes.io/name"] == "paw" and
    .metadata.labels["app.kubernetes.io/component"] == "workspace" and
    .metadata.labels["paw.alc.xyz/managed-by"] == "paw" and
    .metadata.annotations["paw.alc.xyz/state-policy"] == "retain-until-destroy" and
    .spec.accessModes == ["ReadWriteOnce"] and
    .spec.resources.requests.storage == "10Gi") and
  ([.[] | select(.kind == "PersistentVolumeClaim") |
    select(.metadata.name == "workspace-state" or .metadata.name == "workspace-work")] | length) == 2 and
  ([.[] | select(.kind == "StatefulSet")][0] | has("spec") and
    (.spec | has("volumeClaimTemplates") | not)) and
  ([.[] | select(.kind == "StatefulSet")][0] |
    [.spec.template.spec.volumes[] | select(.name == "state") |
      .persistentVolumeClaim.claimName] == ["workspace-state"] and
    [.spec.template.spec.volumes[] | select(.name == "work") |
      .persistentVolumeClaim.claimName] == ["workspace-work"] and
    [.spec.template.spec.volumes[] | select(.name == "tmp") | has("emptyDir")] == [true] and
    ([.spec.template.spec.volumes[] | select(has("emptyDir")) | .name] == ["tmp", "session"]) and
    [.spec.template.spec.containers[0].volumeMounts[] |
      select(.name == "session") | [.mountPath, .subPath]] ==
      [["/workspace/session/codex", "codex"], ["/workspace/session/claude", "claude"]] and
    [.spec.template.spec.containers[0].volumeMounts[] |
      select(.name == "state") | .mountPath] == ["/workspace/state"] and
    [.spec.template.spec.containers[0].volumeMounts[] |
      select(.name == "work") | .mountPath] == ["/workspace/work"] and
    [.spec.template.spec.containers[0].volumeMounts[] |
      select(.name == "tmp") | .mountPath] == ["/tmp"]) and
  ([.[] | select(.kind == "Role")][0] | .rules) == [] and
  ([.[] | select(.kind == "NetworkPolicy" and .metadata.name == "workspace-default-deny")][0] |
    .spec.policyTypes | sort) == ["Egress", "Ingress"] and
  ([.[] | select(.kind == "NetworkPolicy" and .metadata.name == "workspace-default-deny")][0] | .spec.ingress) == [] and
  ([.[] | select(.kind == "NetworkPolicy" and .metadata.name == "workspace-default-deny")][0] | .spec.egress) == [] and
  ([.[] | select(.kind == "Secret")] | length) == 0 and
  ([.[] | select(.kind == "ConfigMap" and .metadata.name == "workspace-contract")][0] |
    .data.profile == "core" and .data.provider == "none" and
    .data["state-policy"] == "retain-until-destroy") and
  ([.. | objects | select(has("hostPath"))] | length) == 0
' "$check_dir/base.json" >/dev/null

assert_egress_contract "$check_dir/base.json" ""

assert_security_contract "$check_dir/base.json"

jq --exit-status '
  ([.[] | select(.kind == "Namespace")][0] |
    .metadata.labels["paw.alc.xyz/managed-by"]) == "paw" and
  ([.[] | select(.kind == "Namespace")][0] |
    .metadata.labels["pod-security.kubernetes.io/enforce"]) == "restricted" and
  ([.[] | select(.kind == "StatefulSet")] | length) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] | .spec.replicas) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].image) ==
    "registry.invalid/paw/workspace@sha256:0000000000000000000000000000000000000000000000000000000000000000" and
  ([.[] | select(.kind == "Deployment")][0] |
    .spec.template.spec.containers[0].image) ==
    "registry.invalid/paw/egress-proxy@sha256:0000000000000000000000000000000000000000000000000000000000000000" and
  ([.. | strings | select(test("minikube|:dev$"; "i"))] | length) == 0
' "$check_dir/kubernetes.json" >/dev/null

assert_security_contract "$check_dir/kubernetes.json"
assert_egress_contract "$check_dir/kubernetes.json" ""

jq --exit-status '
  ([.[] | select(.kind == "Namespace")][0] |
    .metadata.labels["paw.alc.xyz/managed-by"]) == "paw" and
  ([.[] | select(.kind == "Namespace")][0] |
    .metadata.labels["pod-security.kubernetes.io/enforce"]) == "restricted" and
  ([.[] | select(.kind == "StatefulSet")] | length) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] | .spec.replicas) == 1 and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].image) == "paw-core:dev" and
  ([.[] | select(.kind == "StatefulSet")][0] |
    .spec.template.spec.containers[0].imagePullPolicy) == "Never" and
  ([.[] | select(.kind == "Deployment")][0] |
    .spec.template.spec.containers[0].image) == "paw-egress-proxy:dev" and
  ([.[] | select(.kind == "Deployment")][0] |
    .spec.template.spec.containers[0].imagePullPolicy) == "Never"
' "$check_dir/minikube.json" >/dev/null

assert_security_contract "$check_dir/minikube.json"
assert_egress_contract "$check_dir/minikube.json" ""

jq 'map(if .kind == "StatefulSet" then
  .spec.template.spec.volumes +=
  [{"name":"forbidden-secret","secret":{"secretName":"forbidden"}}]
  else . end)' \
  "$check_dir/base.json" >"$check_dir/secret-volume.json"
expect_security_rejection "a secret volume" "$check_dir/secret-volume.json"

jq 'map(if .kind == "StatefulSet" then
  .spec.template.spec.volumes +=
  [{"name":"forbidden-projected","projected":{"sources":[
    {"secret":{"name":"forbidden"}}
  ]}}] else . end)' "$check_dir/base.json" >"$check_dir/projected-secret.json"
expect_security_rejection "a projected secret" "$check_dir/projected-secret.json"

jq 'map(if .kind == "StatefulSet" then
  .spec.template.spec.volumes +=
  [{"name":"forbidden-csi","csi":{
    "driver":"secrets-store.csi.k8s.io",
    "volumeAttributes":{"secretProviderClass":"forbidden"}
  }}]
  else . end)' \
  "$check_dir/base.json" >"$check_dir/csi-volume.json"
expect_security_rejection "a CSI volume" "$check_dir/csi-volume.json"

jq 'map(if .kind == "StatefulSet" then
  .spec.template.spec.containers[0].env +=
  [{"name":"GH_PAT","value":"forbidden"}]
  else . end)' \
  "$check_dir/base.json" >"$check_dir/credential-env.json"
expect_security_rejection "an undeclared environment variable" "$check_dir/credential-env.json"

jq 'map(if .kind == "StatefulSet" then
  .spec.template.spec.initContainers = [{
    "name":"unsafe-init",
    "image":"example.invalid/unsafe:latest",
    "securityContext":{
      "allowPrivilegeEscalation":false,
      "privileged":true,
      "readOnlyRootFilesystem":true,
      "capabilities":{"drop":["ALL"]}
    }
  }] else . end)' "$check_dir/base.json" >"$check_dir/unsafe-init.json"
expect_security_rejection "a privileged init container" "$check_dir/unsafe-init.json"

expect_egress_rejection() {
  local description=$1
  local manifest=$2

  if assert_egress_contract "$manifest" ""; then
    echo "egress contract accepted $description" >&2
    exit 1
  fi
}

jq 'map(if .kind == "NetworkPolicy" and .metadata.name == "workspace-egress-proxy" then
  .spec.egress += [{"to":[{"ipBlock":{"cidr":"0.0.0.0/0"}}]}]
  else . end)' "$check_dir/base.json" >"$check_dir/open-workspace-egress.json"
expect_egress_rejection "an open workspace egress rule" "$check_dir/open-workspace-egress.json"

jq 'map(if .kind == "NetworkPolicy" and .metadata.name == "egress-proxy" then
  .spec.egress[1].to |= map(.ipBlock.except |= map(select(. != "10.0.0.0/8")))
  else . end)' "$check_dir/base.json" >"$check_dir/proxy-private-egress.json"
expect_egress_rejection "proxy egress to a private range" "$check_dir/proxy-private-egress.json"

jq 'map(if .kind == "NetworkPolicy" and .metadata.name == "egress-proxy" then
  .spec.ingress[0].from = [{"namespaceSelector":{}}]
  else . end)' "$check_dir/base.json" >"$check_dir/proxy-open-ingress.json"
expect_egress_rejection "proxy ingress from any namespace" "$check_dir/proxy-open-ingress.json"

jq 'map(if .kind == "StatefulSet" then
  .spec.template.spec.containers[0].env |= map(
    if .name == "HTTPS_PROXY" then .value = "http://203.0.113.9:3128" else . end)
  else . end)' "$check_dir/base.json" >"$check_dir/foreign-proxy.json"
expect_egress_rejection "a workspace proxy address outside the namespace service" "$check_dir/foreign-proxy.json"

jq 'map(if .kind == "StatefulSet" then
  .spec.template.spec.volumes |= map(
    if .name == "work" then {"name":"work","emptyDir":{}} else . end
  )
  else . end)' "$check_dir/base.json" >"$check_dir/ephemeral-work.json"
expect_storage_rejection "an ephemeral work volume" "$check_dir/ephemeral-work.json"

jq 'map(if .kind == "StatefulSet" then
  (.spec.template.spec.volumes[] | select(.name == "work").persistentVolumeClaim.claimName) = "workspace-state"
  else . end)' "$check_dir/base.json" >"$check_dir/shared-claim.json"
expect_storage_rejection "aliased work and state claims" "$check_dir/shared-claim.json"

jq 'map(select(.kind != "PersistentVolumeClaim" or .metadata.name != "workspace-work"))' \
  "$check_dir/base.json" >"$check_dir/missing-work-claim.json"
expect_storage_rejection "a missing work claim" "$check_dir/missing-work-claim.json"

jq 'map(if .kind == "StatefulSet" then
  .spec.volumeClaimTemplates = []
  else . end)' "$check_dir/base.json" >"$check_dir/claim-template.json"
expect_storage_rejection "a StatefulSet claim template" "$check_dir/claim-template.json"

if [[ -n "$paw_binary" ]]; then
  codex_destinations=$'auth.openai.com approved-provider-api\nchatgpt.com approved-provider-api\napi.openai.com approved-provider-api\n'
  claude_code_destinations=$'claude.ai approved-provider-api\nplatform.claude.com approved-provider-api\napi.anthropic.com approved-provider-api\n'
  while read -r profile provider image; do
    name=${profile}-${provider}
    destinations=""
    if [[ "$provider" == codex ]]; then
      destinations="$codex_destinations"
    elif [[ "$provider" == claude-code ]]; then
      destinations="$claude_code_destinations"
    elif [[ "$provider" == all ]]; then
      destinations="$codex_destinations$claude_code_destinations"
    fi
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
          .spec.template.spec.containers[0].imagePullPolicy == "Never" and
          .spec.template.spec.automountServiceAccountToken == false and
          .spec.template.spec.containers[0].securityContext.privileged == false and
          .spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem == true) and
        ([.[] | select(.kind == "ConfigMap" and .metadata.name == "workspace-contract")][0] |
          .data.profile == $profile and .data.provider == $provider) and
        ([.[] | select(.kind == "Secret")] | length) == 0 and
        ([.. | objects | select(has("hostPath") or has("secretKeyRef") or
          has("secretRef") or has("serviceAccountToken"))] | length) == 0
      ' "$check_dir/$name.json" >/dev/null
    assert_security_contract "$check_dir/$name.json"
    assert_egress_contract "$check_dir/$name.json" "$destinations"

    released_image="registry.example/paw/$image@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    released_egress="registry.example/paw/paw-egress-proxy@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    "$paw_binary" workspace render \
      --adapter kubernetes \
      --profile "$profile" \
      --provider "$provider" \
      --image-ref "$released_image" \
      --egress-image-ref "$released_egress" >"$check_dir/$name-kubernetes.yaml"
    yq eval-all -o=json '[.]' \
      "$check_dir/$name-kubernetes.yaml" >"$check_dir/$name-kubernetes.json"
    jq --exit-status \
      --arg profile "$profile" \
      --arg provider "$provider" \
      --arg image "$released_image" \
      --arg egress "$released_egress" '
        ([.[] | select(.kind == "StatefulSet")][0] |
          .metadata.annotations["paw.alc.xyz/profile"] == $profile and
          .metadata.annotations["paw.alc.xyz/provider"] == $provider and
          .spec.template.metadata.annotations["paw.alc.xyz/profile"] == $profile and
          .spec.template.metadata.annotations["paw.alc.xyz/provider"] == $provider and
          .spec.template.spec.containers[0].image == $image and
          .spec.template.spec.containers[0].imagePullPolicy == "IfNotPresent") and
        ([.[] | select(.kind == "ConfigMap" and .metadata.name == "workspace-contract")][0] |
          .data.profile == $profile and .data.provider == $provider) and
        ([.[] | select(.kind == "Deployment")][0] |
          .spec.template.spec.containers[0].image == $egress) and
        ([.. | strings | select(test("minikube|:dev$"; "i"))] | length) == 0
      ' "$check_dir/$name-kubernetes.json" >/dev/null
    assert_security_contract "$check_dir/$name-kubernetes.json"
    assert_egress_contract "$check_dir/$name-kubernetes.json" "$destinations"
  done <<'EOF'
core none paw-core
core codex paw-codex
core claude-code paw-claude-code
core opencode paw-opencode
developer none paw-developer
developer codex paw-developer-codex
developer claude-code paw-developer-claude-code
developer opencode paw-developer-opencode
platform-readonly none paw-platform-readonly
platform-readonly codex paw-platform-readonly-codex
platform-readonly claude-code paw-platform-readonly-claude-code
platform-readonly opencode paw-platform-readonly-opencode
core all paw-all
developer all paw-developer-all
platform-readonly all paw-platform-readonly-all
EOF
fi
