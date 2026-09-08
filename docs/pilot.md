# Focused PAW pilot

Prove one useful workflow before expanding the platform. See
[ADR-008](adr/ADR-008-focused-pilot-and-patch-export.md).

Execution is tracked in [Forgejo issue #38](https://git.alc.xyz/alcxyz/paw/issues/38)
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
  Do not copy a host credential directory or mount SOPS identities.
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

## Task acceptance

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
