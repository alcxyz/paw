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
Image loading, registry authentication, and release provenance remain
environment responsibilities outside the generic lifecycle.

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

Valid v0 profiles are `core` and `platform-readonly`; valid providers are
`none`, `codex`, `claude-code`, and `opencode`. The rendered image and the
StatefulSet and ConfigMap metadata are derived from that exact pair. Released
environments use the generic adapter and immutable image reference instead of
the local development tag.

The initial base remains unreachable because its network policy denies all
traffic. Client-access and egress adapters must add narrowly scoped policy for
their environment. They must never replace the default deny with unrestricted
ingress or egress.

## T3 startup and pairing

The workload uses `t3 start` in authenticated web mode at warning log level.
Unlike `t3 serve`, this avoids writing the automatically issued startup pairing
credential into Kubernetes logs. `paw workspace pair` mints pairing material
inside the selected pod and returns it directly through the operator's
`kubectl exec` stream. The CLI defaults to a ten-minute TTL, rejects TTLs longer
than one hour, and constructs a loopback URL matching `paw workspace connect`.
The connection command binds only `127.0.0.1`; remote adapters must supply their
own authenticated TLS ingress rather than widening this local tunnel.

One StatefulSet replica and one `ReadWriteOnce` claim express the T3
single-writer contract. The development claim is marked ephemeral and is
deleted as a Kubernetes object through the PAW destroy workflow unless a future
profile selects a different, explicit retention policy. Backing-volume
reclamation is a storage-adapter responsibility and is not yet proven here.

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
