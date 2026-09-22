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

The first `t3code-headless` implementation for `x86_64-linux`, measured on
2026-08-21, establishes these core-package values:

- T3 version: `0.0.33`;
- package NAR size: 203,458,784 bytes (194.0 MiB);
- unique runtime-closure NAR size: 443,127,904 bytes (422.6 MiB); and
- enforced runtime-closure budget: 471,859,200 bytes (450 MiB).

The package contains the server, browser client, JavaScript adapter libraries,
native host `node-pty` build, and resource monitor. It excludes Electron,
desktop resources, provider executables, the Claude Agent SDK's optional bundled
Claude executable, foreign `node-pty` prebuilds, and build-time Node, pnpm, and
Python closures.

The first provider-free `paw-core` image for `linux/amd64`, measured on
2026-08-21, establishes these values and budgets:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Unique runtime-closure NAR | 551,754,704 bytes | 592,445,440 bytes |
| Uncompressed OCI layers | 613,201,920 bytes | 655,360,000 bytes |
| Compressed registry transfer | 173,219,812 bytes | 188,743,680 bytes |
| Layers | 80 | 80 |

The compressed-transfer metric is the OCI manifest, config, and individually
gzip-compressed layer descriptors produced from the Docker-compatible archive.
The archive itself is 165,224,683 bytes. The image includes the headless T3
runtime, a reduced Git build with HTTPS support, SSH, Bash, certificates, and
basic shell tools. It runs as UID/GID 65532 and excludes provider executables,
Electron, the Nix CLI and daemon, Python, pnpm, and build toolchains.

The first single-provider `linux/amd64` images establish these additional
measurements and budgets:

`paw-codex` with Codex 0.147.0:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Runtime-closure NAR | 1,053,314,936 bytes | 1,132,462,080 bytes |
| Uncompressed layers | 1,114,787,840 bytes | 1,205,862,400 bytes |
| Registry transfer | 350,393,253 bytes | 382,730,240 bytes |

`paw-claude-code` with Claude Code 2.1.234:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Runtime-closure NAR | 895,640,568 bytes | 964,689,920 bytes |
| Uncompressed layers | 958,801,920 bytes | 1,038,090,240 bytes |
| Registry transfer | 283,012,021 bytes | 309,329,920 bytes |

`paw-opencode` with OpenCode 1.18.18:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Runtime-closure NAR | 751,366,696 bytes | 812,646,400 bytes |
| Uncompressed layers | 812,840,960 bytes | 880,803,840 bytes |
| Registry transfer | 242,981,162 bytes | 267,386,880 bytes |

All three images contain 80 layers. Their compressed Docker archives are
335,242,040 bytes for Codex 0.147.0, 270,844,835 bytes for Claude Code 2.1.234,
and 231,685,666 bytes for OpenCode 1.18.18. Each provider contract rejects the
other provider executables and the same desktop, Nix, and build runtimes
rejected by `paw-core`.

The pinned Claude Code package has an unfree license. PAW permits that exact
Nix package name rather than enabling unfree packages globally. This packaging
exception does not accept provider terms on a user's behalf and does not place
authentication material in a derivation or image.

The first provider-free `paw-platform-readonly` image adds OpenTofu 1.12.5,
kubectl 1.36.3, Helm 4.2.4, Kustomize 5.8.1, jq 1.8.2, and yq 4.53.3. Its
`linux/amd64` baseline is:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Runtime-closure NAR | 850,103,768 bytes | 917,504,000 bytes |
| Uncompressed layers | 912,394,240 bytes | 985,661,440 bytes |
| Registry transfer | 272,446,901 bytes | 298,844,160 bytes |
| Layers | 80 | 80 |

Its compressed Docker archive is 260,191,330 bytes. The generic profile omits
Azure, AWS, and Google Cloud CLIs. Direct cloud CLIs belong to separately
measured environment capability layers so the base profile does not silently
select one cloud. OpenTofu providers are selected by an explicit repository and
must come from a lock-file-verified cache or adapter-approved bounded egress.

The presence of OpenTofu, kubectl, or Helm does not grant mutation authority.
The read-only identity, Kubernetes RBAC, egress policy, and supervised tool
permissions enforce the profile's external-mutation prohibition independently.

The first useful platform workspaces compose that capability closure with one
provider. Their `linux/amd64` baselines are:

`paw-platform-readonly-codex`:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Runtime-closure NAR | 1,351,664,000 bytes | 1,457,520,640 bytes |
| Uncompressed layers | 1,413,990,400 bytes | 1,530,920,960 bytes |
| Registry transfer | 449,584,656 bytes | 492,830,720 bytes |

`paw-platform-readonly-claude-code`:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Runtime-closure NAR | 1,193,989,632 bytes | 1,289,748,480 bytes |
| Uncompressed layers | 1,258,024,960 bytes | 1,363,148,800 bytes |
| Registry transfer | 382,189,332 bytes | 419,430,400 bytes |

`paw-platform-readonly-opencode`:

| Metric | Measured | Budget |
| --- | ---: | ---: |
| Runtime-closure NAR | 1,049,715,760 bytes | 1,137,704,960 bytes |
| Uncompressed layers | 1,112,033,280 bytes | 1,205,862,400 bytes |
| Registry transfer | 342,132,103 bytes | 377,487,360 bytes |

All three composed images contain 80 layers. Their Docker archives are
430,207,196 bytes for Codex, 365,809,308 bytes for Claude Code, and 326,650,924
bytes for OpenCode. Shared closure paths produce identical immutable layers for
registry and node reuse, while each complete image remains independently
measured and budgeted.

Native `arm64` measurements must still be recorded before publication of the
common image index.

### 6. Package providers without packaging credentials

The initial provider variants support the native T3 adapters for Codex and
Claude Code and the OpenCode adapter for GitHub Copilot. A released profile may
combine providers when collaboration requires it, but provider binaries are not
part of the minimal core by default. Authentication and state are separate
workspace attachments governed by the selected persistence and identity policy.

No personal or organizational provider credential is stored in a Nix derivation,
OCI layer, release artifact, profile definition, or build log.

### 7. Reuse pinned shared T3 and Codex package recipes

This section records the initial alignment implementation. Its mandatory shared
repository dependency is superseded by
[ADR-009](ADR-009-portable-build-inputs-and-scheduled-updates.md): PAW owns its
build recipes and defaults to upstream, with explicit downstream overrides.

PAW pins the reusable `nix-packages` repository as a source input. Its Codex
recipe and patched T3 fork source are the version authorities for these two
components; PAW does not independently maintain an upstream T3 version or rely
on whichever executable happens to be installed on the build host.

Evaluate those recipes with PAW's pinned nixpkgs to avoid duplicated runtime
libraries and retain PAW's Linux architecture targets. The headless derivation
reuses the fork source, version, patches, and resource monitor, but builds only
the server and browser assets. A focused Codex runtime preserves the shared
package's provider payload and launcher without adding a full build-time Node
runtime. Image and closure contracts remain the acceptance gate.

This is source/version alignment, not reuse of a desktop image or an automatic
rolling update. Updating the shared-source lock requires rebuilding, measuring,
and testing PAW. Other providers remain independently pinned by PAW's nixpkgs
until explicitly migrated. Credentials and host configuration are never reused.

Alternatives were maintaining duplicate version pins, which permits drift, or
copying the complete desktop runtime, which violates the headless boundary.

The shared-source migration was measured on `linux/amd64` with T3
`0.0.42-fork.20+pa415f7130b` and Codex `0.155.1`:

| Metric | `paw-core` | `paw-codex` |
| --- | ---: | ---: |
| Runtime-closure NAR | 574,020,480 bytes | 944,534,376 bytes |
| Uncompressed layers | 646,666,240 bytes | 1,017,405,440 bytes |
| Registry transfer | 178,547,209 bytes | 327,026,165 bytes |
| Layers | 80 | 80 |

Both pass the existing budgets without increases. The headless T3 closure also
passes its existing 450 MiB limit. These are build-contract results, not live
provider-authentication or egress qualification.

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
