# ADR-009: Portable build inputs and scheduled dependency updates

- Status: Accepted
- Date: 2026-09-22
- Amends: ADR-002 section 7
- Hosting amended by ADR-010: GitHub is now canonical; the safety contract below
  is unchanged.

## Context

PAW's first working package alignment reused a personal package repository's
T3 fork and Codex recipes. That validated compatible versions, but made an
operator-specific source selection mandatory for everyone. PAW must own its
headless build without requiring a particular operator's repository layout.

Provider and T3 releases evolve independently. Scheduled rebuilding of unchanged
pins does not discover new releases, while downloading mutable executables into
a running workspace breaks the connection between image identity and software
versions. The operator wants regular update proposals without automatic rollout.

## Decision

### Own builds; allow explicit source customization

PAW owns the headless T3 and provider image build recipes. Its default T3 source
is a pinned upstream stable release, not a personal fork or a moving branch.
Operators can supply a fork source, revision, patches, and dependency hashes at
build time using the documented Nix interface. This produces a custom image;
ordinary users can continue using released images without installing Nix.

Custom inputs are trusted build configuration, not a workspace or browser
setting. A fork must satisfy the same CLI compatibility, immutable runtime,
provider separation, and image-size checks. A different revision or patch set
may need new dependency hashes or build-recipe changes. Source overrides do not
promise compatibility with arbitrary forks.

Provider packages also have pinned defaults and ordinary build-time package
overrides. T3 runs/configures the installed providers; the image build owns
executable installation and upgrades. Runtime authentication remains separate.
Do not add provider auto-downloads, package managers, or update egress to make an
immutable image self-upgrade.

### Propose updates nightly; promote deliberately

A scheduled GitHub workflow checks upstream stable releases and proposes
changes to reviewed dependency pins. Nightly is the schedule, not the upstream
release channel. Manual dispatch uses the same checks. Fork operators select
their own inputs explicitly; the default updater must not replace a custom
source with upstream silently.

Only changed candidates need rebuilding. Before publishing an update PR, run
the repository's static, CLI, runtime, and image-budget checks. Fail closed on
release discovery errors, source/hash failures, or failed builds. A discovered
hash is an integrity pin, not a security review of upstream code.

Maintain at most one open automated dependency PR. Do not overwrite a pending
review, force-push another person's branch, auto-merge, promote to `main`,
publish images, or redeploy a workspace. An unchanged scan is a successful no-op;
a failed scan remains a failed workflow rather than appearing up to date.

Build/test steps do not receive provider or infrastructure credentials. Use
ephemeral CI runners and sandboxed Nix builds. Repository credentials are used
only for checkout and the narrowly scoped PR publication step, with checkout
credential persistence disabled. Pull-request validation never uses
`pull_request_target` or deployment secrets. An operator must review the runner
trust boundary; workflow YAML alone is not a runner isolation guarantee.

### Stage publication separately

This increment adds update proposals and CI validation, not an image registry or
automated candidate publishing. Later candidate publication requires immutable
digests, source/provider version manifests, reviewed registry credentials, and
the authenticated provider smoke test. Passing build checks is not evidence of
provider login, egress enforcement, cross-device collaboration, or ARM runtime
qualification. Running workspaces change only through deliberate upgrades.

## Alternatives

- Mandatory shared personal package repository: rejected as the product default;
  it remains a possible source of downstream custom configuration.
- Runtime provider installers/updaters: rejected for the immutable workspace
  contract because identical image digests could run different executables.
- Nightly automatic deployment: rejected because it interrupts work and bypasses
  explicit promotion and rollback decisions.
- Full release/registry automation now: deferred until the useful authenticated
  pilot has passed; it is not needed to review dependency updates.

## Validation

- Default inputs build without the personal package repository.
- The documented custom-source interface evaluates and has a tested example.
- Updater tests cover unchanged releases, invalid metadata, failures, and custom
  source refusal; placeholder hashes cannot be proposed as valid updates.
- Publication checks constrain changed paths and branches and avoid replacing
  existing review work.
- CI runs the existing build, manifest, runtime, and image-budget contracts.
- A real scheduled run is only claimed after the workflow reaches the default
  branch and runner execution is observed.
