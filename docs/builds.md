# Reproducible builds and dependency updates

PAW builds its images in this repository. It does not require a personal package
repository, credentials from a developer machine, or Nix inside the workspace.
See [ADR-009](adr/ADR-009-portable-build-inputs-and-scheduled-updates.md) and
[issue #40](https://git.alc.xyz/alcxyz/paw/issues/40).

## Inputs and ownership

- T3: `nix/packages/t3code/source.json` pins upstream owner/repository, immutable
  revision, version, source hash, Cargo hash, and headless pnpm dependency hash.
- Codex: `nix/packages/codex-cli/source.json` and its adjacent npm lock file pin
  the upstream CLI distribution and dependencies. Packaging an upstream binary
  distribution does not mean PAW compiles that provider from its Rust sources.
- Claude Code, OpenCode, and platform tools: selected by `flake.lock`'s nixpkgs
  revision. Updating that lock is currently an explicit maintainer operation,
  validated by the same image contracts.

Source and provider overrides are build-time operator decisions. They do not
grant a browser user permission to change the image or download executables at
runtime. T3 continues to use the installed provider binaries and runtime login;
PAW's build owns their versions.

## Custom T3 and provider builds

Use a downstream Nix expression or flake to select your own inputs. The headless
builder accepts `sourceSpec` (the same fields as the T3 JSON above) and an
optional patch list. Pin a full commit, use content hashes, and give patched
builds a distinct version. Updating only a repository URL is insufficient when
its dependency graph or build layout changes.

PAW exports `lib.mkT3codeHeadless`, `lib.mkCodexCli`, and
`lib.mkWorkspaceImage`. Alternatively, override the exported package arguments
in your downstream expression. Keep the resulting T3 package connected to the
image's `t3codeHeadless` argument; changing a standalone T3 output does not
magically replace it in every image.

[The custom-build example](../examples/custom-build.nix) wires those overrides
through to a Codex image. From this checkout, evaluate its default inputs with:

```sh
nix eval --impure --raw --expr '
  let paw = builtins.getFlake (toString ./.);
  in (import ./examples/custom-build.nix {
    inherit paw;
    system = builtins.currentSystem;
  }).drvPath'
```

In a downstream configuration, pass `t3codeSourceSpec`, `t3codePatches`, and, for
non-GitHub sources, `t3codeSourceArchive` (a pinned unpacked Nix source). That
archive feeds both the server and native resource-monitor build. The equivalent
library argument is `sourceArchive`. Source metadata must describe the supplied
archive accurately. The example also accepts `codexSourceSpec` and
`codexPackageLock`; changing either requires validating the other.

Custom providers are supplied as explicit `providerPackages` with matching
`providers` names. They must preserve the existing runtime and image contracts;
these arguments are not a bypass for host credentials, extra privileges, or
runtime installers. A Codex version override needs its matching npm lock file
and dependency hash, not just a changed version string.

Maintain custom fork pins in the downstream configuration. The upstream nightly
workflow updates PAW's default inputs only; it does not update every downstream
fork or reset one to upstream. Rebuild and verify a custom image before choosing
it for a workspace, and retain the prior image digest for deliberate rollback.

## Local validation

```sh
bash scripts/ci/check.sh
```

This runs offline automation tests and `nix flake check`, including Go tests,
formatting, manifests, runtime separation, and image budgets. It also evaluates
Linux ARM derivations without claiming to build or run them. It does not access
a Kubernetes cluster, log into a provider, or deploy an image.

Prepare changed pins only in a clean, disposable checkout:

```sh
CI=true PAW_DISPOSABLE_UPDATE_CHECKOUT=1 \
  nix shell --no-update-lock-file --inputs-from . nixpkgs#python3 \
    --command python3 scripts/update-dependencies.py
bash scripts/ci/check.sh
git diff -- nix/packages
```

For read-only release discovery without changing pins or building candidate
dependencies, use
`nix shell --no-update-lock-file --inputs-from . nixpkgs#python3 --command
python3 scripts/update-dependencies.py --check`. This mode does not require a
disposable checkout.

The updater needs Nix and uses Python from PAW's pinned nixpkgs so safe archive
extraction does not depend on the runner or host Python version. It also obtains
npm from the same pinned nixpkgs when regenerating the Codex lock, with lifecycle
scripts disabled and temporary npm configuration/cache. It accesses public
upstream release metadata and archives.
Review the diff even when hashes and builds pass: a content hash is not a review
of upstream behavior. Discovery errors are failures, not evidence that pins are
current. Custom T3 repositories are not silently reset to upstream by the
default update command.

## Nightly proposals

CI requires a runner that supports Nix's sandbox namespaces. Both workflows
disable sandbox fallback: an incompatible runner must fail, not silently build
without the isolation required by ADR-009. A generic container runner label
alone does not establish this capability.

`.forgejo/workflows/update-dependencies.yml` runs at **03:17 UTC** and supports
manual dispatch from `dev`. It:

1. Checks whether this repository already has an open automated dependency PR;
   if so, preserves that review and skips the scan.
2. Discovers stable T3 and Codex updates, not upstream nightly/prerelease builds.
3. Resolves new immutable source/dependency pins. An unchanged scan is a no-op.
4. Runs the complete build checks only when there are changed inputs.
5. Proposes only the allowlisted pin/lock files on a new automation branch.

There is no force-push, auto-merge, deployment, image publication, or credential
login. PRs target `dev`; promotion to protected `main` remains deliberate.
Pending updates intentionally pause later proposals: maintainers should review
or close them rather than expecting the bot to replace review work.

Forgejo's automatic workflow token does not trigger additional workflows from
its own pushes or PRs. The update job therefore validates before publication;
do not assume the bot-created PR will also receive an automatic validation run.
Dispatch **Validate PAW** manually on the proposed branch before merging,
especially after any maintainer edits. No broader personal token is required
just to bypass this recursion protection. See the
[automatic-token documentation](https://forgejo.org/docs/v15.0/user/actions/basic-concepts/#automatic-token).

The publication step uses Forgejo's repository-scoped workflow token through
the authenticated client and Git askpass; it never places a credential in a
command argument or saved Git remote. Checkout disables credential persistence.
Build steps have no provider/infrastructure credentials or configured publication
token. Run jobs on isolated ephemeral runners, not a developer workstation with
credential-bearing mounts. Do not weaken isolation or attach broad tokens to
work around runner/API failures.

`.forgejo/workflows/validate.yml` runs the same checks for PRs and pushes to
`dev`/`main`. All external actions are pinned to commits. No shared writable
cross-trust build cache is configured in this first implementation.

## Activation and limits

Forgejo only schedules workflows present on its default branch. Adding the
workflow to a PR does not activate the nightly schedule. After review and merge
to the default `dev`, manually dispatch once, confirm runner availability and
repository-token permission, and then observe the scheduled run. Do not report
the scheduler as proven merely because local unit tests pass. See the
[Forgejo scheduling reference](https://forgejo.org/docs/v15.0/user/actions/reference/#onschedule).

The first lane builds Linux amd64. Native ARM images, authenticated provider
smoke tests, and candidate registry publication remain separate qualification
work. Passing CI is not a completed PAW pilot. The current browser-only workspace
is not upgraded by any of these workflows.
