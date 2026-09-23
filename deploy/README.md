# PAW Kubernetes deployment

The resources in `base` are the portable workspace contract. Environment
adapters compose that base and supply a namespace, an immutable released image,
client access, concrete egress destinations, storage behavior, and any workload
identity required by the selected profile.

The base depends on standard Kubernetes APIs and verified behavior, not Calico,
Cilium, Flannel, Azure CNI, or another named implementation. An adapter may use
environment-native networking features, but it must preserve the portable
default deny and pass live positive and negative controls. See
[ADR-005](../docs/adr/ADR-005-portable-kubernetes-networking-and-environment-conformance.md).

The base deliberately uses an invalid registry and zero digest. This prevents a
bare base from silently pulling an unreviewed `latest` image. Released adapters
replace it with an immutable image digest. The Minikube adapter uses the local
development image name `paw-core:dev`; it is not a release manifest.

Render the portable or local resources with:

```sh
kubectl kustomize deploy/base
kubectl kustomize deploy/adapters/kubernetes
kubectl kustomize deploy/adapters/minikube
```

The checked-in portable base and generic adapter deliberately retain the
invalid zero-digest image placeholder and are safe to inspect but not deploy.
The CLI replaces that placeholder only when the operator supplies an immutable
profile-matching released image:

```sh
paw workspace render --adapter kubernetes \
  --profile platform-readonly --provider opencode \
  --image-ref "$PAW_IMAGE_REF"
paw workspace create --adapter kubernetes --context CONTEXT \
  --profile platform-readonly --provider opencode \
  --image-ref "$PAW_IMAGE_REF"
```

The reference must be fully qualified, use `@sha256:…` rather than a mutable
tag, and end with the reviewed image name selected by the profile/provider pair.
The generic adapter also requires `--egress-image-ref`, an immutable
`paw-egress-proxy` image for the bounded egress proxy described below.
Image loading, registry authentication, and release provenance remain
environment responsibilities outside the generic lifecycle.

## Host-driven Git transport

Bundle import creates a workspace copy; after that the host talks to it as a
Git remote over `kubectl exec`, with the host's own Git identity and no remote,
credential, or network inside the workspace
([ADR-018](../docs/adr/ADR-018-host-driven-git-transport.md)):

```sh
git config --global protocol.ext.allow user   # Git disables ext:: by default
git remote add paw "ext::paw workspace repository remote --adapter minikube \
  --context minikube --name paw --service %S"
git push paw dev      # host commits into the workspace copy, on a branch
git fetch paw         # workspace commits back to the host
```

Only `git-upload-pack` and `git-receive-pack` are tunnelled. Pushes land on
branches; Git refuses to update the branch the workspace has checked out, so a
push never rewrites a working tree an agent is editing. Publishing to GitHub or
Forgejo stays a host action after review.

## Provider login state

The workspace pod mounts the retained `workspace-session` claim as the
provider state directories under `/workspace/session` (`CODEX_HOME`,
`CLAUDE_CONFIG_DIR`). A subscription login made inside the pod lasts as long as
the provider allows: it survives pod replacement, upgrades, and restores, and
it ends only with logout or workspace destruction. The claim is never mounted by
the backup helper, so it is never in a backup, never restored, and never in a
manifest or on the operator host
([ADR-017](../docs/adr/ADR-017-retained-provider-session-claim.md), amending
[ADR-014](../docs/adr/ADR-014-pod-scoped-provider-login-state.md)). It lives only
for that pod's lifetime and never reaches a persistent claim, a backup, a
manifest, or the operator host
([ADR-014](../docs/adr/ADR-014-pod-scoped-provider-login-state.md)).

```sh
paw workspace login --adapter minikube --context minikube --provider codex
paw workspace logout --adapter minikube --context minikube --provider codex
```

Login runs the provider's own device or code login inside the pod through an
interactive exec; the operator finishes the browser step on their own machine.
The provider must match the one recorded on the workspace, or be a member of an
`all` workspace.

## Bounded egress

Every workspace namespace runs one egress proxy
([ADR-013](../docs/adr/ADR-013-per-workspace-bounded-egress-proxy.md)): the
`egress` Deployment, Service, ServiceAccount, and `egress-destinations`
ConfigMap in `base/egress.yaml`. Workspace pods keep the default deny and gain
exactly one egress rule, to the proxy port; they have no DNS egress. The proxy
address reaches the T3 container through Kubernetes service links as
`HTTPS_PROXY`, so provider tools that honor that variable use the proxy without
any name resolution.

The proxy accepts only HTTP `CONNECT` to port 443 for hostnames listed in the
ConfigMap, resolves them itself, refuses private, link-local, loopback, and
metadata address ranges, and logs hostname and outcome only. The CLI resolves
the selected provider's declared purposes to that list at render time from a
reviewed table in `deploy/egress.go`; `--provider none` renders an empty list
and the proxy denies everything. The proxy pod may reach the cluster resolver
and public TCP 443 only. Minikube binds the local `paw-egress-proxy:dev` image.

The PAW CLI binds one reviewed local image composition rather than accepting an
arbitrary image name:

```sh
paw workspace render --adapter minikube \
  --profile platform-readonly --provider opencode
paw workspace create --adapter minikube --context minikube \
  --profile platform-readonly --provider opencode
paw workspace inspect --adapter minikube --context minikube
paw workspace connect --adapter minikube --context minikube
paw workspace pair --adapter minikube --context minikube \
  --ttl 10m --label browser
paw workspace revoke --adapter minikube --context minikube --pairing-id ID
```

Valid v0 profiles are `core`, `developer`, and `platform-readonly`; valid
providers are `all` (Codex, Claude Code, and OpenCode together) or one of
`none`, `codex`, `claude-code`, and `opencode`. The rendered image and the
StatefulSet and ConfigMap metadata are derived from that exact pair. Released
environments use the generic adapter and immutable image reference instead of
the local development tag.

The initial base remains unreachable because its network policy denies all
traffic. Client-access and egress adapters must add narrowly scoped policy for
their environment. They must never replace the default deny with unrestricted
ingress or egress.

Creation first runs baseline network verification, atomically creates the
PAW-owned namespace, then applies the workload and waits for readiness. It
refuses an existing namespace. Failed application or readiness retains partial
resources for inspection rather than claiming a usable workspace.

The Minikube adapter requires images to be preloaded (`imagePullPolicy: Never`).
The generic adapter pulls the requested immutable image when needed. Workspace
deletion checks the namespace ownership label and uses UID/resource-version
preconditions, then waits for namespace deletion. Legacy namespaces lacking
the ownership marker require operator-reviewed cleanup. These safeguards do
not prove backing-volume reclamation; that remains storage-adapter work.

## T3 startup and pairing

The workload uses `t3 start` in authenticated web mode at warning log level.
Unlike `t3 serve`, this avoids writing the automatically issued startup pairing
credential into Kubernetes logs. `paw workspace pair` mints pairing material
inside the selected pod and returns it directly through the operator's
`kubectl exec` stream. The CLI defaults to a ten-minute TTL, rejects TTLs longer
than one hour, and constructs a loopback URL matching `paw workspace connect`.
The connection command binds only `127.0.0.1`; remote adapters must supply their
own authenticated TLS ingress rather than widening this local tunnel.

One StatefulSet replica and `ReadWriteOnce` claims express the T3
single-writer intent; the access mode alone does not prevent two pods on one
node from writing. New `persistent-v2` workspaces have separate state and work
claims, both retained across pod replacement until explicit workspace destruction.
Only temporary files use `emptyDir`. Existing ephemeral repository volumes are
not migrated automatically. Persistent storage is not a backup; see
[the staged upgrade contract](../docs/upgrades.md). Backing-volume reclamation
is a storage-adapter responsibility and is not yet proven here.

The manifest contract rejects host namespaces, host paths and ports, runtime
sockets, device mounts, added capabilities, sysctls, host aliases, secret or
service-account-token projections, all CSI volumes, undeclared environment
variables, and `envFrom`. It also rechecks restricted execution and the absence
of Secret objects for every reviewed profile/provider render.

`scripts/check-minikube-live.sh` complements those structural checks with an
explicitly enabled live run. It refuses an existing `paw-workspace` namespace
and proves the ephemeral create/inspect/connect/pair/revoke/destroy workflow,
runtime UID, absent token and socket mounts, empty effective RBAC, enforced
egress denial, streamed repository selection, clean logs, and
namespace/PVC-object deletion. It does not yet claim backing-volume
reclamation. The target cluster must use a CNI that enforces NetworkPolicy and
must already contain the selected development image. The current script uses
Minikube as a reference environment; it does not make Minikube or its selected
network-policy implementation part of the portable deployment contract.

The opt-in script also checks persistence on its own newly created workspace:
it writes harmless state and repository fixtures, scales the writer to zero,
waits for pod deletion, restarts it, and checks unchanged claim identities and
preserved tracked, staged, untracked, and ignored content. This is a test path,
not an upgrade or migration command; it refuses an existing namespace. Adding
the test is not evidence of a live pass or of real T3 thread restoration.

`paw environment verify --context CONTEXT` is the portable preflight for that
contract. It creates only a randomly named restricted namespace, proves the
future selected ingress and egress paths before policy, applies standard
`networking.k8s.io/v1` default-deny probes, and requires both paths to become
unreachable. It uses a digest-pinned Kubernetes `agnhost` image and direct pod
addresses, so the result does not depend on public endpoints or cluster DNS.
The namespace is deleted with a separate bounded cleanup context even when the
main run is interrupted. A nonzero result means the environment is not
qualified; it must not be converted into a warning or unrestricted fallback.
