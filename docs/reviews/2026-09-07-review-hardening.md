# PAW implementation review — 2026-09-07

## Scope

Reviewed the implemented CLI, repository transfer, environment verifier,
deployment rendering, image composition, and conformance scripts. The review
includes the pending environment-verifier and agent-guidance changes. Existing
image versions and capability ceilings are unchanged.

## Findings addressed

| Area | Finding | Correction |
| --- | --- | --- |
| Workspace creation | Applying manifests could update an existing workspace and report success before readiness | Verify networking, atomically reserve the namespace, refuse collisions, and wait for rollout |
| Workspace deletion | Manifest deletion relied on names without ownership verification | Require the PAW ownership label and delete with UID/resource-version preconditions |
| Operator interruption | Repeated interrupts could leave an unresponsive child running indefinitely | Forward signals to the child process group; kill after another signal or a five-second grace period |
| Network evidence | Error substrings and a running target could be mistaken for enforced denial | Parse recognized remote probe results, check listeners and an unaffected path, version the probe as v2 |
| Probe cleanup | Cleanup could target a namespace replaced after creation | Capture creation UID, reconcile ambiguous creation with an independent token, and use deletion preconditions |
| Repository import | Temporary artifacts could survive early failure; recipient Git settings could affect checkout | Install cleanup before allocation and ignore recipient configuration and templates |
| Repository tests | String matching did not execute the import behavior | Execute the production script against temporary Git repositories, malformed bundles, and destination races |
| Live-test cleanup | Lookup or creation errors could be mistaken for ownership | Require successful creation and matching namespace UID; surface cleanup failures |
| Live-test denial | Any failing kubectl invocation could count as denied egress | Require a successful remote probe result and recheck the positive control |
| Minikube image loading | Missing local images could trigger registry pulls | Render `imagePullPolicy: Never` for Minikube; retain `IfNotPresent` for generic immutable images |

Lifecycle behavior is recorded in ADR-007. Failed application or rollout retains
resources for inspection. Legacy namespaces without the ownership marker are
not automatically adopted or deleted.

## Verification

- Go unit tests, race detection, and vet passed across all packages.
- Nix-built CLI tests, formatting, Markdown, and Kubernetes render contract
  checks passed, including all profile/provider combinations.
- The CLI cross-builds for Linux arm64 and macOS arm64.
- Repository tests execute imports and failures without Kubernetes or provider
  credentials; script tests exercise cleanup with local command substitutes.
- Image derivations were evaluated, but unchanged images were not rebuilt or
  requalified as part of this review.

The attempted v2 live run could not qualify the disposable Minikube cluster:
its API readiness check reported an etcd failure and requests timed out during
pod creation and cleanup. PAW returned an error. The disposable clusters were
removed after the attempt. No v2 pass or known-non-enforcing live result is
claimed; both lanes still need a healthy test environment. Live evidence for
`network-policy-v1` remains historical.

## Remaining work

- Issue #23: prove backing-volume reclamation; namespace/PVC deletion does not
  establish data erasure or complete storage conformance.
- Issue #30: implement bounded external egress and provider access.
- Issues #4 and #8: release and qualify immutable multi-architecture images,
  provider authentication, and actual multi-client thread continuation.
- Issue #29: complete the same-image lifecycle matrix and managed-cluster lane.
- Repository streaming still lacks a configurable transfer deadline and resume
  support, as documented in ADR-004.

The review strengthens the implemented foundation. It does not declare the full
M1 workspace or provider security contract complete.
