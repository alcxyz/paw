# Stack review: PRs #10–#22 (feat/repository-bundle-materialization)

- Date: 2026-08-21
- Reviewer: Claude (Fable 5), read-only code and architecture review
- Base: `origin/dev` (0102b51); head: `feat/repository-bundle-materialization` (462349d)
- Scope: full stacked diff (38 files, ~5,000 lines), ADR-001..004,
  `contract/v0.json`, Forgejo PRs #10–#22 and issues #1, #2, #4–#8
- Verification: `go test`/`go vet`, shellcheck, `git diff --check`,
  `nix flake check --no-build`, a CLI render smoke test, empirical Git-behavior
  checks, and inspection of locally built Nix store outputs. The opt-in live
  conformance script was **not** run and no infrastructure was created.

## Findings (by severity)

### High

#### H1. Live default-deny egress check is vacuous — the image has no `curl`

- **PR:** #21 — `scripts/check-minikube-live.sh:125-129`
- **Evidence:** The egress probe is
  `kubectl exec workspace-0 -- curl … https://example.com` inside an
  `if …; then fail` block. The `core` runtime closure
  (`nix/profiles/runtime-core.nix`) contains bash, coreutils, findutils,
  gitMinimal (against `curlMinimal`), grep, sed, openssh — no curl executable.
  The only curl store path in the built closure is `curl-8.21.0` containing
  only `lib/` (libcurl for git), no `bin/`. The image `PATH` is
  `makeBinPath runtimeContents` (`nix/images/paw-core.nix:68`), which includes
  no curl bin dir.
- **Impact:** `kubectl exec` fails with "executable not found" (exit 126/127),
  the `if` condition is false, and the script proceeds — reporting "enforced
  default-deny egress" regardless of whether the CNI enforces NetworkPolicy at
  all. On Minikube's default kindnet CNI (no NetworkPolicy enforcement) the
  flagship live security claim passes falsely. The script checks for `curl` on
  the host (line 45) but never in the pod, which disguises the failure mode.
- **Fix:** Probe with a tool actually present in the image and distinguish
  "tool missing" from "blocked", e.g. exec
  `sh -c 'command -v git >/dev/null || exit 9;
  timeout 5 git ls-remote https://github.com/ >/dev/null 2>&1'`,
  treating exit 9 as a harness error and success as an egress-enforcement
  failure. Ideally add a positive control (prove the same probe succeeds
  without the NetworkPolicy, or assert the CNI advertises policy support).
- **Blocks merge:** Yes — the PR's core deliverable is this conformance
  evidence.

### Medium

#### M1. Manifest contract misses `secret` volumes and common credential env names

- **PR:** #19 — `scripts/check-kubernetes-contract.sh:68,80-82` (regex reused
  in `scripts/check-minikube-live.sh:88-90`)
- **Evidence:** The negative checks reject `secretKeyRef`, `secretRef`,
  `serviceAccountToken`, and `envFrom`, but a pod volume of type `secret:`
  (`volumes: [{name: x, secret: {secretName: y}}]`) — the most common static
  secret attachment — matches none of them. Projected-volume `secret` sources
  are likewise uncaught. The env-name regex
  `(TOKEN|SECRET|PASSWORD|CREDENTIAL|ACCESS_KEY|PRIVATE_KEY)` does not match
  `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or `*_BEARER`. Checks inspect only
  `containers[0]`; initContainers/sidecars would be unexamined.
- **Impact:** A future regression attaching a static secret via a secret
  volume or an `API_KEY` variable passes the exact checks PR #19 claims to
  add. False negative in a control whose purpose is preventing that
  regression.
- **Fix:** Add a `has("secret")` rejection scoped to volume/projection
  objects, extend the regex with `API_?KEY|BEARER|PASSPHRASE`, and iterate all
  containers and initContainers.
- **Blocks merge:** Yes — cheap fix and central to the PR's claim.

#### M2. Ref-movement TOCTOU: bundle created from the ref name, not the pinned commit

- **PR:** #22 — `internal/repository/repository.go:45` (`git bundle create -
  selected.ref`) vs. `repository.go:127-135` (resolution to
  `selected.commit`)
- **Evidence:** `resolve()` pins `ref → commit`, then a separate
  `git bundle create` runs against the ref. If the ref fast-forwards between
  the two (a concurrent commit in the operator's checkout), the bundle carries
  commits newer than the resolved commit; the pod checks out the resolved
  commit, but the newer history remains reachable in the materialized clone
  via the bundle's branch ref. If the ref was rebased/force-moved, checkout
  fails late with an opaque error. The command output (`materialized at
  COMMIT`) then understates what entered the workspace.
- **Impact:** Unintended history can enter the workspace beyond the
  operator-reported commit — precisely the property ADR-004's "resolves the
  ref to an exact commit before transfer" is meant to guarantee.
- **Fix:** In `materializeScript`, verify the bundle tip equals `$commit`
  before checkout, or bundle a temporary ref pinned to the resolved commit.
  Pod-side verification is simplest and closes both windows.
- **Blocks merge:** Fix as part of accepting ADR-004; window is small and
  operator-local, so "fix before merge" rather than a redesign.

#### M3. kubectl-side failures masked as bundle failures

- **PR:** #22 — `internal/repository/repository.go:82-88`;
  `materializeScript` line `test ! -e "$destination"`
- **Evidence:** When the pod script fails early (notably the overwrite
  refusal), kubectl exits, the stdin pipe breaks, and `git bundle` dies with
  SIGPIPE. `Add` checks `bundleErr` first, so the operator sees
  `create Git bundle: signal: broken pipe` — and the pod script prints
  nothing, so there is no "destination exists" message at all. Which error
  surfaces is timing-dependent.
- **Impact:** Diagnosability, not safety: the refusal works (the live test
  relies only on the nonzero exit), but the operator cannot distinguish
  "destination exists" from a corrupted stream.
- **Fix:** Report `kubectlErr` first when both are set, and make the pod
  script announce its failure
  (`echo "destination … already exists" >&2; exit 1`).
- **Blocks merge:** No; fix alongside M2.

#### M4. `check-kubernetes-contract.sh` fails outside Nix; fixed shared temp path

- **PR:** #13 — `scripts/check-kubernetes-contract.sh:6`
- **Evidence:** `check_dir=${TMPDIR:?}/paw-kubernetes-contract` aborts when
  `TMPDIR` is unset — the default in most interactive Linux shells; it only
  works reliably inside the Nix sandbox. The predictable path in shared tmp is
  never cleaned up and is symlink-attackable on multi-user hosts (low
  practical risk).
- **Impact:** The documented local-check workflow breaks for contributors.
- **Fix:** `check_dir=$(mktemp -d "${TMPDIR:-/tmp}/paw-kubernetes-contract.XXXXXX")`
  with a cleanup trap.
- **Blocks merge:** No.

### Low

#### L1. `--revision HEAD` in the live script; CLI accepts `HEAD` at all

- **PR:** #21/#22 — `scripts/check-minikube-live.sh:159`,
  `internal/repository/repository.go:123-141`
- On a detached checkout (typical CI), `rev-parse --symbolic-full-name HEAD`
  prints literal `HEAD`, `allowedRef` rejects it, and the conformance run
  fails confusingly. Accepting `HEAD` on an attached branch is implicit
  selection, in tension with ADR-004's explicitness rationale. Use an explicit
  ref in the script; decide the `HEAD` policy in ADR-004 (recommend rejecting
  it). Non-blocking.

#### L2. Overwrite refusal has its own TOCTOU

- **PR:** #22 — `materializeScript`
- `test ! -e "$destination"` runs before the transfer; if the destination
  appears during it, plain `mv` moves the staging dir *into* the existing
  directory as `$destination/repository`. Use `mv -T` (coreutils is in the
  image) so a concurrent destination makes the move fail. Non-blocking
  (single-operator model).

#### L3. `paw workspace connect` does not forward termination to kubectl

- **PR:** #20 — `internal/cli/cli.go:474-479`
- Killing the `paw` process by PID (what the live script's cleanup does at
  `check-minikube-live.sh:16`) orphans `kubectl port-forward`, which keeps the
  local port bound until the pod goes away. Interactive Ctrl-C is fine
  (process group). Forward signals or exec kubectl directly. Non-blocking.

#### L4. "Foreign prebuilds" removal only covers node-pty

- **PR:** #10 — `nix/packages/t3code-headless.nix:140-144`
- The built output still ships `bufferutil@4.1.0/prebuilds` and
  `utf-8-validate@6.0.6/prebuilds` (multi-platform `.node` binaries). Small,
  but inconsistent with the packaging narrative; extend the prune.
  Non-blocking.

#### L5. Layer-count budget can never trip

- **PR:** #14 — `flake.nix` image contracts; `nix/images/paw-core.nix:37`
- `buildLayeredImage` uses `maxLayers = 80`, so `layerCount > 80` is
  unreachable; the check documents rather than enforces. Informational.

#### L6. Flag values that look like flags are consumed as values

- **PRs:** #13/#18/#22 — `internal/cli/cli.go` parse loops,
  `runWorkspaceRepository`
- `--source --revision main …` yields `source="--revision"` and a confusing
  downstream error rather than a parse error. Everything fails closed
  (resolution/matrix lookups reject the garbage); UX only. Non-blocking.

#### L7. ADR-004 is silent on submodules, LFS, and stream timeouts

- A source repo using submodules or Git LFS materializes silently incomplete
  (empty submodule dirs; LFS pointer files) with no network to recover, and no
  timeout exists on the stream (an API-server hang blocks `Add` until
  interrupted). Belongs in ADR-004's Negative consequences. Doc-only.

## Verified non-findings

- **Claude Agent SDK bundled-binary removal is real, not a glob accident.**
  The glob `@anthropic-ai+claude-agent-sdk-*` correctly targets the platform
  package `@anthropic-ai+claude-agent-sdk-linux-x64@0.3.170` (112 MB in a
  stale pre-fix store output) while keeping the 3 MB JS SDK
  (`…claude-agent-sdk@0.3.170_…`). The current `nix eval`'d output contains no
  bundled binary, no Electron, and no node-pty prebuilds.
- **Ambiguous refs fail closed.** `rev-parse --symbolic-full-name` on an
  ambiguous name exits 0 but emits only warning text; `allowedRef` rejects it.
  The error message is misleading but the behavior is safe.
- **The `io.Pipe` plumbing has no deadlock or goroutine leak.** The writer is
  always closed by the `bundle.Wait` goroutine, `bundleDone` is buffered, and
  exec's stdin-copy goroutine terminates on EPIPE or EOF in every traced path,
  including early kubectl exit and `bundle.Start` failure.
- **Context/namespace discipline is airtight.** Every cluster-touching kubectl
  invocation carries explicit `--context` plus hardcoded
  `--namespace paw-workspace`; render refuses `--context`; destroy's manifest
  set is independent of profile/provider input; the image matrix rejects
  everything outside the eight reviewed pairs (verified by a render smoke test
  producing `paw-platform-readonly-opencode:dev` with matching annotations).
- **No remote push, mutation, or static credentials anywhere.**
  `git bundle create` and `rev-parse` are read-only against the source; the
  CLI's only mutations are `apply -k`/`delete -k` of its own embedded
  manifests; RBAC is an empty Role; SA token automount is off at pod and
  ServiceAccount; the unfree exception is the exact `claude-code`
  package-name predicate (`flake.nix:20`).
- **Single-writer is structurally sound:** one-replica StatefulSet
  (OrderedReady), RWO claim, headless service — pod identity prevents the
  two-writers-on-one-node RWO loophole.
- **Pairing paths:** TTL validated as `0 < ttl ≤ 1h` with a 10m default; pair
  output flows only through the operator's exec stream; revoke IDs are
  length-bounded and reject a leading `-`; connect binds `127.0.0.1` only.

## ADR-004 recommendation

**Accept after targeted revisions — do not split or reject.** The core
decision (operator-side selection, canonical-root + named-ref enforcement,
credential-free bundle streamed through the existing exec channel, detached
checkout, no remote, atomic staging, refuse-overwrite) is sound, minimal, and
consistent with ADR-001 §5 and ADR-003's repository-source adapter.
Canonical-root enforcement genuinely works: `Abs` + `EvalSymlinks` on both the
argument and `--show-toplevel` closes the symlink and subdirectory/parent
holes, and the tests cover them. "Committed named refs only" is the right
first contract — dirty-worktree and raw-SHA rejection keep selection
auditable, and both can be relaxed later without breaking the interface.

Required revisions before flipping to Accepted:

1. Close the ref-movement window (M2): guarantee the materialized content is
   exactly the reported commit (pod-side tip verification suffices).
2. Decide the `HEAD` question (L1) — recommend rejecting `HEAD`/`@` so
   "named ref" means named by the operator.
3. Make the overwrite refusal observable (M3) and race-free (L2).
4. Document submodule/LFS incompleteness and the absence of a stream timeout
   (L7).

## Merge-readiness, in stack order

| PR | Verdict | Notes |
| --- | --- | --- |
| #10 headless T3 runtime | Ready | L4 prune as follow-up |
| #11 v0 contract + threat model | Ready | Thorough negative tests |
| #12 Nix profile evaluation | Ready | Assertions pin the contract |
| #13 Kubernetes base + lifecycle | Ready | M4 (TMPDIR) as follow-up |
| #14 measured core image | Ready | L5 layer budget tautological |
| #15 provider images | Ready | SDK-binary removal verified |
| #16 platform-readonly image | Ready | Cloud-CLI exclusion enforced |
| #17 composed platform/provider images | Ready | |
| #18 image selection | Ready | Matrix rejects unknown pairs |
| #19 security contracts | Fix first | M1: secret volumes, regex |
| #20 operator workflows | Ready | L3 signals as follow-up |
| #21 live conformance | Blocked | H1; also L1 (`HEAD` in CI) |
| #22 repository materialization | Hold with ADR-004 | M2/M3, then ADR-004 |

The stack is linear, so #19's and #21's fixes ripple into later rebases —
cheapest to fix bottom-up before merging anything above #18.

## Remaining M1 gaps

Claims proven only structurally, only on Minikube, or not at all:

1. **Egress purposes resolve to nothing.** No adapter maps
   `approved-provider-api`/`selected-git-read` to destinations; under an
   enforcing CNI even DNS is blocked. Every provider image is currently
   non-functional for its purpose — provider conformance (issues #4, #8:
   auth, thread resume, supervised permissions) is unproven.
2. **Default-deny is unproven in practice** (H1), and Minikube's default CNI
   does not enforce NetworkPolicy — the README documents the CNI requirement
   but nothing validates it.
3. **client-access adapter** (#7): only the loopback port-forward exists; no
   TLS, no authenticated ingress, no session (vs. pairing) revocation story.
4. **provider-authentication adapter:** no mechanism yet to attach revocable
   provider state at runtime; pairing-credential expiry and revocation
   *effect* (using a revoked credential) are untested — only command success
   is checked.
5. **lifecycle-and-audit adapter:** no audit records exist at all.
6. **Multi-client thread resume** (ADR-001 validation #3, issue #8):
   untested.
7. **Portability:** no second conforming cluster; no native arm64
   measurements (acknowledged in ADR-002); Linux-only images with darwin
   limited to the CLI; the `x86_64-darwin` T3 package remains unverified.
8. **One workspace per cluster:** namespace `paw-workspace` and pod
   `workspace-0` are hardcoded — the ADR-001 workspace-per-trust-boundary
   model needs parameterization.
9. **No CI in-repo:** budget and contract enforcement runs only when someone
   invokes `nix flake check`; ADR-002's "CI records/enforces" is aspirational
   until a pipeline exists. Digest-pinned release manifests and registry
   publication are also open.
10. **Startup pairing-credential leak:** the `t3 start` rationale rests on
    upstream T3 behavior at warn level; the only verification is the live log
    grep — a T3 upgrade could regress silently. A dedicated at-rollout log
    assertion (before any `pair` call) would be stronger.

## Checks executed

| Check | Result |
| --- | --- |
| `go test ./...` | All packages pass |
| `go vet ./...` | Clean |
| `shellcheck` (both scripts) | Clean |
| `git diff --check origin/dev...HEAD` | Clean |
| `nix flake check --no-build` | x86_64-linux checks evaluate |
| Render smoke test via `go run` + kustomize | Correct image and annotations |
| Store-output inspection | Basis for H1 and L4; SDK/Electron absent |
| Empirical Git behavior checks | Basis for M2, L1, ambiguity non-finding |

Not run: the live conformance script, cluster creation, image loads, full
`nix flake check` builds, or anything touching secrets.

## Bottom line

The stack is unusually disciplined for its stage — #10–#18 and #20 are
mergeable as-is. The two items to insist on before trusting the security
story are H1 (the egress conformance check currently proves nothing) and M1
(the secret-attachment negative checks have real holes). ADR-004 should be
Accepted after the M2/M3/L1 revisions rather than reworked.
