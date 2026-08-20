# ADR-002: Go CLI and Nix flake build architecture

- Status: Accepted
- Date: 2026-08-20

## Context

PAW needs both a reproducible contributor environment and an approachable,
portable operator experience. The project must build a command-line client,
multi-architecture workspace images, provider integrations, capability profiles,
and machine-readable deployment metadata.

Devbox was considered as a developer-facing wrapper over Nix. It provides a
simpler package manifest, plugins, scripts, direnv integration, and generated
container definitions. PAW, however, already requires a purpose-built CLI for
workspace lifecycle and an explicit Kubernetes deployment contract. Retaining
both Devbox and a PAW CLI would duplicate responsibility and obscure the build
boundary.

A Nix-built image does not require the complete host Nix store. It can contain
only the runtime closure of the selected packages, and it does not need the Nix
daemon or CLI at runtime.

The existing desktop-oriented T3 package is not an acceptable container input.
On 2026-08-21 its runtime closure measured approximately 4.1 GiB and directly
referenced Electron, Codex, and Claude Code. This demonstrates that closure-based
images are only as focused as their package outputs; blindly copying the current
package would produce an unnecessarily large workspace image.

## Decision

### 1. Implement the PAW CLI in Go

The `paw` command is the stable user and operator interface. It owns workflows
such as environment diagnostics, profile inspection, workspace creation,
connection and pairing, status, and deletion.

The CLI must:

- be distributed as native release binaries;
- operate with released images and deployment metadata without requiring Nix;
- use explicit adapters for environment-specific cluster integration;
- avoid handling plaintext provider or infrastructure credentials directly;
- expose the effective workspace profile and authority to users; and
- keep its behavior testable independently of a particular developer machine or
  cluster.

Early implementations may invoke established local tools where an adapter
requires them. Stable Kubernetes operations should use typed APIs when that
improves validation and error handling.

### 2. Use a Nix flake as the build interface

The repository flake pins inputs and produces:

- the `paw` Go binary;
- workspace OCI images;
- contributor development shells;
- provider CLIs and platform tools;
- evaluated capability-profile metadata; and
- build, module, image, and contract checks.

The intended public outputs include equivalents of:

```text
packages.<system>.paw
packages.<system>.workspace-image
devShells.<system>.default
checks.<system>.*
pawProfiles.<system>.*
```

Exact attribute names may evolve before the first release, but the separation of
binary, image, development, checks, and profile outputs is part of the decision.

### 3. Use small PAW-specific Nix modules for build-time composition

Nix modules compose immutable package closures, files, provider binaries, and
profile metadata. Begin with only the `core` and `platform-readonly` profiles.
Extract reusable options when actual variation between those profiles requires
them; do not build a speculative general framework.

An evaluated profile exports machine-readable metadata needed by the Go CLI and
Kubernetes layer, including:

- immutable image identity;
- included providers and tools;
- required cluster and adapter capabilities;
- persistence and mount requirements;
- identity and network-policy requirements; and
- the maximum permission class represented by the profile.

Nix describes build inputs and emitted contracts. Kubernetes remains responsible
for enforcing runtime identity, RBAC, mounts, networking, resources, and
admission policy.

### 4. Keep Nix out of the normal workspace runtime

Build workspace images from selected runtime closures. Do not include the Nix
daemon, Nix CLI, compilers, build caches, or general contributor toolchain unless
a future capability explicitly requires them.

Package a dedicated `t3code-headless` output containing the server, web assets,
resource monitor, and runtime dependencies needed by `t3 serve`. It must not
reference Electron, desktop resources, provider binaries, or build-time
dependencies.

Build image variants from composable layers rather than publishing one mandatory
all-tools image:

- `paw-core` contains the headless T3 runtime and essential workspace tools;
- provider layers add Codex, Claude Code, or OpenCode;
- capability layers add platform tools such as Kubernetes, Terraform/OpenTofu,
  or cloud CLIs; and
- released profiles select the required provider and capability layers.

Shared closures and OCI layers should be reused by registries and nodes, but
layer reuse is not accepted as a substitute for controlling total unpacked size.
Large tools must remain outside `paw-core` and be included only by profiles that
require them.

Publish multi-architecture images and refer to released images by immutable
digest. Build architectures natively in CI where practical and publish a common
image index.

Normal PAW users install the released Go binary and pull released images. Nix is
required for contributors building PAW or authoring custom profiles, not for
ordinary workspace operation.

### 5. Measure and enforce image budgets

CI records, for every released image:

- the unique Nix runtime-closure size;
- the uncompressed OCI image size;
- the compressed registry-transfer size; and
- the largest closure members and layers.

The first optimized headless and provider images establish reviewed size budgets.
Subsequent builds fail when they exceed those budgets beyond an explicitly
documented tolerance. Raising a budget requires an intentional review explaining
the new runtime dependency or capability.

### 6. Package providers without packaging credentials

The initial provider variants support the native T3 adapters for Codex and
Claude Code and the OpenCode adapter for GitHub Copilot. A released profile may
combine providers when collaboration requires it, but provider binaries are not
part of the minimal core by default. Authentication and state are separate
workspace attachments governed by the selected persistence and identity policy.

No personal or organizational provider credential is stored in a Nix derivation,
OCI layer, release artifact, profile definition, or build log.

## Consequences

### Positive

- PAW has one user-facing CLI rather than a CLI wrapped around another tool's
  project interface.
- Contributors retain direct access to Nix reproducibility, module composition,
  binary builds, image construction, and checks.
- Normal users do not need to install or understand Nix.
- Runtime images contain only selected closures and omit build infrastructure.
- Headless, provider, and capability boundaries make image cost visible and
  prevent unrelated tools from accumulating in every workspace.
- The Go CLI can be released independently and tested against multiple cluster
  adapters.

### Negative

- PAW owns CLI design, release engineering, module interfaces, and documentation
  that Devbox would otherwise partially provide.
- Nix module evaluation and exported profile metadata require explicit contract
  tests.
- Native multi-architecture image production requires suitable CI builders.
- Contributors defining new profiles need some Nix knowledge.
- Maintaining focused package outputs and image budgets adds packaging and CI
  work that a general-purpose desktop package avoids.

## Alternatives considered

### Devbox as the canonical tool manifest

Rejected for the initial implementation. Its simplified interface is valuable,
but the PAW CLI supplies the user experience and the project needs lower-level
control over images, closures, flake outputs, and profile metadata.

### Install Nix and Devbox in every workspace

Rejected because build-time flexibility is not required for the normal runtime
and would increase image size and attack surface.

### Handwritten Dockerfiles and shell scripts

Rejected because they would duplicate dependency closure management, weaken
reproducibility, and create a less testable operator interface.

### Require Nix for all users

Rejected because PAW should be consumable from released binaries and OCI images
on any supported Kubernetes environment.

## Initial validation

The first implementation must demonstrate:

1. `nix build .#paw` builds the Go CLI;
2. a flake output builds `t3code-headless` without Electron or desktop runtime
   references;
3. flake outputs build `paw-core` and provider-specific image variants;
4. a flake output builds the `platform-readonly` workspace image;
5. each image reports its closure, uncompressed, compressed, and largest-member
   sizes;
6. reviewed budgets are established from the optimized baseline and enforced in
   CI;
7. the images contain required runtime closures but no Nix daemon or decryption
   identity;
8. `nix develop` provides the contributor environment;
9. `nix flake check` validates Go, modules, profile metadata, and image policy;
10. Linux amd64 and arm64 release artifacts can be produced; and
11. a released `paw` binary can deploy a prebuilt image without Nix installed.
