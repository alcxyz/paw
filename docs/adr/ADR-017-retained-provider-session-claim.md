# ADR-017: Retained provider session claim

- Status: Accepted
- Date: 2026-09-23
- Amends: ADR-014 decisions 1 and 3; ADR-011 storage layout (`persistent-v2`)

## Context

ADR-014 kept provider login state on a memory-backed volume so that no
credential ever sat on retained storage or in a backup. Its accepted cost was
a new login after every pod replacement. In use that cost is wrong: T3, Codex,
and Claude Code all update by replacing the pod, and the operator's stated
requirement is that a valid login lasts as long as the provider allows, not
as long as one pod does. Ephemeral Codex transcripts also broke provider
thread resume in T3.

The properties ADR-014 protected still hold if the credential never enters a
backup, never appears in a manifest or image, and never passes through the
operator host. None of those require the credential to die with the pod.

## Decision

Replace the memory volume with a third persistent claim, `workspace-session`
(1 GiB, ReadWriteOnce, `retain-until-destroy`), mounted as the same
`subPath` directories `/workspace/session/codex` and
`/workspace/session/claude`. The storage layout becomes `persistent-v2`.

The session claim is outside the backup and restore contract:

- the backup helper mounts only the state and work claims, so an archive
  never contains a credential;
- restore never writes the session claim, so a restored workspace keeps
  whatever login its session claim already holds, or none;
- upgrade preflight validates the session mount but does not treat the claim
  as data to preserve or verify.

A login therefore survives pod replacement, upgrade, backup, and restore for
as long as the provider's token lives, and ends only with
`paw workspace logout`, provider-side revocation, or workspace destruction,
which deletes the claim with the namespace.

Everything else in ADR-014 stands: login runs inside the pod, PAW never
handles the credential, the requested provider must match the workspace, and
long-lived tokens and credential helpers are still not adopted.

## Consequences

### Positive

- One login per workspace lifetime, not per pod.
- Codex transcripts and Claude Code local state persist, so T3 provider
  threads resume after replacement.
- Backup, restore, and manifest guarantees are unchanged.

### Negative

- A credential is at rest on cluster storage for the workspace's lifetime.
  Encryption at rest is the storage environment's responsibility, and
  destroy is the only PAW-side erasure beyond logout.
- Legacy `persistent-v1` workspaces fail preflight and need recreation; none
  hold retained work.

## Alternatives considered

### Keep the memory volume and re-login per update

Rejected by the operator: updates are routine and a login is not.

### Persist only the credential file, not the provider state directory

Rejected. Neither Codex nor Claude Code separates its credential from its
state directory, and split mounts under a single `CODEX_HOME` are fragile.

### Persist login state on the state claim

Rejected. It would put the credential in every backup and restore.

## Validation

1. Rendered workspaces carry three claims and the session mounts; the
   contract, upgrade, and live checks pass with `persistent-v2`.
2. On a live workspace, a completed login survives a pod replacement without
   re-login, and a backup archive contains no session content.
