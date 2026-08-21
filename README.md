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
paw profile show platform-readonly
paw profile show platform-readonly --json
paw workspace render --adapter minikube --profile core --provider codex
paw workspace create --adapter minikube --context minikube \
  --profile platform-readonly --provider opencode
paw workspace destroy --adapter minikube --context minikube --delete-state
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
The versioned [v0 capability contract](contract/v0.json) defines the initial
authority ceilings, trust boundaries, adapter requirements, and invariants in a
form shared by the Go CLI, Nix profiles, and deployment checks.

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
CLI, a focused T3 headless package, and reproducible flake checks. The repository
structure and planned areas are:

```text
cmd/paw/              Go CLI entry point
contract/             Versioned machine-readable capability contracts
internal/             CLI and adapter implementation
nix/packages/         Focused runtime packages
nix/modules/          PAW build-time modules
nix/profiles/         Composed workspace profiles
deploy/base/          Portable Kubernetes resources
deploy/adapters/      Environment-specific adapters
docs/adr/             Architecture decisions and threat model
```

Start with:

- [ADR-001: Kubernetes-native collaborative AI workspaces](docs/adr/ADR-001-kubernetes-native-collaborative-ai-workspaces.md)
- [ADR-002: Go CLI and Nix flake build architecture](docs/adr/ADR-002-go-cli-and-nix-flake-build-architecture.md)
- [ADR-003: PAW v0 capability contract and threat model](docs/adr/ADR-003-v0-capability-contract-and-threat-model.md)

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

Inspect the evaluated, machine-readable profile contract:

```sh
nix eval --json .#pawProfiles.x86_64-linux | jq
```

Build or run the CLI:

```sh
nix build .#paw
nix run .#paw -- profile list
```

Build the headless T3 server or inspect its closure metadata:

```sh
nix build .#t3code-headless
nix build .#t3code-headless-closure-info
cat result/total-nar-size
```

The package builds the T3 server and browser client without the Electron app.
It retains adapter libraries used by the server, but bundled provider
executables are removed so that Codex, Claude Code, and OpenCode can be supplied
as independent image layers. The runtime contract check enforces a 450 MiB NAR
closure ceiling for the initial `x86_64-linux` baseline.

Build the provider-free core workspace image or inspect its complete size
report:

```sh
nix build .#paw-core-image
nix build .#paw-core-image-report
jq . result/report.json
```

The report records the unique runtime closure, uncompressed layers, compressed
registry transfer, largest closure members and layers, image configuration, and
manifest digest. The image runs as UID/GID 65532 and contains no Nix CLI,
daemon, Electron runtime, or provider executable.

Build the focused provider variants or their equivalent reports with:

```sh
nix build .#paw-codex-image
nix build .#paw-claude-code-image
nix build .#paw-opencode-image
nix build .#paw-codex-image-report
nix build .#paw-claude-code-image-report
nix build .#paw-opencode-image-report
```

Each variant contains one provider executable and inherits the same unprivileged
T3 runtime contract. No provider credential or authentication state is present
in the build. OpenCode supplies T3's GitHub Copilot path; users authenticate the
Copilot subscription at runtime through OpenCode's
[documented device flow](https://opencode.ai/docs/providers/#github-copilot).

Build the provider-free platform capability image and inspect its budget report:

```sh
nix build .#paw-platform-readonly-image
nix build .#paw-platform-readonly-image-report
jq . result/report.json
```

This image adds OpenTofu, kubectl, Helm, Kustomize, jq, and yq for planning and
inspection. It deliberately omits cloud-specific CLIs; concrete cloud tooling
and short-lived identity belong to separately measured environment capability
layers. Installing an executable does not grant mutation authority: the
`read-only` ceiling, scoped identity, RBAC, egress policy, and supervised
permissions remain independent enforcement boundaries.

Useful released workspaces compose that capability with exactly one provider:

```sh
nix build .#paw-platform-readonly-codex-image
nix build .#paw-platform-readonly-claude-code-image
nix build .#paw-platform-readonly-opencode-image
```

These are complete OCI images, not runtime installers. They preserve the exact
profile and provider labels and reuse identical Nix-derived layers across the
core, provider-only, capability-only, and composed artifacts.

## Sensitive material

This repository is private, but credentials, decrypted configuration, internal
access details, and operational transcripts must still not be committed. See
[AGENTS.md](AGENTS.md) for the repository's agent and secret-handling rules.
