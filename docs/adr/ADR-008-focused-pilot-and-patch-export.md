# ADR-008: Focused pilot and operator-controlled patch export

- Status: Accepted
- Date: 2026-09-08

## Context

PAW has reproducible images, lifecycle safeguards, repository import, and network
verification, but has not demonstrated a complete useful AI-assisted task.
Expanding provider coverage, cluster portability, and backend abstractions before
that feedback risks optimizing a workspace that is difficult to use.

ADR-003 prohibits remote Git push from the workspace. ADR-004 imports selected
committed repositories but leaves change retrieval to a separate operator path.
A useful pilot needs that return path without granting additional authority.

## Decision

### Validate one workflow before expanding the platform

Introduce a focused pilot before completing the broader M1 qualification:

- one engineer using multiple browser devices;
- one isolated workspace on one explicitly selected, disposable local
  Kubernetes environment;
- one selected provider and one representative repository/task, adding a second
  repository only if the task requires it;
- workspace-only authority, with no cloud or infrastructure identity;
- a reviewable change retrieved by the operator; and
- verified session revocation and cleanup with storage limitations explicit.

Use the existing `core` profile and provider image selection. Do not add a new
profile, runtime backend, controller, or workspace-management platform for this
pilot. Provider choice is an operator selection, not a permanent architectural
dependency. Multi-user identity, extra providers, remote portability lanes,
Compose, and a Coder evaluation are deferred until pilot feedback warrants them.

The pilot does not waive ADR-003, ADR-005, or ADR-007. A healthy environment,
current baseline enforcement evidence, bounded provider egress, suitable runtime
authentication, and authenticated client access remain prerequisites. No
unrestricted networking or host-credential inheritance is an acceptable shortcut.
The broader M1 acceptance criteria remain open; a pilot pass is not a release or
portability claim.

### Export an artifact, not authority

Add `paw workspace repository export` with an explicit context, repository name,
full base commit ID, and new absolute output path. The operator retains the
imported commit ID and supplies it as the base; workspace-controlled metadata is
not an authoritative record of the original selection.

The command retrieves a binary-capable Git patch through Kubernetes exec into a
private local file. It never applies a patch, changes the host checkout, invokes
remote Git push, or prints patch contents. It refuses existing output paths,
including symlinks, and does not publish a successful artifact from a failed or
interrupted transfer. Execution has a bounded deadline.

Export includes changes against the base in committed and tracked working-tree
content. New files must be deliberately staged first. Untracked content,
including ignored files, must not be silently omitted; unsupported repository
states are refused. External diff programs and text conversion are disabled.
Submodule contents and Git LFS payload transfer remain unsupported.

The operator pauses editing before export and reviews the artifact outside the
agent session before applying it in a separate review checkout. The artifact is
untrusted repository content and may contain sensitive data. File permissions
and output suppression are not secret scanning, content approval, or a guarantee
against a malicious workspace. Concurrent-edit snapshot consistency is not
claimed. Abnormal termination may leave private temporary data for operator
cleanup; deletion is not secure erasure.

## Alternatives

- Complete the full M1 matrix first: deferred because it postpones feedback on
  the basic user workflow. M1 remains required before claiming its guarantees.
- Replace the lifecycle platform immediately: deferred because that introduces
  another integration project before the existing workspace is tested for use.
- Allow workspace Git push: rejected for the pilot because it expands external
  authority and conflicts with ADR-003.
- Copy the whole working directory: rejected because it broadens exported data
  and can include Git configuration, generated files, and incidental state.
- Automatically apply changes to the source checkout: rejected because output
  from an agent-controlled workspace requires an independent review boundary.

## Validation

- Executable tests cover binary, committed, tracked, and staged new-file changes,
  invalid bases, incomplete-content refusal, output collisions, and failed or
  interrupted transfers.
- A pilot run records the exact image, source revision, provider, environment,
  task outcome, cross-device continuation, export review, and cleanup outcomes
  without recording pairing material, credentials, or repository payloads.
- The operator can review and apply the exported change in a separate checkout
  at the recorded base, then run the task's tests.
- Missing provider, ingress, or egress integration is recorded as a blocker,
  never treated as a passing pilot.
