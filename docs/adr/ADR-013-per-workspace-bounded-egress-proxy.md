# ADR-013: Per-workspace bounded egress proxy

- Status: Accepted
- Date: 2026-09-22
- Implements: ADR-003 purpose-bound egress and ADR-005 decision 4

## Context

Every PAW profile declares `defaultDenyEgress` and a list of egress purposes
such as `approved-provider-api` and `selected-git-read`. The portable base
enforces the default deny with standard `NetworkPolicy`, and the live verifier
proves that enforcement. Nothing yet resolves a purpose to a destination, so a
provider image cannot reach its provider API. The Codex pilot in ADR-008 is
blocked on this: `codex login` must not run until bounded provider egress and
credential storage exist.

Standard `NetworkPolicy` expresses pod selectors, namespaces, IP blocks, and
ports. It cannot express hostnames. Provider and Git endpoints are hostnames
whose addresses change, so an IP-block allowlist would be brittle and would
still permit any service that shares those addresses. CNI-specific FQDN policy
exists in some implementations, but ADR-005 rejects it as a portable base
dependency because clusters such as K3s with flannel and kube-router, or
managed clusters with a different policy engine, would be excluded or, worse,
silently unprotected.

The models must also be able to look things up. Hosted provider search runs on
the provider's side of the already approved API connection and needs no new
workspace egress. Direct page fetching and package downloads from the workspace
are broader capabilities that need their own purposes and review.

## Decision

### 1. Route all external workspace egress through one per-workspace proxy

Each workspace namespace gets one egress proxy: a Deployment, a Service, and
its own `NetworkPolicy`, rendered from the same manifests as the workspace by
both the Minikube and Kubernetes adapters. The proxy is a separate pod, not a
sidecar. `NetworkPolicy` is enforced per pod, so a sidecar would share the
workspace pod's egress and the workspace container could bypass it.

The workspace pod's egress policy allows traffic only to the proxy pod
selector on the proxy port. It allows no DNS egress. The proxy address is
supplied to the workspace by the adapter without name resolution; the
workspace fails closed at startup if the address is absent.

The proxy pod's egress policy allows DNS to the cluster resolver and TCP 443
to external addresses. The approved-destination list, not the policy, is the
control at that boundary.

One proxy per workspace is chosen over one shared proxy per cluster. It keeps
purpose resolution isolated to the workspace that declared it, lets the proxy
lifecycle follow the workspace lifecycle, and matches the current one workspace
per cluster limit. A shared proxy is a possible later variation, not a goal.

### 2. Implement the proxy as a PAW-owned CONNECT-only forward proxy

The first implementation is a small Go program built by Nix into a minimal
non-root image, referenced by digest like the core and helper images. It:

- accepts HTTP `CONNECT` only and tunnels TLS end to end; it never terminates
  TLS, so provider credentials and content are never visible to it;
- refuses plain HTTP forwarding and any destination port other than 443;
- allows a destination only when the requested hostname matches exactly an
  entry in the resolved destination list;
- resolves the hostname itself and refuses private, link-local, loopback,
  multicast, cluster, and cloud metadata address ranges, so a listed name
  that resolves inside the cluster or to a metadata endpoint is still denied;
- logs hostname, purpose, and outcome for each request, never request bodies,
  headers, or credentials;
- requires no credentials, holds no Kubernetes identity, and mounts no
  workspace volume; and
- fails closed: a missing, unreadable, or empty destination list denies
  everything.

Any component that satisfies the same contract and passes the same live
probes may replace it. Squid, or a cluster's native FQDN or identity-aware
policy proven no weaker under ADR-005, are acceptable later implementations.
The contract is the boundary; the Go proxy is its first implementation.

### 3. Resolve purposes to destinations at create time

The workspace contract continues to declare purposes. The adapter resolves
each declared purpose to a PAW-owned, reviewed destination list at create
time and writes it to a ConfigMap that the proxy reads. Resolution tables are
part of PAW source, keyed by purpose and provider, and change only through
reviewed commits. Repository content, workspace state, and the running agent
cannot alter the ConfigMap, because the workspace service account has no
write access to it.

The initial purposes are the ones the contract already declares:

- `approved-provider-api`: the selected provider's API and authentication
  endpoints, for example the OpenAI endpoints for Codex, including the device
  authentication flow;
- `selected-git-read`: the hosts of the explicitly selected repositories,
  which today is none, because repositories arrive by streamed bundle.

Hosted provider search needs no purpose of its own: it is a provider-side
feature of the `approved-provider-api` connection.

Purposes for package downloads and general page fetching are not accepted by
this decision. A package purpose would allow the selected registries and
caches. A browse purpose would be operator opt-in, off by default, port 443
only, with a denylist, address-range refusal, and the destination log as its
controls, and it would be recorded as the primary exfiltration channel in the
threat model. Each requires its own review and, for browse, a separate ADR.

### 4. Prove the boundary behaviorally

The environment verifier gains proxy probes, run against a disposable
workspace, that must all hold before a provider profile is considered
available in an environment:

- an allowed hostname connects successfully through the proxy;
- an unlisted hostname is refused by the proxy;
- a listed hostname on a port other than 443 is refused by the proxy;
- a direct connection from the workspace pod that bypasses the proxy is
  dropped by `NetworkPolicy`; and
- DNS resolution from the workspace pod fails.

A known non-enforcing environment remains a required negative lane and must
fail the bypass probe with a precise diagnostic. As with ADR-005, the CNI name
is diagnostic information only.

## Consequences

### Positive

- Provider images can reach their provider through a reviewed, logged,
  hostname-level boundary while the portable base keeps standard resources.
- The same manifests deploy to Minikube and to conforming clusters such as K3s
  or managed Kubernetes; only enforcement verification differs.
- DNS exfiltration from the workspace is closed, not merely narrowed.
- The proxy is a small, auditable PAW component with the same build, digest,
  and contract-test discipline as the other images.

### Negative

- One more pod, image, and set of policies per workspace.
- Exact hostname lists must track provider endpoint changes; an endpoint
  change breaks the provider until the table is updated, by design.
- The proxy cannot inspect inside TLS, so it cannot prevent an agent that has
  legitimate access to an allowed destination from misusing it. Supervised
  permissions and the destination log remain the controls there.
- Service address delivery without DNS depends on adapter mechanics that must
  be validated on each conforming environment.

## Alternatives considered

### Sidecar proxy in the workspace pod

Rejected. Policy applies to the pod, so the workspace container would inherit
the proxy's egress and could connect directly.

### CNI-native FQDN policy as the primary mechanism

Rejected for the portable base under ADR-005. Permitted as an adapter
implementation only where live probes prove it no weaker.

### Traefik or another ingress controller as the boundary

Rejected. Ingress controllers route inbound traffic to known services; they
have no fail-closed forward-proxy mode for outbound hostname allowlisting.

### Squid or Envoy as the first implementation

Deferred. Both can satisfy the contract, but they bring larger images and
configuration surfaces than the boundary needs. They remain valid
replacements under the same probes.

### IP-block allowlists in `NetworkPolicy`

Rejected. Provider addresses change and are shared with unrelated services;
the result would be both brittle and too wide.

## Validation

The implementation is complete when:

1. a provider workspace renders with the proxy, its Service, its ConfigMap,
   and both policies on Minikube and Kubernetes adapters, with contract tests
   over the rendered manifests;
2. the proxy image passes the existing size, layer, non-root, and runtime
   contract checks;
3. the five probes in decision 4 pass on Minikube with an enforcing policy
   implementation, and the bypass probe fails on the non-enforcing lane; and
4. a Codex workspace completes device authentication and one hosted search
   through the proxy with no other egress, recorded in the pilot evidence.

## References

- [Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
- [HTTP CONNECT method](https://www.rfc-editor.org/rfc/rfc9110#name-connect)
- [Codex authentication](https://developers.openai.com/codex/auth)
