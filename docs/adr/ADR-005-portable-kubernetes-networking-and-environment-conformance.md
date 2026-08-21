# ADR-005: Portable Kubernetes networking and environment conformance

- Status: Accepted
- Date: 2026-08-21

## Context

ADR-001 makes Kubernetes the initial PAW deployment contract for M1, while
ADR-006 leaves a deliberate extension point for later runtime backends. The
Kubernetes core must avoid dependencies on a particular cluster or cloud.
ADR-003 uses standard Kubernetes `NetworkPolicy` for default deny and requires
an environment adapter to prove bounded egress.

The first live conformance run used Minikube with Calico because Minikube's
default network did not enforce the policy. That proved the security control,
but it did not make Calico part of PAW's architecture. Kubernetes accepts
`NetworkPolicy` resources even when no installed component enforces them, so
manifest acceptance, API discovery, or a recognized CNI name cannot establish
the isolation boundary.

PAW must also run on environments such as stock K3s and managed Kubernetes
services. These may separate pod networking from policy enforcement, use a
different policy engine, or replace both over the lifetime of a cluster.
Binding PAW to a product name would unnecessarily exclude conforming clusters
and could still give false assurance when that product is disabled or
misconfigured.

Standard `NetworkPolicy` supplies portable layer 3 and layer 4 controls, but it
does not portably express provider domains, identity-aware destinations, or
other layer 7 egress requirements. PAW therefore needs both a portable default
deny and an explicit environment capability for bounded external egress.

## Decision

### 1. Depend on observable capabilities, not networking products

The portable PAW base uses standard `networking.k8s.io/v1` `NetworkPolicy`
resources. It must not contain Calico, Cilium, Flannel, Azure CNI, kube-router,
or other implementation-specific resources.

A Kubernetes environment conforms only when it proves the behavior required by
the selected PAW profile. Installing or reporting a known CNI or policy engine
is diagnostic information, not evidence of enforcement. An environment that
accepts policy resources but fails a behavioral probe is non-conforming and PAW
must fail closed.

### 2. Separate the portable Kubernetes adapter from environment conveniences

The generic Kubernetes adapter owns operations that can be implemented through
the portable base and an explicitly selected Kubernetes context, including
render, create, inspect, connect, repository materialization, pairing,
revocation, and destroy.

Environment-specific adapters or overlays may supply bounded integration such
as:

- local cluster startup and image loading;
- ingress, DNS, authentication, and TLS;
- immutable image registry resolution;
- storage-class and reclamation behavior;
- workload identity and approved egress; and
- policy or observability integrations.

Minikube remains the first local reference environment. Its conveniences must
not be prerequisites for the generic Kubernetes lifecycle. K3s, AKS, and other
distributions may conform without pretending to be Minikube.

### 3. Add a fail-closed environment verifier

The `paw` CLI will provide an explicit environment verification workflow. The
workflow operates only on the Kubernetes context selected by the operator and
uses a uniquely named ephemeral namespace. It must refuse collisions, use
bounded deadlines, avoid credentials and external destinations, and clean up
the resources it created on success or failure.

Network-policy verification uses independent controls:

1. a positive control proves that the test path works without isolation;
2. a selected workload proves that default-deny egress blocks the same path;
3. a positive ingress path proves that the test service is reachable; and
4. a selected workload proves that default-deny ingress blocks the same path.

A failed positive control is an inconclusive or broken environment, not a
successful denial. A successful negative connection is an isolation failure.
Results identify the contract and probe versions, selected context, Kubernetes
server version, time, and outcomes without recording credentials, payloads, or
sensitive cluster inventory.

Verification is evidence about the tested environment state, not a permanent
certificate. It must be repeated after material cluster, CNI, policy-engine, or
PAW contract changes. Profiles may require further capability-specific probes
beyond the baseline network-policy check.

### 4. Keep bounded external egress behind an adapter capability

All profiles retain portable default-deny egress. Provider, Git, package, cloud,
and Kubernetes API access is allowed only for declared purposes resolved by an
environment adapter.

The portable implementation should route external traffic through a controlled
egress proxy or gateway that can enforce the approved destinations while
standard `NetworkPolicy` permits workspace traffic only to that boundary.
Environment-native FQDN or identity-aware policy may implement the same contract
when live positive and negative controls prove that it is no weaker.

PAW must not require a CNI-specific policy CRD, silently fall back to
unrestricted internet access, or claim provider readiness from default-deny
verification alone. Selection and detailed validation of the first bounded
egress implementation remains explicit follow-up work.

### 5. Maintain a behavioral portability matrix

The initial matrix exercises the same released image, portable Kubernetes base,
and conformance contract on:

- Minikube with an enforcing policy implementation as the development reference;
- stock K3s with its default networking and policy components; and
- one non-production managed AKS environment.

A non-enforcing environment remains a required negative test. Environment
documentation records tested versions and exceptions, but product names are not
added to the portable profile contract.

## Consequences

### Positive

- PAW can support any Kubernetes distribution that proves the contract.
- A CNI or policy-engine change does not require changes to the portable base.
- Live controls detect the dangerous case where policy objects exist but have
  no effect.
- Local K3s and managed environments exercise the same workload boundary.
- CNI-native features remain available without becoming universal
  dependencies.

### Negative

- Environment qualification requires temporary Kubernetes writes and active
  network probes rather than static inspection alone.
- Conformance results expire operationally when relevant cluster components
  change.
- Precise external egress requires a proxy, gateway, or environment-native
  mechanism in addition to standard `NetworkPolicy`.
- Maintaining multiple conformance lanes adds test infrastructure and release
  time.

## Alternatives considered

### Require Calico everywhere

Rejected because Calico is only one valid enforcement implementation. This
would exclude otherwise conforming clusters and would not prove that policy is
actually enforced.

### Trust the CNI name or cluster configuration

Rejected because configuration can drift from runtime behavior and pod
networking may be separate from policy enforcement. Capability discovery is
useful diagnostics but not a security test.

### Use CNI-specific policy resources in the portable base

Rejected because they couple profiles to one implementation. An environment
adapter may use stronger native controls only when it also preserves and proves
the portable invariants.

### Permit unrestricted egress on implementations without FQDN policy

Rejected because it violates ADR-003's default-deny and purpose-bound egress
contract. Lack of an enforcement mechanism makes a profile unavailable in that
environment.

## Validation

The implementation work is complete when:

1. the generic Kubernetes lifecycle no longer depends on Minikube-specific
   behavior;
2. the environment verifier distinguishes positive-control failure from policy
   enforcement failure and cleans up its ephemeral resources;
3. a known non-enforcing environment fails with a precise diagnostic;
4. Minikube with enforcement and stock K3s pass the same baseline probes;
5. a non-production AKS environment is qualified independently of its reported
   networking product; and
6. bounded provider egress passes both approved-destination and denied-
   destination tests without unrestricted fallback.

## References

- [Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
- [K3s networking services](https://docs.k3s.io/networking/networking-services)
- [AKS network-policy options](https://learn.microsoft.com/azure/aks/use-network-policies)
