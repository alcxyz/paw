# Focused PAW pilot

Prove one useful workflow before expanding the platform. See
[ADR-008](adr/ADR-008-focused-pilot-and-patch-export.md).

Execution is tracked in [GitHub issue #1](https://github.com/alcxyz/paw/issues/1)
under the **Pilot: One useful AI-assisted task** milestone.

## Scope

One engineer uses one `core` workspace and one provider from two browser devices.
Start with one selected repository and a small documentation or test change that
does not need cloud access, infrastructure credentials, or new package downloads.
Add a second repository only when required by the task. PAW itself is a suitable
candidate, subject to operator approval of the source revision and provider.

Do not evaluate another workspace platform, implement Compose, or expand the
provider/cluster matrix during this pilot. Existing functionality and tests are
retained. M1 remains the broader secure read-only qualification milestone.

## Readiness gates

These are gates, not claims that the integrations already exist:

- [ ] Select one provider and approve its authentication and data-use arrangement.
- [ ] Select a repository, named revision, and small task; retain the exact commit
  ID reported by import. Selected Git history is also transferred by the bundle.
- [ ] Prepare a disposable local Kubernetes environment and the selected image.
  Do not repair, replace, or repurpose an unrelated existing cluster implicitly.
- [ ] Pass current v2 baseline network verification and cleanup. Retain a known
  non-enforcing negative control for the security test, not a new support matrix.
- [ ] Enable provider authentication at runtime through a reviewed mechanism.
  Do not copy a host credential directory or mount SOPS identities. The
  reviewed mechanism is the pod-scoped login of
  [ADR-014](adr/ADR-014-pod-scoped-provider-login-state.md); the gate closes
  when the first pilot login completes through it.
- [ ] Prove approved provider access and denied unapproved access while retaining
  default-deny networking. Baseline pod-network checks do not prove this.
- [ ] Establish authenticated access from both devices. The current CLI tunnel
  binds loopback only; it is not phone ingress. Do not widen its bind address or
  reuse an unrelated T3 deployment to make the pilot appear functional.
- [ ] Define session revocation, state retention, and cleanup for the selected
  environment. Namespace/PVC disappearance does not establish data erasure.

Provider authentication, bounded egress, and remote browser access must be
completed before the AI task. Until then, an offline lifecycle smoke test is
useful but is not a completed pilot.

### Selected provider (2026-09-21)

The first pilot uses Codex with a fresh ChatGPT subscription login, not an API
key or a copied host authentication cache. The planned headless flow is
`codex login --device-auth`, completed by the operator in their browser with
device login enabled for their account or workspace. See the
[official authentication documentation](https://developers.openai.com/codex/auth).
Do not run login until bounded provider egress and credential storage have been
implemented and reviewed. In particular, the default Codex home must not place
its authentication cache on the existing persistent workspace-state volume.

The first aligned candidate used Codex `0.155.1` and T3
`0.0.42-fork.20+pa415f7130b`. ADR-009 subsequently moved PAW's default to
PAW-owned upstream pins; the operator's fork remains a custom build selection.
Record the exact selected image and versions before upgrading the pilot. Neither
this build change nor nightly update proposals upgrade the running browser-only
workspace or establish provider authentication readiness.

### Browser-only smoke evidence (2026-09-21)

The provider-free core image was started on a dedicated Minikube v1.38.1 /
Kubernetes v1.35.1 / containerd 2.2.1 environment. The v2 network verifier passed
its positive controls, ingress/egress denial, target-health checks, and cleanup.
The workspace reached readiness, its loopback HTTP endpoint returned 200, and
pairing creation and revocation commands succeeded without displaying credentials.
Runtime checks confirmed UID 65532 and absent service-account token and Docker
socket mounts. No AI-provider or cross-device browser session was tested.

Startup exposed containerd's rejection of absolute account-file symlinks. The
image now materializes its generated `/etc/passwd` and `/etc/group` as regular
files; live checks guard that compatibility requirement. The rebuilt core image
passed its existing size and runtime contracts. This is browser-only smoke
evidence, not a completed pilot or a new full portability qualification.

### Bounded egress evidence (2026-09-22)

The per-workspace egress proxy from
[ADR-013](adr/ADR-013-per-workspace-bounded-egress-proxy.md) was verified on
the same disposable Minikube, Calico-enforcing environment with the core image
and `scripts/check-minikube-live.sh`. From the workspace pod, a listed hostname
connected through the proxy, an unlisted hostname and a non-443 port were
refused, a direct connection that bypassed the proxy was dropped by policy, and
name resolution failed because the workspace has no DNS egress. The proxy log
recorded only hostnames and outcomes. This proves the boundary for the Codex
destination list; it does not establish provider authentication readiness,
which still needs the credential storage decision.

## Task acceptance

Runtime upgrades are a separate staged capability. New workspaces retain state
and repository volumes; earlier workspaces need explicit migration. Follow
[the upgrade guide](upgrades.md), not destroy/recreate, when preserving work.

1. Create and inspect the selected workspace. Confirm its profile and image.
2. Import the selected named repository revision using
   `paw workspace repository add`; record the returned commit ID outside the
   workspace.
3. Pair the first browser. Perform the selected task using supervised agent
   permissions. Keep pairing material out of reports and agent transcripts.
4. Connect the second browser to that same workspace and continue the same
   thread. Confirm prior messages and changes are present and there is no
   competing-writer error. No second T3 backend is started.
5. Pause editing, stage intended new files, and export changes using the command
   below. Resolve untracked/ignored files deliberately; never use blanket
   staging or deletion just to make export pass.
6. Review the private patch locally outside the agent transcript. In a separate
   clean checkout at the recorded base, check and apply the reviewed patch, then
   run the task's tests. PAW does not apply, commit, or publish it automatically.
7. Revoke client/provider sessions as supported by the reviewed integration and
   verify denial. Delete the workspace intentionally after retrieving the work;
   record Kubernetes-object cleanup separately from backing-storage reclamation.

Example export, with operator-selected non-secret values:

```sh
paw workspace repository export --adapter minikube --context "$PILOT_CONTEXT" \
  --name "$PILOT_REPOSITORY" --base-commit "$PILOT_BASE_COMMIT" \
  --output "$PILOT_PATCH_PATH"
```

The output path must be absolute and must not already exist. The base is the
full commit ID retained at import, not the workspace's current `HEAD`. Export is
a net binary-capable patch, not commit history, a backup, or a secret scan.
Submodule contents and Git LFS payload transfer are outside the pilot. Keep
artifacts private and do not upload them with the QA report.

## Evidence and stop conditions

Record only versions/image identity, pass/fail outcomes, elapsed setup effort,
task usefulness, and remaining limitations. Keep operational endpoints, provider
identity, pairing material, repository contents, and credentials out of shared
reports. A failure is useful feedback; it must not be converted into success by
disabling safeguards.

Stop for a concrete decision when provider authorization, repository selection,
or a new integration changes the approved scope. Fix only problems necessary for
this workflow. After the pilot, decide whether the next investment is usability,
platform-readonly access, another provider, or broader deployment support.
