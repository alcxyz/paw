# Second-pass review: PRs #10–#22 after remediation

- Date: 2026-08-21
- Reviewer: Claude (Fable 5), independent second pass; grunt-work
  verification delegated to a Codex CLI session (the requested Codex 5.6
  model is not available on this account, so the account's default Codex
  model was used; its findings were independently re-verified before
  acceptance)
- Base: `origin/dev` (0102b51); stack head:
  `feat/repository-bundle-materialization` (581424f)
- Prior review: `docs/reviews/2026-08-21-stack-review-pr10-22.md`
  (preserved, not modified)
- Remediation commits verified: 4a50439 (#13), e59b8b1 (#19), a3b2264
  (#20), a978df5 (#21), 581424f (#22); the #14–#18 heads are rebases with
  no content change to `flake.nix`, `nix/`, `deploy/`, or `contract/`
  (verified by `git diff 462349d..581424f` — exactly seven files changed,
  all in the remediated areas)

## Executive merge recommendation

**Merge the entire stack #10–#22 in numerical order.** All eight
actionable original findings (H1, M1–M4, L1–L3, L6) are genuinely
resolved at the current heads — verified by static inspection, unit
tests, adversarial fixtures, empirical Git/shell experiments, and two
live Minikube conformance runs (one on an enforcing CNI that passed, one
on a non-enforcing CNI that failed with the exact expected diagnostic,
proving the egress check is no longer vacuous). The two deferred Lows
(L4 foreign prebuilds, L5 tautological layer budget) remain intentional,
documented follow-ups. Flip ADR-004 from Proposed to Accepted as part of
merging #22: the revised text now matches the implementation exactly.

No new Medium- or High-severity findings. Six new Low/informational
observations are listed below; none blocks merging.

## Per-PR disposition

| PR | Head | Disposition |
| --- | --- | --- |
| #10 headless T3 runtime | 0bc3c9d | Merge (unchanged; L4 deferred) |
| #11 v0 contract + threat model | d359ed2 | Merge (unchanged) |
| #12 Nix profile evaluation | 80c2166 | Merge (unchanged) |
| #13 Kubernetes base + lifecycle | 4a50439 | Merge (M4 fixed) |
| #14 measured core image | 7039ea5 | Merge (rebase only; L5 deferred) |
| #15 provider images | d3a90ad | Merge (rebase only) |
| #16 platform-readonly image | b155850 | Merge (rebase only) |
| #17 composed platform images | 33c31b7 | Merge (rebase only) |
| #18 image selection | 14a61b2 | Merge (rebase only) |
| #19 security contracts | e59b8b1 | Merge (M1 fixed, self-testing) |
| #20 operator workflows | a3b2264 | Merge (L3, L6 fixed) |
| #21 live conformance | a978df5 | Merge (H1 fixed, live-proven) |
| #22 repository materialization | 581424f | Merge with ADR-004 Accepted |

## Original-finding resolution matrix

| ID | Verdict | Evidence at current head |
| --- | --- | --- |
| H1 | Resolved | See below |
| M1 | Resolved | See below |
| M2 | Resolved | See below |
| M3 | Resolved | See below |
| M4 | Resolved | See below |
| L1 | Resolved | See below |
| L2 | Resolved | See below |
| L3 | Resolved | See below |
| L4 | Accepted follow-up | Deferred; needs image rebaseline |
| L5 | Accepted follow-up | Deferred; nonblocking |
| L6 | Resolved | See below |
| L7 | Resolved | ADR-004 documents the limits |

### H1 — vacuous live egress check: Resolved (live-proven)

`scripts/check-minikube-live.sh:69-103` creates an unrestricted
positive-control pod (label `paw-live-conformance`, deliberately outside
the NetworkPolicy selector at `deploy/base/networkpolicy.yaml:9-12`);
`probe_egress` (lines 175-186) uses `bash` and coreutils `timeout` — both
actually present in the image — with a `command -v timeout` presence
guard and a `/dev/tcp` connect to a default IP endpoint (`1.1.1.1:443`,
lines 39-40), avoiding the DNS confound. Line 188 requires the control
pod to reach the endpoint before line 192 accepts the workspace pod's
failure as enforcement evidence. Probe host/port are validated
(lines 46-48) and passed as positional parameters, not interpolated.

Live evidence (this session): the full script **passed** on a disposable
Calico cluster, and **failed with exactly
`live conformance: default-deny egress is not enforced` (exit 1)** on a
disposable kindnet cluster, cleaning up its workspace on the failure
path. Both detection directions are real.

### M1 — manifest-contract blind spots: Resolved (self-testing)

`scripts/check-kubernetes-contract.sh:9-45` centralizes the predicate:
regular, init, and ephemeral containers are enumerated (lines 11-14)
with a non-vacuity guard `($containers | length) > 0` (line 15);
per-container securityContext requirements incl. empty `capabilities.add`
(lines 16-21); plain `secret` volumes rejected (lines 25-26); projected
`secret` sources rejected (lines 27-28); env regex extended with
`API_?KEY|BEARER|PASSPHRASE` (lines 37-40). Four mutation fixtures
(lines 131-163) prove the predicate rejects a secret volume, a projected
secret, an `API_KEY` variable, and a privileged init container; the
predicate also runs against every rendered profile/provider pair
(line 192).

Adversarial fixtures I ran beyond the built-in ones (with a passing base
manifest as sanity control): a privileged **ephemeral** container is
rejected; a plain secret volume is rejected; a CSI secrets-store volume
and a `GH_PAT` env name are still accepted (new Lows N2/N3 below).

### M2 — ref-movement TOCTOU: Resolved (empirically verified)

`internal/repository/repository.go:130-148` resolves both the advertised
ref object (`ref^{object}`) and the peeled commit (`ref^{commit}`); the
pod script (lines 178-189) compares `git bundle list-heads` output
against exactly `"$object $ref"` before import, unbundles into a fresh
`git init` staging repo (no remote, no refs), re-verifies
`$object^{commit} == $commit` in staging, then checks out detached.

Empirical verification (this session): an annotated tag flows correctly
end to end (tag object ≠ peeled commit; `list-heads` advertises the tag
object; unbundle + peel + detach succeed; staging ends with zero refs
and zero remotes), and a ref moved between resolution and bundling is
detected as a `list-heads` mismatch. Multi-ref bundles fail the
single-line equality. `TestResolvePreservesAnnotatedTagObjectAndPeeledCommit`
covers the tag case in unit tests.

### M3 — error precedence: Resolved

`internal/repository/repository.go:87-97` (`finishAdd`) returns the
kubectl/materialization error before the bundle error, and the pod
script now announces `destination … already exists` and
`… appeared during materialization` on stderr (lines 172-175, 191-194).
Unit test `TestFinishAddPrefersWorkspaceFailureOverBundlePipeFailure`
locks the ordering.

### M4 — TMPDIR portability: Resolved

`scripts/check-kubernetes-contract.sh:6-7` uses
`mktemp -d "${TMPDIR:-/tmp}/paw-kubernetes-contract.XXXXXX"` with a
quoted cleanup trap. Verified by running the script with `TMPDIR` unset
outside the Nix sandbox, including the full eight-pair render matrix —
it passed.

### L1 — implicit HEAD selection: Resolved

`internal/repository/repository.go:103-106` rejects `HEAD` and `@`
before resolution (unit-tested). The live script (lines 219-229) derives
an explicit named ref via `symbolic-ref`, with a detached-HEAD fallback
through `for-each-ref --points-at HEAD`, failing if no named ref exists.

### L2 — destination race: Resolved

Pod script line 190 uses `mv --no-clobber -T` followed by an explicit
`test -e "$staging"` failure check (lines 191-194). Verified on GNU
coreutils 9.11 (same coreutils family as the image): when the
destination appears concurrently, `mv -n -T` exits 0 without moving and
the post-check fires. Nothing is ever merged into an existing
destination.

### L3 — signal forwarding: Resolved (behaviorally verified)

`internal/cli/cli.go:483-519` subscribes to SIGINT/SIGTERM and forwards
them to the child while waiting on a buffered completion channel (no
goroutine leak; late signals harmless). Behavioral test (this session):
`SIGTERM` sent to a running `paw workspace connect` was received by the
kubectl-shim child, and paw exited with the child's status. Unit tests
cover forwarding and completion-race paths.

### L6 — flag-like option values: Resolved

`internal/cli/cli.go:463-465` (`optionValueMissing`) rejects values
beginning with `--` for every workspace option and for repository-add
parsing (lines 238-251), with a table-driven test across all five
repository options. Single-dash values can still be consumed; PAW
exposes only long options, so no recognized flag can be swallowed.

### L7 — ADR-004 limitations: Resolved

ADR-004 now documents submodule gitlink-only transfer, LFS
pointer-file-only transfer, and the absence of a stream deadline/resume
(Negative consequences), and its decision text matches the
implementation: pinned object+commit resolution, bundle-head
verification, no-remote import, no-clobber rename, and HEAD/`@`
rejection. Status remains Proposed pending the flip recommended here.

## New findings (ordered by severity; none blocks merge)

### N1 (Low, lifecycle): destroy does not verify PV reclamation

Live evidence: after a successful `workspace destroy --delete-state` on
the Calico cluster, the namespace and PVC were gone but the backing PV
remained `Released` (reclaim policy `Delete`) for at least ~4 minutes
until profile deletion. On minikube's hostpath provisioner, workspace
state can linger on the node after destroy. The live script asserts
namespace deletion only (`scripts/check-minikube-live.sh:296-298`).
Recommend: poll for PV deletion in the live script, or document the
provisioner dependency in the state-deletion claims (issues #5/#8).

### N2 (Low, contract boundary): CSI secret volumes are not rejected

An adversarial fixture adding a `csi` volume with
`secrets-store.csi.k8s.io` and a `secretProviderClass` passes
`assert_security_contract` (verified empirically). Add a rejection for
`csi` volumes (or specifically secrets-store drivers) to
`scripts/check-kubernetes-contract.sh`.

### N3 (Low, contract boundary): credential env denylist is incomplete

`GH_PAT` (and similar names without the listed substrings) passes the
env-name regex. Inherent to the denylist approach; consider an
allowlist of the six expected image variables for the pod env check,
mirroring what the image contract already does.

### N4 (Low, test quality): pod materialize script tested only as text

`internal/repository/repository_test.go:95-109` asserts required
fragments of `materializeScript` but never executes it. The flow is
executable hermetically (sh + git against temp dirs, no kubectl);
an end-to-end unit test would catch regressions in the bundle-head
verification and no-clobber logic. (Found independently by the Codex
pass; I verified the script's behavior manually this session.)

### N5 (Low, ergonomics): no force-exit on repeated interrupt

`executeCommandWithSignals` forwards every signal but never gives up; a
child that ignores SIGINT/SIGTERM keeps paw waiting indefinitely. A
second-interrupt force-exit (or kill-after-grace) would match common CLI
behavior. (Codex finding; verified by inspection.)

### N6 (Informational): same-UID availability race after install

Between the no-clobber `mv` and the staging post-check, a process that
already has full workspace filesystem authority could remove the
installed destination, producing a false failure. Not a boundary issue.
(Codex finding; concur with Informational.)

## Evidence actually obtained this session

Static and unit:

- `go test ./...` at 581424f: all packages pass; `go vet` clean.
- Full `nix flake check` (not `--no-build`): **passed** — image
  derivations cached (no Nix inputs changed), `paw` rebuilt with the new
  tests in-sandbox, and the remediated kubernetes-contract check ran its
  four rejection fixtures.
- `shellcheck` on both scripts: clean. `git diff --check`: clean.
- `scripts/check-kubernetes-contract.sh` run directly with `TMPDIR`
  unset, including the eight-pair render matrix: passed.
- Adversarial jq fixtures against the extracted security predicate (with
  a passing-base sanity control): ephemeral privileged container
  rejected, secret volume rejected, CSI secret volume accepted (N2),
  `GH_PAT` accepted (N3).
- Empirical Git experiments: annotated-tag object/commit distinction
  through the full simulated pod flow; ref-movement detection via
  `bundle list-heads` mismatch; `mv --no-clobber -T` semantics on
  coreutils 9.11.
- Behavioral signal test: SIGTERM to `paw workspace connect` reached the
  child process; paw exited promptly.
- Codex CLI adversarial pass (read-only sandbox, default model): all
  eight findings Resolved, no new Medium/High; contributed N4–N6. Its
  file:line citations were spot-checked against the working tree.

Live (disposable clusters created and deleted this session; no existing
cluster, namespace, or profile touched):

- `paw-rereview-20260821` (docker driver, **Calico** CNI): loaded the
  Nix-built `paw-core:dev`; full
  `PAW_LIVE_TEST=1 scripts/check-minikube-live.sh` run **passed**
  (exit 0) — create, rollout, positive-control egress success, workspace
  egress denial under real NetworkPolicy enforcement, restricted-pod and
  RBAC/socket/token checks, loopback connect, pinned-ref repository
  streaming with detached/no-remote/writable verification, overwrite
  refusal, pairing mint and revoke, clean logs, destroy, namespace gone.
- `paw-rereview-noenf-20260821` (docker driver, default kindnet CNI, no
  policy enforcement): the same script **failed with exit 1 and exactly
  `default-deny egress is not enforced`**, and its cleanup trap removed
  the workspace it had created. This proves the H1 remediation
  distinguishes enforcement from non-enforcement in both directions.
- Both profiles were deleted afterwards; no `paw-rereview` entries
  remain in any kubeconfig.

Distinguished from prior evidence: the first review ran no live cluster
at all; every live claim above is new to this pass.

## Remaining accepted follow-ups and broader v0 gaps

Accepted follow-ups (tracked, nonblocking): L4 foreign
bufferutil/utf-8-validate prebuilds (needs image rebaseline), L5
tautological layer-count budget, and new N1–N6 above.

Broader v0/M1 gaps, unchanged from the first review and still open:

1. No adapter resolves egress purposes to destinations; provider images
   cannot reach provider APIs under an enforcing CNI, so provider
   conformance (issues #4, #8) remains unproven.
2. No client-access adapter (TLS ingress, session revocation) beyond the
   loopback port-forward.
3. No provider-authentication runtime attachment mechanism; pairing TTL
   expiry and revocation *effect* remain untested.
4. No lifecycle-and-audit records.
5. Multi-client thread resume untested; no second conforming cluster.
6. No native arm64 measurements; darwin limited to the CLI.
7. One workspace per cluster (hardcoded namespace/pod).
8. No in-repo CI pipeline; budgets enforced only by local
   `nix flake check`.
9. Startup pairing-credential silence rests on upstream `t3 start`
   behavior; the live log grep is the only guard (held during the live
   run this session).

## Recommended merge order

Merge in numerical stack order, each into `dev` after its predecessor,
preserving PR boundaries: #10, #11, #12, #13, #14, #15, #16, #17, #18,
then #19, #20, #21, and #22. Flip ADR-004 to Accepted in or
immediately after #22. After the stack lands, open follow-up issues for
N1–N6 plus the deferred L4/L5 so they are not lost with the review
documents.
