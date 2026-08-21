# ADR-003: PAW v0 capability contract and threat model

- Status: Accepted
- Date: 2026-08-21

## Context

ADR-001 establishes the workspace as PAW's isolation unit and requires security
controls outside agent instructions. It leaves the minimum adapter contract,
trust boundaries, and first profile authority ceilings open for implementation.

The first proof of concept needs enough precision to reject an unsafe deployment
without prematurely selecting a production identity broker, ingress controller,
storage class, cloud, Git host, or Kubernetes distribution. The contract also
needs a machine-readable form so Nix profiles, the Go CLI, Kubernetes adapters,
and conformance tests can consume the same security-relevant facts.

This threat model covers the PAW workspace boundary and its direct integrations.
It does not claim that Kubernetes, a provider, or a host is immune to compromise.

## Decision

### 1. Publish one versioned contract

The canonical v0 contract is [contract/v0.json](../../contract/v0.json). It
defines:

- ordered authority classes;
- the security-relevant properties of the `core` and `platform-readonly`
  profiles;
- trust boundaries;
- required environment adapters;
- invariants with expected enforcement boundaries; and
- explicit non-goals.

The document contains no endpoints, credential values, repository names,
identity-provider details, or cluster-specific configuration. Environment
adapters bind its declared purposes to concrete implementations.

The Go contract package embeds and validates the document. Nix profile outputs
and deployment checks consume the same file rather than independently restating
the authority classes.

### 2. Treat authority as a ceiling

An authority class is the maximum authority a workspace may receive. Installed
tools do not grant authority, and input from a browser, repository, model, or
provider process cannot raise the selected ceiling.

| Class | Local changes | Inspection | Mutation | Production |
| --- | --- | --- | --- | --- |
| `workspace-only` | Yes | Git and provider | No | No |
| `read-only` | Yes | Non-production | No | No |

Local changes include editing an ephemeral clone and creating plans or patches.
External mutation includes Git push, Terraform apply, Kubernetes writes, and
cloud resource changes. Provider requests, client-session lifecycle, audit
records, and workspace lifecycle metadata are necessary control-plane effects;
they do not authorize mutations to repositories or platform resources.

The `core` profile is capped at `workspace-only`. It may use an approved
provider session, read explicitly selected repositories, and modify workspace
files, but it receives no platform identity and cannot push to a remote.

The `platform-readonly` profile is capped at `read-only`. It adds planning and
inspection of explicitly selected non-production platform targets. Read-only
does not imply that inspected data is safe to send to a model; repository and
data-selection policy remains an independent obligation.

### 3. Define the trust and data-flow boundaries

The main execution path is:

```text
authenticated browser
        │
client-access adapter
        │
one T3 server ── provider CLI ── approved provider API
        │
selected repositories and isolated state
        │
scoped workload identity ── approved non-production APIs
```

The browser is authenticated but remains an untrusted command source. It may
hold a revocable PAW client session, but it never receives repository,
workload, or platform credentials.

T3 and its provider subprocesses share one workspace authority boundary. A
provider process is not a security boundary from T3, and a separate T3 server
must not write the same state. Multiple browser clients collaborate through the
one server.

Repository content is untrusted input. It can influence an agent but cannot
select mounts, attach identities, alter egress, change RBAC, or choose a
stronger profile.

The Kubernetes control plane enforces pod security, resource limits, workload
identity, RBAC, storage attachment, and network policy. A workspace service
account cannot modify those enforcement resources.

External Git, provider, package, Kubernetes, and cloud services are outside the
workspace. Each connection requires a declared purpose, an adapter-approved
destination, and separately scoped authentication where required.

### 4. Require explicit environment adapters

A conforming v0 environment supplies these capabilities:

- `kubernetes-workspace`: standard workload, service, configuration, storage,
  RBAC, and network-policy APIs; restricted non-root execution; bounded
  resources; and exactly one T3 server for a writable T3 state boundary;
- `client-access`: authenticated encrypted browser access, revocable sessions,
  and no unauthenticated exposure of T3;
- `repository-source`: explicit repository and revision selection, read-only
  remote credentials, and no ambient host checkout;
- `provider-authentication`: runtime-attached, revocable provider state that is
  absent from builds and profile metadata;
- `workspace-storage`: isolated state with declared single-writer, retention,
  expiry, deletion, and backup behavior;
- `identity-and-egress`: default-deny egress, purpose-to-destination resolution,
  and short-lived read-only workload identity when platform access is enabled;
  and
- `lifecycle-and-audit`: create, inspect, connect, revoke, expire, and destroy
  operations with metadata-only, credential-free audit records.

Kubernetes `NetworkPolicy` supplies the portable default deny. Because standard
policy does not describe every DNS or identity-aware egress mechanism, the
environment adapter must prove that it can enforce each resolved destination or
reject the profile. An adapter cannot silently replace bounded egress with
unrestricted internet access. ADR-005 defines behavioral environment
verification and keeps individual networking implementations outside the
portable contract.

### 5. Enforce v0 invariants at independent boundaries

The contract defines six required invariants:

- one writer for each T3 state boundary;
- explicit repository selection;
- no ambient credentials;
- default-deny egress;
- restricted pods without host or container-runtime access; and
- monotonic authority that workspace-controlled input cannot increase.

Controls are deliberately layered. For example, agent instructions prohibit
secret access, image and manifest tests detect accidental inclusion, runtime
identity avoids static credentials, and egress policy limits exfiltration. No
single one of those controls is treated as sufficient.

### 6. Make v0 non-goals explicit

PAW v0 does not provide:

- production access or infrastructure mutation;
- remote Git push or pull-request publication from a workspace;
- high availability for an individual T3 server;
- isolation between mutually distrusting users sharing one workspace;
- ambient host files, credentials, or organization-wide checkouts;
- secret decryption or a general credential broker inside the agent runtime; or
- confidentiality for data intentionally sent to an approved AI provider.

Future profiles that add mutation or production access require a new accepted
ADR, stronger approval boundaries, new contract classes, and negative tests.

## Threat analysis

- **Stolen pairing material or browser session.** Authenticated TLS access,
  short-lived pairing, revocation, and metadata-only audit reduce exposure. A
  live client can still act as the user until revoked, so conformance tests
  cover expiry and revocation.
- **Competing T3 writers corrupt or lock state.** One server replica,
  single-writer storage, and isolated server state prevent planned overlap.
  Process or node failure still interrupts a workspace, so tests cover resume
  and replacement without overlap.
- **Prompt or repository injection escalates authority.** Operator-selected
  immutable profiles, immutable RBAC and mounts, and supervised permissions
  prevent input from granting authority. An agent can still misuse authority
  already granted, so policy tests attempt forbidden operations.
- **Credentials leak through files, environment, logs, or images.** Runtime
  attachment, no decryption identity, scans, redacted audit, and bounded egress
  reduce exposure. Provider tools may persist their own session state; each
  provider and retention path requires inspection.
- **A workspace reaches the host or another workload.** Restricted non-root
  pods, dropped capabilities, and no host namespaces, paths, privilege, or
  runtime socket limit lateral access. Kernel and cluster-runtime
  vulnerabilities remain outside PAW's application controls.
- **A read-only identity mutates a platform.** Provider-side IAM and Kubernetes
  RBAC deny writes independently of CLI behavior. Misclassified API actions or
  external IAM drift remain possible, so conformance includes negative API
  tests.
- **Unbounded network access enables exfiltration.** Portable default deny and
  adapter-resolved destinations restrict egress. Endpoint and CDN changes can
  break or widen policy, so adapters fail closed and require retesting.
- **A malicious build input enters an image.** Pinned Nix inputs, focused
  closures, image inventories, and digest references make inputs reviewable.
  Upstream source compromise remains possible; release provenance and
  vulnerability policy are follow-up work.
- **Deleted workspaces leave usable identity or state.** Revocation before
  deletion, declared retention, isolated volumes, and lifecycle audit close the
  local boundary. External provider sessions still need provider-specific
  revocation conformance.

## Resolution of ADR-001 open questions

- The minimum Kubernetes and adapter capabilities are defined here and in the
  v0 machine contract.
- Portable lifecycle and the Minikube implementation remain issue #5. A
  controller is not required for the first proof of concept.
- The local identity broker and approval boundary remain adapter choices. Issue
  #7 must prove the v0 read-only boundary without embedding an implementation in
  core.
- Storage must declare persistence and retention behavior. Issue #5 implements
  it, and issue #8 validates expiry, revocation, deletion, and selected
  persistence.
- Provider authentication must be attached at runtime. Provider-specific
  behavior remains issues #4 and #8.
- The representative repository bundle is an explicit non-production selection
  under issue #6 and cannot widen the profile.

## Consequences

### Positive

- Build, CLI, deployment, and conformance work share a versioned vocabulary.
- Local and remote adapters can differ without weakening portable invariants.
- The first profiles have clear, testable authority ceilings.
- Repository or model input cannot legitimately request more authority.
- Unresolved provider and environment choices remain explicit adapter work.

### Negative

- Environments need more than syntactically valid Kubernetes manifests to
  claim conformance.
- Read-only provider, Git, and platform identities require separate validation.
- Standard Kubernetes network policy may need an additional environment egress
  mechanism for precise external destinations.
- The v0 prohibition on remote Git push requires patches to leave the workspace
  through an operator-controlled path.

## Validation

The Go package rejects contracts that:

- omit or alter the required authority ceilings;
- grant external mutation or production access;
- permit ambient repository selection or remote Git push;
- disable default-deny egress;
- reference unknown authority classes; or
- omit required security invariants and enforcement boundaries.

Later deployment issues add manifest and live-cluster tests for the same
invariants. Passing the document validator alone is not a security claim.

The initial image contracts allow only `HOME`, `PATH`, `TMPDIR`, and the three
declared XDG directory variables in image configuration. The initial manifest
contract renders every reviewed profile/provider pair and rejects host/runtime
access, privilege additions, static secret attachment paths, and
credential-shaped environment variables. These structural checks do not
replace the live identity, egress, state, log, revocation, or provider
conformance tests required by issues #7 and #8.
