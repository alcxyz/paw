# ADR-014: Pod-scoped provider login state

- Status: Accepted; amended by ADR-017 (login state moved to a retained claim)
- Date: 2026-09-22
- Implements: ADR-003 runtime credential attachment; unblocks ADR-008 pilot gate

## Context

Provider tools authenticate with a subscription login and keep a credential
cache on disk: Codex under `CODEX_HOME` and Claude Code under
`CLAUDE_CONFIG_DIR`. Both default to the home directory, which in a PAW
workspace is the persistent state claim. That claim survives pod replacement,
is retained until destroy, and is captured by ADR-012 backups. A credential
cache there would be an ambient credential in exactly the sense ADR-003
forbids, and the pilot gate in ADR-008 says the cache must not land on the
state volume.

The workspace contract also forbids Kubernetes Secret volumes, secret
references, projected secrets, and CSI secret drivers in workspace manifests,
and PAW must not copy a host credential directory into the pod or mount a
decryption identity. Bounded egress (ADR-013) now lets a provider tool reach
its authentication endpoints from inside the pod, so the login can happen
where the credential is used.

Both providers offer a login that does not need a browser on the same
machine: `codex login --device-auth` prints a link and a one-time code and
polls for up to fifteen minutes; `claude auth login` prints a link and
accepts the resulting code. Both have a logout command that deletes the
stored credential.

## Decision

### 1. Keep provider login state on an in-memory, pod-scoped volume

The workspace pod mounts one `emptyDir` with `medium: Memory` as two
`subPath` directories, `/workspace/session/codex` and
`/workspace/session/claude`, so the kubelet creates them inside the volume
before the tools start; Codex refuses to run when its home is missing, and a
directory baked into the image would be hidden under the mount. Provider
images point their state directories there: `CODEX_HOME=/workspace/session/codex`
and `CLAUDE_CONFIG_DIR=/workspace/session/claude`. Nothing on that volume
survives pod replacement, appears in a backup, or is written to node disk.
The image contracts assert exactly these variables for the matching provider
image and none for others.

### 2. Log in inside the pod, driven by the operator

`paw workspace login --provider PROVIDER` runs the provider's own login
command inside the workspace pod through an interactive `kubectl exec`. The
operator completes the browser step on their own machine and, where the flow
requires it, enters the resulting code in the same terminal. The credential
is written by the provider tool, in the pod, to the session volume. PAW never
sees, stores, forwards, or logs the credential, and no host credential
directory is copied.

`paw workspace logout --provider PROVIDER` runs the provider's logout command
the same way. Replacing the pod also discards every login.

The requested provider must match the workspace's recorded provider; PAW
refuses to run one provider's login in another provider's image.

### 3. Accept per-pod re-login and ephemeral provider-side state

Because the provider state directories are pod-scoped, a workspace needs a
new login after every pod replacement, including after a backup, restore, or
upgrade. Provider-side artifacts that share those directories, such as Codex
rollouts or Claude Code local history, are ephemeral too. T3 thread state
remains on the persistent state claim. Whether T3 can continue a provider
thread after the provider's own transcript is gone is a pilot finding to
record, not a reason to move credentials back to persistent storage.

### 4. Do not adopt long-lived tokens or credential helpers yet

`claude setup-token`, `CLAUDE_CODE_OAUTH_TOKEN`, API keys in the environment,
and `apiKeyHelper` are not used. Each would move a credential through the
operator host, a manifest, or an environment variable that the contract
forbids. They remain possible later behind a reviewed external approval
boundary, which ADR-003 lists as follow-up work.

## Consequences

### Positive

- No credential ever reaches a manifest, image, persistent claim, backup, or
  the operator host; the pilot gate on credential storage is satisfied.
- Revocation is simple and complete: log out or replace the pod.
- The mechanism is provider-neutral: any tool with an in-terminal login and
  a relocatable state directory fits.

### Negative

- Operators log in again after every pod replacement.
- Provider-side transcripts and local settings do not persist; resume
  behavior of T3 provider threads across replacement needs pilot evidence.
- Memory-backed storage counts against the pod's memory limit; the volume is
  capped at 64 MiB.

## Alternatives considered

### Persist provider state on the state claim and scrub backups

Rejected. It leaves a live credential at rest on retained storage, contrary
to ADR-003, and makes backup safety depend on a scrub list.

### A separate persistent claim for provider state

Rejected for the same reason: retained storage with a credential at rest,
plus a third claim to reason about in backup and restore.

### Inject a token from the operator host

Rejected for now. Copying a host credential or a minted long-lived token into
the pod moves the credential through the host and the manifest or environment,
which the contract forbids.

### Kubernetes Secret with a projected volume

Rejected by the existing contract, which the manifest tests enforce.

## Validation

The implementation is complete when:

1. provider images set the state variables above and the image contracts
   enforce them, with the session volume in the rendered workspace and the
   contract, upgrade, and live checks updated;
2. `paw workspace login` and `logout` exist, refuse a provider that does not
   match the workspace, and run the provider commands interactively in the
   pod;
3. a disposable Codex workspace reaches the device-code prompt through the
   bounded egress proxy, with the session directory created on the session
   volume and nothing written under the state claim; and
4. the first pilot login is completed and recorded by the operator.

## References

- [Claude Code authentication](https://code.claude.com/docs/en/authentication)
- [Codex authentication](https://developers.openai.com/codex/auth)
