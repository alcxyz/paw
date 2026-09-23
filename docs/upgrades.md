# Workspace upgrades and preservation

PAW does not yet provide a mutating `workspace upgrade` command. A successful
image build or Minikube image load does not change an existing workspace.
Do not use `workspace destroy --delete-state` followed by create as an upgrade.

## Storage contract

New `persistent-v2` workspaces use separate persistent claims for T3 state
(`/workspace/state`), repository working copies (`/workspace/work`), and
provider login state (`workspace-session`, ADR-017). All three claims are
retained across ordinary pod replacement, including local repository
changes. `/tmp` remains disposable. Explicit workspace destruction deletes the
namespace and claims; backing-volume reclamation remains environment-dependent.
Cluster deletion, node loss, and storage failure are not covered by this policy.

Earlier workspaces use `emptyDir` for repositories. Updating CLI binaries or
loading a new image does not migrate them. Stopping/replacing those pods can
destroy their repository working copies. Leave them running until a reviewed
migration has verified a complete backup, including untracked and ignored files.
Repository patch export is useful, but is not a substitute for that backup.

## Upgrade delivery stages

Inspect an existing workspace without stopping it:

```sh
nix run .#paw -- workspace upgrade-check \
  --adapter minikube --context YOUR_PAW_CONTEXT
```

Use `--adapter kubernetes` for the portable Kubernetes adapter. A nonzero result
means the current layout or observed workload state failed preflight. The check
never edits resources, creates a backup, or restarts a pod. Its observations are
point-in-time, not an exclusive lock against concurrent operators.

1. Persistent repository storage and read-only preflight.
2. Stopped-writer backup and tested compatible restore of both volumes.
3. Exclusive controlled restart with explicit runtime selection, bounded
   readiness checks, and interruption/failure recovery.
4. Explicit legacy migration and live qualification on disposable workspaces.

The first two stages have implementations, with qualification limits recorded
below. A preflight pass is not authorization to delete a pod and does not prove
backup, recovery, authentication, or egress readiness. Provider credentials remain
separate work; do not copy host credentials into either claim.

## Offline backup

Build and load the separate helper before starting a local backup:

```sh
nix build .#paw-backup-helper-image --out-link result-backup-helper
minikube -p YOUR_PAW_PROFILE image load ./result-backup-helper
nix run .#paw -- workspace backup --adapter minikube \
  --context YOUR_PAW_CONTEXT --output /absolute/private/new-backup
```

The output directory must not exist. Keep it outside Git checkouts and protect
it as sensitive data. Permissions are private, but the archive is not encrypted.
Do not paste the archive, metadata, or workload files into an agent conversation.
PAW validates the archive and writes integrity metadata; this is not a signature,
off-machine replication, or a storage-durability guarantee.

Backup acquires a lifecycle lock, stops the one writer, waits for termination,
then reads both claims through a restricted helper. On success it removes the
helper and resumes the same runtime, checking readiness before releasing the lock.
Finish active AI work first: browser connections and provider turns are interrupted.
The workspace must already use `persistent-v2`; legacy workspaces are refused.
The session claim is never part of a backup.

For `--adapter kubernetes`, also supply `--helper-image-ref` with a reviewed,
fully qualified `paw-backup-helper@sha256:...` image. The local `:dev` helper
is for development only. PAW does not publish or load images automatically.

## Empty-target restore

Restore is deliberately offline and non-overwriting:

```sh
nix run .#paw -- workspace restore --adapter minikube \
  --context YOUR_EMPTY_RESTORE_CONTEXT --input /absolute/private/new-backup \
  --confirm-empty-restore
```

Prepare a separate PAW-managed `persistent-v2` target using the canonical
manifests with the StatefulSet at **zero replicas from creation**. Its state and
work claims must be empty, and its profile, provider, and runtime reference must
match the backup. An ordinary `workspace create` starts T3 and initializes state,
so it is not empty-target provisioning. The opt-in test below demonstrates fresh
target provisioning; a user-facing destination-creation workflow remains future
work. Never clear an existing claim to satisfy this requirement.

Restore validates before extraction and rejects existing contents. It leaves
the writer stopped on success for explicit runtime-compatibility review. It does
not deploy an image named by the archive, change a runtime, or migrate schemas.
Mutable development tags can change meaning; verify the original image identity
before starting a restored local workspace.

The format preserves regular-file contents, subdirectory/file permissions, and
supported relative symlinks. It rejects unsafe paths and unsupported special
files. Symlink targets containing `..` components are rejected, even if a simple
lexical path appears to stay within a volume: link chains can escape that boundary.
Setgid directories used by Kubernetes volume permissions are supported; setuid
files and setgid regular files are not. It is not a filesystem snapshot and does
not preserve arbitrary ownership, ACLs, extended attributes, or hardlink identity.
Mount-root permissions remain environment policy; the non-root helper cannot
change a Kubernetes-owned volume root. Unsupported content must be
resolved explicitly; PAW does not silently skip it.

## Failure and recovery boundaries

A failed or interrupted lifecycle operation retains its lock and may leave a
stopped writer or helper pod. Inspect the selected context, workload/claim
identities, and operation status before recovery. Do not remove a lock or scale
up a writer while a helper still accesses the claims. Locks do not expire into
permission for another writer. Kubernetes administrators can bypass these
operator safeguards; this is not a controller enforcing exclusive access.

A failed restore may leave partial files on its new claims. They are retained
for inspection, never automatically deleted. Retry only with a separately
reviewed empty destination. Backups are untrusted data: integrity validation is
not approval to execute restored repository code.

## Opt-in qualification

On a separate disposable Minikube profile with core and helper images loaded:

```sh
nix build .#paw
PAW_LIVE_TEST=1 scripts/check-backup-live.sh YOUR_TEST_CONTEXT \
  "$PWD/result/bin/paw" /absolute/private/new-test-artifacts
```

This script requires Mike Farah `yq` v4, `jq`, and `kubectl`. It refuses an
existing `paw-workspace` namespace, tests backup and restore with harmless file
fixtures, and deletes only its own source/restore workspaces. It retains private
artifacts for inspection. It does not qualify authenticated provider state, T3
thread/schema migrations, or production restore readiness.

Provider logins live on the retained `workspace-session` claim (ADR-017): a
login survives pod replacement, restore, and upgrade for as long as the provider
allows, and ends only with `paw workspace logout` or workspace destruction.
Backups never contain provider credentials, and restore never writes the
session claim.

The planned upgrade deliberately interrupts browser connections and provider
turns. Operators must finish active work before it starts. Preserve a compatible
previous runtime and backup together: an image-only rollback after a database
migration may not be safe.

See [ADR-011](adr/ADR-011-state-preserving-workspace-upgrades.md) and
[issue #14](https://github.com/alcxyz/paw/issues/14) for implementation and
qualification requirements. No existing workspace is migrated by this change.
