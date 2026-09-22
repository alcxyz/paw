# ADR-011: State-preserving workspace upgrades

- Status: Accepted
- Date: 2026-09-22
- Amends: ADR-004 repository storage and ADR-007 lifecycle scope

## Context

Loading a new image does not update a running workspace. Creation deliberately
refuses existing namespaces. T3 state currently uses a persistent claim, but
repository working copies use `emptyDir`: pod replacement can lose uncommitted,
untracked, and ignored files even when T3 threads survive. That is not an
acceptable foundation for routine upgrades.

## Decision

### Persist data independently of the runtime

New workspaces use two separate 10 GiB `ReadWriteOnce` claims:
`workspace-state` for T3 state and `workspace-work` for selected repositories.
Both survive ordinary pod replacement and remain until explicit workspace
deletion. Temporary files remain on `emptyDir`. Mark this layout
`paw.alc.xyz/storage-layout: persistent-v1` on the workload and its pod template.
Retention is not backup, high availability, secure erasure, or a promise that
the storage system survives cluster or node loss. `ReadWriteOnce` alone does not
enforce a single process writer; lifecycle orchestration must do that too.

Provider authentication remains a separate runtime attachment/retention design.
These claims do not authorize copying host credentials or storing provider
authentication in the existing persistent home. T3 state and future backup
artifacts may contain sensitive session data and must not be printed or committed.

### Controlled downtime, not concurrent writers

The upgrade contract is: validate ownership and storage, reserve exclusive
lifecycle ownership, stop the writer gracefully, prove its termination, back up
both volumes consistently, change only the explicitly selected runtime, start
one writer, and verify readiness. Preserve storage, profile authority, network
policy, and client-access boundaries. Kubernetes distribution and CSI snapshot
support must not become implicit requirements.

Backup artifacts belong in a new private operator-controlled destination, not
terminal output or the repository. Publish success only after a complete,
verified transfer. Record the old/new runtime identities and storage identities
without payloads. Interruption and ambiguous API responses must fail closed;
never delete data or automatically restart a second writer to conceal failure.

Do not automatically roll back only the image: the new T3 version may have
migrated data. Recovery must pair a reviewed previous runtime with its compatible
backup and be tested on disposable storage before a mutating upgrade is exposed.

### Stage the implementation

First deliver persistent repository storage and a read-only upgrade preflight.
This does not expose a mutating upgrade command or claim backup/recovery is
implemented. Follow with backup/restore, exclusive lifecycle transitions, and
failure-injection qualification under [issue #14](https://github.com/alcxyz/paw/issues/14).

Legacy `emptyDir` workspaces must be refused before stopping or replacing their
pod. Migration is a separate explicit operation requiring verified preservation
of all repository contents, including dirty, untracked, and ignored data. Patch
export alone is not a full workspace backup. Never reinterpret create or
destroy as an upgrade, and never silently adopt an unmarked namespace.

## Alternatives

- Rolling updates or blue/green against shared state: rejected because they can
  overlap writers and do not solve data-migration rollback.
- Keep repositories ephemeral and reimport commits: rejected because it loses
  local work and breaks continuity.
- One shared claim for everything: simpler, but couples repository lifecycle
  and backup selection to T3/provider data; separate claims preserve that boundary.
- Require CSI snapshots: deferred because local and remote environments differ;
  any future snapshot adapter must demonstrate consistent, restorable state.
- Add a controller or generic backend framework now: deferred; the existing
  operator CLI and Kubernetes backend suffice for a focused implementation.

## Validation

- Structural checks prove the two PVC mounts and ephemeral temporary storage.
- Preflight tests refuse legacy, unowned, missing, inconsistent, or unhealthy
  storage/workload state without writes.
- Live qualification must prove repository and T3 state survival across a
  controlled pod replacement, unchanged claim identities, and no writer overlap.
- Before mutating upgrades ship, test backup completeness, partial transfer,
  interruption, concurrency, readiness failure, and compatible recovery.
- The existing workspace remains untouched until its migration is reviewed.
