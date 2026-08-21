# PAW Kubernetes deployment

The resources in `base` are the portable workspace contract. Environment
adapters compose that base and supply a namespace, an immutable released image,
client access, concrete egress destinations, storage behavior, and any workload
identity required by the selected profile.

The base deliberately uses an invalid registry and zero digest. This prevents a
bare base from silently pulling an unreviewed `latest` image. Released adapters
replace it with an immutable image digest. The Minikube adapter uses the local
development image name `paw-core:dev`; it is not a release manifest.

Render the portable or local resources with:

```sh
kubectl kustomize deploy/base
kubectl kustomize deploy/adapters/minikube
```

The PAW CLI binds one reviewed local image composition rather than accepting an
arbitrary image name:

```sh
paw workspace render --adapter minikube \
  --profile platform-readonly --provider opencode
paw workspace create --adapter minikube --context minikube \
  --profile platform-readonly --provider opencode
```

Valid v0 profiles are `core` and `platform-readonly`; valid providers are
`none`, `codex`, `claude-code`, and `opencode`. The rendered image and the
StatefulSet and ConfigMap metadata are derived from that exact pair. Released
remote adapters replace the local development tag with an immutable digest.

The initial base remains unreachable because its network policy denies all
traffic. Client-access and egress adapters must add narrowly scoped policy for
their environment. They must never replace the default deny with unrestricted
ingress or egress.

## T3 startup and pairing

The workload uses `t3 start` in authenticated web mode at warning log level.
Unlike `t3 serve`, this avoids writing the automatically issued startup pairing
credential into Kubernetes logs. PAW will mint short-lived pairing material on
demand through an authenticated operator action and return it directly to the
requesting user.

One StatefulSet replica and one `ReadWriteOnce` claim express the T3
single-writer contract. The development claim is marked ephemeral and is
deleted through the PAW destroy workflow unless a future profile selects a
different, explicit retention policy.

The manifest contract rejects host namespaces, host paths and ports, runtime
sockets, device mounts, added capabilities, sysctls, host aliases, secret or
service-account-token projections, credential-shaped environment variables,
and `envFrom`. It also rechecks restricted execution and the absence of Secret
objects for every reviewed profile/provider render.
