# PAW — Platform AI Workspace

PAW is a portable, isolated collaboration environment for AI-assisted platform
engineering. It combines a headless T3 Code server, multiple coding-agent
providers, selected repositories, reproducible tooling, and explicit authority
inside a Kubernetes workspace.

PAW is intended to run on a local Kubernetes implementation or a remote
self-managed, on-premises, or managed cluster without maintaining a separate
deployment model for each environment.

> PAW is in early implementation. The CLI and reproducible build foundation
> exist, but the workspace runtime is not ready for operational use. Accepted
> decisions live in
> [docs/adr](docs/adr/README.md).

## Goals

- Let multiple authenticated browser clients collaborate through one workspace.
- Support Codex, Claude Code, and GitHub Copilot through T3 provider adapters.
- Expose only the repositories and authority selected for a task.
- Use the same OCI image and Kubernetes resources locally and remotely.
- Compose tooling and policy into reviewable capability profiles.
- Keep provider credentials, decryption identities, and broad host access out of
  workspace images.
- Make normal operation approachable through a portable `paw` CLI.

## Architecture

```text
browser clients
      │
authenticated ingress
      │
┌─────▼────────────────────────────────────────┐
│ PAW workspace                               │
│                                             │
│ one T3 server                               │
│ ├── Codex CLI                               │
│ ├── Claude Code CLI                         │
│ └── OpenCode CLI → GitHub Copilot           │
│                                             │
│ selected repositories and worktrees         │
│ capability-specific tools and authority      │
│ isolated provider and workspace state        │
└─────────────────────────────────────────────┘
```

One workspace has exactly one T3 server and one writer for its state. Multiple
clients may connect to that server. Separate T3 servers must not share writable
state.

## Layers

### Go CLI

`paw` is the user and operator interface. It will create, inspect, connect to,
pair with, and delete workspaces while hiding cluster-specific mechanics.
Ordinary users should be able to run released binaries and images without Nix.

The initial foundation provides:

```text
paw version
paw doctor
paw profile list
```

### Nix flake

The flake is the contributor build and composition interface. It will pin and
produce:

- the `paw` Go CLI;
- a dedicated headless T3 package without Electron or desktop resources;
- multi-architecture workspace images;
- provider CLIs and platform tools;
- contributor development shells;
- evaluated profile metadata; and
- build, module, image, and contract checks.

Workspace images contain only required runtime closures. They do not require a
Nix daemon or a general-purpose Nix installation at runtime.

Images are composed from a small headless core, provider layers, and
capability-specific tooling. Codex, Claude Code, OpenCode, Azure tooling, and
other large dependencies are included only when selected by a released profile.
CI measures closure and OCI sizes and enforces budgets established from the first
optimized images.

### Kubernetes

Kubernetes is the sole deployment contract. A portable core uses standard APIs,
while explicit adapters provide local-cluster lifecycle, ingress and identity,
storage, registry, Git hosting, policy, and observability integration.

Minikube is the initial local reference adapter, not a PAW dependency. Docker or
another OCI runtime may build, transport, or execute images, but PAW does not
maintain a parallel Docker Compose deployment.

### Capability profiles

Profiles compose tools and authority rather than treating installed binaries as
permissions. The first planned profiles are:

- `core`: T3, provider CLIs, Git, shell utilities, and workspace lifecycle;
- `platform-readonly`: selected repositories, infrastructure planning, and
  read-only Kubernetes and cloud inspection.

A capability declares its packages, repository access, mounts, network policy,
identity requirements, RBAC, approval points, agent instructions, and tests.

## Provider model

The initial provider targets are:

| Experience | T3 provider |
| --- | --- |
| Codex | Codex CLI |
| Claude Code | Claude Code CLI |
| GitHub Copilot | OpenCode CLI |

Provider binaries may be present in a shared image, but authentication state is
attached separately. Personal credentials must never be baked into an image or
silently shared by a team workspace.

## Security boundary

PAW assumes agent instructions are defense in depth, not access control. The
workspace must technically restrict what an agent can read and do.

The baseline design includes:

- selected repository mounts instead of ambient host access;
- no SOPS decryption identity in the workspace by default;
- short-lived, narrowly scoped workload identity;
- separation of read/plan and write/apply authority;
- non-root execution and restricted pod security;
- no privileged mode or host container socket;
- default-deny network policy and bounded resources; and
- external approval for high-impact credential issuance and changes.

## Repository status

The project currently contains its architecture, agent safety rules, initial Go
CLI, and reproducible flake checks. The repository structure and planned areas
are:

```text
cmd/paw/              Go CLI entry point
internal/             CLI and adapter implementation
nix/modules/          PAW build-time modules
nix/profiles/         Composed workspace profiles
deploy/base/          Portable Kubernetes resources
deploy/adapters/      Environment-specific adapters
docs/adr/             Architecture decisions
```

Start with:

- [ADR-001: Kubernetes-native collaborative AI workspaces](docs/adr/ADR-001-kubernetes-native-collaborative-ai-workspaces.md)
- [ADR-002: Go CLI and Nix flake build architecture](docs/adr/ADR-002-go-cli-and-nix-flake-build-architecture.md)

## Development

Enter the pinned contributor environment with direnv or Nix:

```sh
direnv allow
# or
nix develop
```

Run the local and flake checks:

```sh
go test ./...
go vet ./...
nix flake check
```

Build or run the CLI:

```sh
nix build .#paw
nix run .#paw -- profile list
```

## Sensitive material

This repository is private, but credentials, decrypted configuration, internal
access details, and operational transcripts must still not be committed. See
[AGENTS.md](AGENTS.md) for the repository's agent and secret-handling rules.
