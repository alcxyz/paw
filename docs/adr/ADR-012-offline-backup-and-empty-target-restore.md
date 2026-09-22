# ADR-012: Offline backup and empty-target restore

- Status: Accepted
- Date: 2026-09-22
- Implements: ADR-011 backup/recovery stage

## Context

Persistent claims survive pod replacement but are not backups. Before enabling
runtime upgrades, PAW needs a consistent copy of both data volumes and a tested
way to restore it without erasing existing work. CSI snapshots are not available
uniformly across local and remote Kubernetes environments.

## Decision

- Add explicit operator commands for backup and restore. Neither is an upgrade
  command, a legacy migration, or a replacement for the existing create/destroy
  ownership rules.
- Backup requires the supported persistent layout and a healthy single writer.
  Acquire an exclusive lifecycle lock, revalidate identities, scale the writer
  down with object preconditions, and prove termination before accessing data.
  Resume only the unchanged original runtime after a verified backup and helper
  removal. Ambiguous failures retain the lock and stopped state for inspection.
- Use a dedicated minimal non-root helper image with a Go archive implementation.
  It contains no T3/provider process, shell, Kubernetes client, or credentials.
  Mount both claims read-only for backup. Keep namespace default-deny networking,
  disabled service-account token mounting, and restricted pod security.
- Stream the archive to a new private operator-selected directory. Validate it
  and record integrity information before reporting success. Never print payloads
  or raw workload/transport diagnostics. Artifacts may contain sensitive T3 and
  repository data; filesystem permissions are not encryption or content approval.
- The versioned format supports directories, regular files, and bounded relative
  symlinks, including binary, staged, untracked, and ignored repository data.
  Reject unsupported file types rather than silently omitting them. This is an
  application-data format, not a full filesystem snapshot: it does not promise
  preservation of arbitrary ownership, ACLs, extended attributes, or hardlinks.
  Reject symlink targets containing parent-traversal components, including
  apparently in-volume paths, to prevent chained-link escape. Restore uses
  directory-root-scoped filesystem operations. Setgid directories are supported
  for Kubernetes volume permissions; privileged regular-file modes are rejected.
  Preserve nested permissions, but leave mount-root permissions to the storage
  environment because the non-root helper does not own Kubernetes volume roots.
- Restore requires an explicitly confirmed, stopped, compatible workspace with
  empty destination claims. Validate the archive before writing. Never clear a
  claim, overwrite existing data, extract into the operator host, or choose a
  runtime from untrusted archive metadata. Leave the writer stopped after restore
  so the operator can review compatibility before starting it.
- Failed restore may leave partial data on those new claims. Keep that state
  stopped for inspection; do not attempt a destructive rollback or automatic
  cleanup of archive-controlled paths. Retry requires a separately reviewed empty
  destination. Runtime replacement remains a later stage.
- Minikube uses an explicitly preloaded development helper image. Portable
  Kubernetes requires an immutable helper image reference. Development tags are
  not release provenance and do not solve immutable local upgrade selection.

## Alternatives

- Copy from the running T3 pod: rejected because the two volumes and databases
  would not have a stopped-writer consistency boundary.
- Embed shell/tar tooling in every provider image: rejected; the helper keeps
  backup tooling independently versioned and absent from normal workspaces.
- Restore over existing data with a confirmation flag: deferred; empty targets
  make accidental data loss substantially harder and recovery easier to inspect.
- Automatically start the archived image: rejected; metadata and backups are
  untrusted inputs, and data/runtime compatibility needs explicit review.
- Require a storage snapshot controller: deferred under ADR-011 portability.

## Validation

Unit tests cover format/metadata validation, traversal and link rejection,
partial transfers, destination collisions, content preservation, and read/write
lifecycle ordering with failures. Build CI includes the helper image and keeps
its compressed archive below 32 MiB.

An opt-in disposable-cluster test must back up fixtures, verify the original
workspace resumed unchanged, restore to fresh stopped claims, then explicitly
start the test-owned writer and verify those fixtures again. It must refuse an
existing namespace and delete only its own fixture resources. A passing test is
not proof of authenticated provider state, T3 schema migration compatibility,
durable off-cluster backup storage, or production restore readiness.
