# ADR-001: Kubernetes-native collaborative AI workspaces

- Status: Proposed
- Date: 2026-08-20

## Context

Platform engineering work spans multiple repositories and toolchains, including
cloud infrastructure, Terraform/OpenTofu, Kubernetes, application runtimes, and
automation. AI assistance can improve work across these repositories, but a
host-level agent with broad filesystem and credential access has an
unnecessarily large blast radius.

The desired environment must:

- support multiple browser clients collaborating through one T3 backend;
- isolate repositories, credentials, processes, network access, and state;
- compose additional tools and authority as explicit capabilities;
- run locally and in a shared Kubernetes platform without maintaining two
  deployment models;
- preserve reproducible developer tooling; and
- integrate with existing direnv and SOPS practices without treating them as a
  sandbox.

T3 is a stateful execution boundary. Its server owns provider processes,
threads, terminals, Git operations, and filesystem access. Running multiple T3
servers against one state directory can create competing writers. Browser
clients do not need their own backend when collaborating in the same workspace.

## Decision

### 1. The workspace is the isolation unit

Provision a separate workspace for each intended trust and authority boundary.
A workspace may correspond to an engineer, team, task, change, or incident,
depending on the selected profile.

Each workspace contains:

- exactly one T3 server process;
- the Codex provider process started by T3;
- selected repository clones or worktrees;
- an isolated T3 state directory and Codex home;
- a composed development tool environment;
- capability-specific identity and policy; and
- optional persistent storage governed by a workspace lifetime policy.

Multiple authenticated browser clients may connect to the same workspace. T3
server replicas must remain one, and separate T3 servers must not share writable
state.

### 2. Kubernetes is the sole deployment contract

Use one OCI image and one set of Kubernetes resources for both local and remote
execution.

- Minikube is the initial reference implementation for local Kubernetes.
- Another conforming local Kubernetes implementation may be substituted.
- The local container or VM driver is an implementation detail.
- Docker Compose is not maintained as a parallel deployment path.
- Kustomize overlays may express bounded local and remote differences while
  sharing a common base.

Docker and other OCI tooling remain valid ways to build, transport, and execute
the image. Avoiding Docker Compose does not mean avoiding OCI containers.

### 3. Tooling and authority are separate layers

Use a Nix-backed environment to pin command-line tools. Evaluate Devbox as the
developer-facing package, script, plugin, and direnv interface.

Devbox configuration does not define the security boundary. Kubernetes policy
and workspace capabilities define identity, RBAC, mounts, network access,
resource limits, and admission requirements.

A capability module declares at least:

- packages and configuration;
- repository access;
- filesystem mounts;
- allowed network destinations;
- workload identity and Kubernetes RBAC;
- whether operations are read-only, planning, or mutating;
- required human approval points;
- agent instructions; and
- verification tests.

Profiles compose capabilities such as platform read-only access, infrastructure
planning, non-production operations, or explicitly approved production changes.

### 4. Secrets are not inherited by the long-lived agent runtime

Direnv loads and unloads environment variables for a shell. SOPS protects data
at rest. Neither prevents a process from accessing plaintext when that process
runs as a user that can access both encrypted content and a usable decryption
identity.

Therefore:

- do not mount SOPS decryption identities into AI workspaces by default;
- do not launch T3 with a broad environment of decrypted credentials;
- prefer federated workload identity and short-lived credentials;
- scope identities to the selected capability and target environment;
- separate read/plan authority from write/apply authority;
- constrain egress independently of credential scope; and
- keep high-impact approval and credential issuance outside the agent's direct
  control.

Agent instructions prohibiting secret access remain useful defense in depth,
but they are not considered a technical security boundary.

### 5. Repository access is selected, not ambient

Do not expose an entire organization checkout to every workspace. A workspace
request selects a repository bundle, revisions or worktrees, and the required
read/write mode. Persist intentional Git outputs; treat other workspace state as
ephemeral unless a profile explicitly requires persistence.

### 6. Secure defaults are enforced by the platform

The baseline workspace runs non-root with restricted pod security, dropped Linux
capabilities, no privileged mode, no host container socket, bounded resources,
and default-deny network policy. Supervised agent permissions are the default.
More permissive modes require an explicitly stronger isolation profile.

## Consequences

### Positive

- Local and remote environments exercise the same image and Kubernetes model.
- Capabilities become reviewable bundles of tooling and authority.
- Multiple devices can collaborate without creating competing T3 backends.
- Workspace deletion provides a clear revocation and cleanup boundary.
- Teams can adopt reproducible tooling without requiring every contributor to
  author Nix expressions directly.

### Negative

- Local use requires a Kubernetes runtime and more resources than a native
  development shell.
- Some local and remote differences remain, especially identity and ingress;
  overlays and contract tests must keep them bounded.
- A single T3 backend is not highly available within one workspace.
- Interactive collaboration within one workspace shares that workspace's
  effective authority unless finer-grained controls are added externally.
- Kubernetes isolation reduces blast radius but does not make arbitrary agent
  execution intrinsically safe.

## Alternatives considered

### Docker Compose locally and Kubernetes remotely

Rejected as the canonical approach because it duplicates lifecycle, networking,
storage, and policy configuration and creates predictable drift. An OCI builder
or runtime may still use Docker without introducing Compose.

### Native Devbox shell locally

Retained only as a possible trusted fast path, not as the isolation or parity
contract. It cannot reproduce Kubernetes identity, RBAC, network, admission, or
storage controls on its own.

### One shared global T3 backend

Rejected because it creates an excessively broad filesystem and credential
boundary and couples unrelated work. Collaboration occurs within deliberately
scoped workspaces instead.

### One T3 backend per browser client

Rejected for collaborative work because it duplicates execution state and can
produce competing writers for the same provider thread.

## Initial validation

The first proof of concept should demonstrate:

1. one non-production, read-only workspace profile;
2. the same image and Kubernetes base on Minikube and a remote cluster;
3. multiple browser clients continuing one T3 thread without writer conflicts;
4. a deliberately selected repository bundle rather than an ambient host tree;
5. no static plaintext credentials in the image, manifests, pod environment,
   logs, workspace state, or build context;
6. enforced pod, RBAC, resource, and egress restrictions;
7. Terraform/OpenTofu planning and read-only Kubernetes/cloud inspection;
8. reliable expiry, revocation, deletion, and audit behavior; and
9. automated tests for the capability and security contracts.

## Open questions

- Which Git host, organization, repository name, and visibility should own the
  implementation?
- Should Devbox be the canonical tool manifest or a generated developer-facing
  view over Nix modules?
- What creates and expires workspaces: Helm/Kustomize automation, a small
  controller, or an existing internal platform API?
- Which identity broker and approval boundary should be used for local
  development?
- Which state should survive workspace deletion, and for how long?
- What is the smallest representative repository bundle for the pilot?
