# ADR-015: Developer profile for PAW self-development

- Status: Accepted
- Date: 2026-09-22
- Amends: ADR-003 profile list

## Context

The first goal after the pilot is to develop PAW inside PAW. That needs the
toolchain PAW's own checks use: Go for build, test, vet, and formatting, and
shellcheck, jq, and yq for the scripts. PAW has no Go module dependencies, so
no module proxy or package download purpose is required; the toolchain itself
is the only gap.

The `core` image is measured against closure, layer, and transfer budgets and
sits within a few megabytes of its closure budget. The Go toolchain alone is
about 270 MiB of closure. Adding it to `core` would either break the budgets
or raise them for every workspace, including ones that never compile anything.

Profiles in the v0 contract compose tools with an authority ceiling. Adding a
tool to a profile does not grant authority; the ceiling, egress policy,
identity, and supervised permissions remain independent controls.

## Decision

Add a `developer` profile to the v0 contract with the same `workspace-only`
ceiling, capabilities, egress purposes, and prohibitions as `core`, plus the
Go toolchain, shellcheck, jq, and yq in its runtime packages. It is `core`
with a compiler, not a new authority class.

Developer images set `CGO_ENABLED=0`, because the image carries no C
toolchain and PAW builds without cgo, and `GOTOOLCHAIN=local`, because the
workspace has no package egress and must never try to download a toolchain.
The image contracts assert exactly those variables for developer images.

The profile composes with every provider the same way `core` does, producing
`paw-developer`, `paw-developer-codex`, `paw-developer-claude-code`, and
`paw-developer-opencode`, each with its own measured budgets and the same
runtime and security contracts. `core` budgets are unchanged.

Nix itself is not in the profile. Flake checks, image builds, and live cluster
tests run outside the workspace by design: the workspace has no cluster
identity and must not gain one for self-development.

## Consequences

### Positive

- A PAW workspace can run `go build`, `go test`, `go vet`, `gofmt`,
  shellcheck, and the contract script's jq and yq logic on its own source.
- `core` stays lean and its budgets stay meaningful.
- No new egress purpose, credential, or authority is introduced.

### Negative

- Four more images to build, measure, and keep within budget.
- The full validation loop still needs the operator's machine for Nix and
  live checks; the workspace produces a reviewed patch, as ADR-008 already
  requires.

## Alternatives considered

### Grow the core profile

Rejected. Every workspace would carry a compiler and the core budgets would
lose their meaning as a regression guard.

### A toolchain dimension separate from profile and provider

Rejected for now. It would double the image matrix again and add a third
selector to every command for one use case.

### Include Nix in the workspace

Rejected. Nix builds need a daemon or sandbox privileges and the flake checks
touch clusters; both are outside the workspace-only ceiling.

## Validation

1. `paw profile show developer` renders the contract entry and the Nix
   profile evaluation passes its contract assertions.
2. The four developer images build, pass the runtime and security contracts,
   and stay within their recorded budgets.
3. A disposable developer workspace with the PAW repository imported runs
   `go build ./...`, `go test` on a package, `gofmt -l`, and `shellcheck` on
   a script successfully with no network access beyond the proxy.
