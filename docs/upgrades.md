# Workspace upgrades and preservation

PAW does not yet provide a mutating `workspace upgrade` command. A successful
image build or Minikube image load does not change an existing workspace.
Do not use `workspace destroy --delete-state` followed by create as an upgrade.

## Storage contract

New `persistent-v1` workspaces use separate persistent claims for T3 state
(`/workspace/state`) and repository working copies (`/workspace/work`). Both
claims are retained across ordinary pod replacement, including local repository
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

Only the first stage is implemented in this increment. A preflight pass is not
authorization to delete a pod and does not prove backup, recovery, authentication,
or egress readiness. Provider credentials remain separate work; do not copy host
credentials into either claim.

The planned upgrade deliberately interrupts browser connections and provider
turns. Operators must finish active work before it starts. Preserve a compatible
previous runtime and backup together: an image-only rollback after a database
migration may not be safe.

See [ADR-011](adr/ADR-011-state-preserving-workspace-upgrades.md) and
[issue #14](https://github.com/alcxyz/paw/issues/14) for implementation and
qualification requirements. No existing workspace is migrated by this change.
