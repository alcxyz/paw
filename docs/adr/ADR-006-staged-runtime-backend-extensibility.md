# ADR-006: Staged runtime-backend extensibility

- Status: Accepted
- Date: 2026-08-22
- Amends: ADR-001 section 2

## Context

ADR-001 selected Kubernetes as PAW's sole deployment contract so the first
implementation would not drift between independently maintained local and
remote definitions. That constraint remains useful for the first secure
workspace proof of concept, but making Kubernetes a permanent architectural
requirement adds unnecessary local complexity and prevents PAW from later using
a simpler container runtime where its guarantees can be enforced.

The concepts also need clearer boundaries. Kubernetes is a runtime backend.
Minikube, K3s, AKS, and other distributions are environments that provide that
backend. Ingress, identity, storage, egress, registry, and local image loading
are integrations within an environment; they are not independent workspace
architectures.

Docker or Docker Compose may eventually be useful for a low-friction local
workspace, but implementing or selecting that path now would distract from the
Kubernetes conformance and security work required for M1. PAW must leave a
deliberate extension point without claiming support or weakening controls.

## Decision

### 1. Make the workspace contract authoritative

PAW's profiles, authority ceilings, selected repositories, image composition,
single-writer rule, lifecycle, and required security outcomes define a
backend-neutral workspace request. A deployment representation such as
Kubernetes resources or a future Compose model is an implementation of that
request, not the source of truth.

Backend-specific controls remain explicit. PAW will not reduce the contract to
the lowest common denominator or treat similarly named controls as equivalent
without behavioral evidence.

### 2. Distinguish runtime backends from environment adapters

A **runtime backend** translates a workspace request into workload, storage,
network, resource, and lifecycle primitives. The only implemented backend in
M1 is `kubernetes`.

An **environment adapter** supplies bounded integration required by a backend
in one environment. For the Kubernetes backend this includes Minikube local
image behavior and may later include K3s, AKS, ingress, identity, storage,
registry, egress, and observability integration.

The current `--adapter kubernetes` and `--adapter minikube` CLI remains stable
for M1. `minikube` is understood as a compatibility facade over the Kubernetes
backend plus Minikube environment behavior. A later CLI contract may expose
backend and environment as separate selections, but that change requires its
own design and migration plan.

### 3. Keep M1 Kubernetes-focused

ADR-001's single Kubernetes deployment model remains the implementation scope
for M1. Current Kubernetes lifecycle, policy, conformance, and portability work
must not be delayed by a Docker or Compose implementation.

Code and contracts introduced during M1 should avoid making Kubernetes resource
types part of otherwise generic profile, repository-selection, provider,
workspace-identity, and lifecycle request models. Kubernetes-specific rendering
and enforcement belong behind the Kubernetes backend boundary. Existing v0
contract names and CLI flags may remain Kubernetes-specific until a second
backend makes migration concrete.

### 4. Defer Docker and Compose as a second backend

Docker/Compose is a candidate future local backend, not an accepted or
implemented deployment path. Before it can be supported, a follow-up decision
must define:

- whether PAW drives the Docker API, generates Compose, or does both;
- which profiles and capabilities the backend can safely support;
- equivalents for non-root execution, read-only filesystems, dropped
  capabilities, resource limits, single-writer storage, selected repository
  materialization, client access, and default-deny networking;
- how purpose-bound egress, identity, audit, retention, and revocation work;
- which host file references and runtime features PAW rejects; and
- live positive and negative conformance tests.

PAW must fail closed when a profile requires a capability that a selected
backend cannot prove. Backend support does not imply that every profile is
available on that backend.

### 5. Do not maintain independent handwritten deployment models

If another backend is added, its artifacts are derived from the same reviewed
workspace and capability contract. PAW will not require operators to keep an
independent Compose definition synchronized manually with Kubernetes resources.
Generated artifacts may be inspectable and exportable, but backend conformance
tests decide whether they implement the declared outcomes.

## Consequences

### Positive

- Kubernetes remains the focused, production-representative implementation for
  M1.
- A simpler local backend can be added later without overturning the workspace
  model.
- Minikube and K3s are correctly treated as Kubernetes environments rather than
  competing runtime architectures.
- Security claims remain capability- and evidence-based instead of assuming
  feature parity between runtimes.
- A shared contract avoids two manually synchronized deployment definitions.

### Negative

- The v0 CLI and machine-readable contract retain Kubernetes-specific names
  during the transition.
- A future second backend will require explicit interface, migration, and
  conformance work.
- Some profiles may remain Kubernetes-only if another backend cannot enforce
  their identity, networking, or policy requirements.
- Backend-neutral domain boundaries add design constraints before they provide
  a second working implementation.

## Alternatives considered

### Implement Docker Compose alongside Kubernetes now

Rejected for M1 because it would divide effort before the initial security and
environment conformance contract is complete.

### Keep Kubernetes as a permanent requirement

Rejected because it imposes cluster lifecycle and resource overhead on every
local workspace even when a container runtime could prove the selected
profile's guarantees.

### Treat Minikube, K3s, and Docker as peer adapters

Rejected because it mixes runtime primitives with environment integration and
would duplicate Kubernetes lifecycle behavior across distributions.

### Promise identical capabilities on every backend

Rejected because similarly shaped runtime features do not establish equivalent
security boundaries. Profiles are available only where their requirements are
verified.

## Validation

This decision is reflected when:

1. M1 documentation identifies Kubernetes as the currently implemented backend,
   not the only backend PAW may ever support;
2. Kubernetes distributions and integrations remain environment adapters or
   compatibility facades rather than duplicate lifecycle implementations;
3. generic workspace-domain changes avoid unnecessary Kubernetes resource
   coupling;
4. the tracker defers second-backend design and implementation beyond M1; and
5. any future backend is accepted only with a capability matrix and live
   conformance evidence.
