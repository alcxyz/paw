# Architecture at a glance

One PAW workspace on one Kubernetes cluster, as deployed today by the
Minikube and generic Kubernetes adapters. The decisions behind each box are
in the [ADR index](adr/README.md).

## Workspace topology

```mermaid
flowchart LR
  subgraph host["Operator host (xyz or the Mac)"]
    cli["paw CLI<br/>defaults, log, aliases"]
    clone["Git clone<br/>remote 'paw' via ext::"]
    browser["Browser<br/>paired T3 session"]
    kubectl["kubectl + kubeconfig"]
    cli --> kubectl
    clone -- "git fetch / push" --> cli
  end

  subgraph cluster["Kubernetes cluster (Minikube, K3s, AKS, ...)"]
    subgraph ns["namespace paw-workspace"]
      subgraph pod["workspace-0 (StatefulSet, non-root, default-deny)"]
        t3["T3 server :3773"]
        codex["Codex CLI"]
        claude["Claude Code CLI"]
        opencode["OpenCode CLI"]
        go["Go toolchain<br/>(developer profile)"]
        t3 --- codex
        t3 --- claude
        t3 --- opencode
      end
      state[("workspace-state<br/>T3 threads, home")]
      work[("workspace-work<br/>/workspace/work/NAME")]
      session[("workspace-session<br/>provider logins")]
      pod --- state
      pod --- work
      pod --- session
      proxy["egress proxy<br/>CONNECT :443 only<br/>reviewed hostnames"]
      dests["ConfigMap<br/>egress-destinations"]
      dests --> proxy
      pod -- "HTTPS_PROXY<br/>(only allowed egress)" --> proxy
    end
    dns["cluster DNS"]
    proxy --> dns
  end

  providers["Provider APIs<br/>OpenAI and Anthropic endpoints"]
  proxy -- "TCP 443 to public addresses" --> providers

  kubectl -- "port-forward 127.0.0.1" --> t3
  browser --> kubectl
  kubectl -- "exec: git-upload-pack / git-receive-pack" --> work
  kubectl -- "exec: codex login, claude auth login" --> session
  kubectl -- "exec: bundle import, patch export" --> work
```

What the picture enforces:

- The workspace pod can reach exactly one thing, the proxy, and the proxy
  admits exactly the hostnames resolved from the selected provider's
  purposes ([ADR-013](adr/ADR-013-per-workspace-bounded-egress-proxy.md)):
  `auth.openai.com`, `chatgpt.com`, and `api.openai.com` for Codex;
  `claude.ai`, `platform.claude.com`, and `api.anthropic.com` for Claude
  Code. There is no DNS egress from the pod.
- Provider logins happen inside the pod and live on their own retained claim,
  outside every backup ([ADR-014](adr/ADR-014-pod-scoped-provider-login-state.md),
  [ADR-017](adr/ADR-017-retained-provider-session-claim.md)).
- Every transfer with the host rides `kubectl exec` under the operator's
  identity: bundle import, patch export, and the Git `ext::` transport
  ([ADR-004](adr/ADR-004-streamed-git-bundle-repository-materialization.md),
  [ADR-018](adr/ADR-018-host-driven-git-transport.md)). The workspace holds
  no remote and no credential of the operator's.
- The browser reaches T3 only through the loopback tunnel today; real ingress
  is future work.

## Backup and restore

```mermaid
flowchart LR
  op["operator: paw ws backup"] --> lock["lifecycle lock<br/>ConfigMap"]
  lock --> stop["writer scaled to 0"]
  stop --> helper["backup helper pod<br/>mounts state + work only"]
  helper -- "verified archive stream" --> archive[("workspace.paw-backup<br/>metadata.json")]
  helper2["backup helper pod<br/>check-empty, import"]
  archive -- "paw ws restore<br/>(stopped, empty claims)" --> helper2
  helper2 --> newstate[("state")]
  helper2 --> newwork[("work")]
  session[("workspace-session")] -. "never mounted,<br/>never archived" .- helper
```

The session claim is deliberately absent from both directions
([ADR-012](adr/ADR-012-offline-backup-and-empty-target-restore.md),
[ADR-017](adr/ADR-017-retained-provider-session-claim.md)).

## Image matrix

```mermaid
flowchart TB
  base["headless T3 core<br/>+ git, bash, coreutils"]
  base --> core["core"]
  base --> dev["developer<br/>+ go, shellcheck, jq, yq"]
  base --> pr["platform-readonly<br/>+ kubectl, helm, kustomize, opentofu"]
  core --> c1["none | codex | claude-code | opencode | all"]
  dev --> d1["none | codex | claude-code | opencode | all"]
  pr --> p1["none | codex | claude-code | opencode | all"]
```

Fifteen workspace images, each with measured closure, layer, and transfer
budgets, plus the egress proxy and backup helper images
([ADR-015](adr/ADR-015-developer-profile.md),
[ADR-016](adr/ADR-016-all-providers-composition.md)).
