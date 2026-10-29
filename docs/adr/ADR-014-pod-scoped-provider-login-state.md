# ADR-014: Provider login state inside the workspace

- Status: Accepted
- Date: 2026-09-22
- Updated: 2026-09-23 (retained login state, per ADR-017)
- History: [replaced text](archive/ADR-014-pod-scoped-provider-login-state-history.md)
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

### 1. Keep provider login state on a retained session claim outside backups

The workspace pod mounts the `workspace-session` claim (1 GiB,
`ReadWriteOnce`, `retain-until-destroy`) as two `subPath` directories,
`/workspace/session/codex` and `/workspace/session/claude`, so the kubelet
creates them inside the volume before the tools start; Codex refuses to run
when its home is missing, and a directory baked into the image would be hidden
under the mount. Provider images point their state directories there:
`CODEX_HOME=/workspace/session/codex` and
`CLAUDE_CONFIG_DIR=/workspace/session/claude`. The image contracts assert
exactly these variables for the matching provider image and none for others
([ADR-017](ADR-017-retained-provider-session-claim.md)).

The session claim is outside the backup and restore contract: the backup
helper mounts only the state and work claims, so an archive never contains a
credential; restore never writes the session claim; and upgrade preflight
validates the session mount but does not treat the claim as data to preserve
or verify.

### 2. Log in inside the pod, driven by the operator

`paw workspace login --provider PROVIDER` runs the provider's own login
command inside the workspace pod through an interactive `kubectl exec`. The
operator completes the browser step on their own machine and, where the flow
requires it, enters the resulting code in the same terminal. The credential
is written by the provider tool, in the pod, to the session volume. PAW never
sees, stores, forwards, or logs the credential, and no host credential
directory is copied.

`paw workspace logout --provider PROVIDER` runs the provider's logout command
the same way.

The requested provider must match the workspace's recorded provider; PAW
refuses to run one provider's login in another provider's image.

### 3. A login lasts for the workspace's lifetime

A login survives pod replacement, upgrade, backup, and restore for as long as
the provider's token lives, and ends only with `paw workspace logout`,
provider-side revocation, or workspace destruction, which deletes the claim
with the namespace. A restored workspace keeps whatever login its session
claim already holds, or none. Provider-side artifacts that share those
directories, such as Codex rollouts or Claude Code local history, persist with
the login, so T3 provider threads resume after replacement. T3 thread state
remains on the persistent state claim.

### 4. Do not adopt long-lived tokens or credential helpers yet

`claude setup-token`, `CLAUDE_CODE_OAUTH_TOKEN`, API keys in the environment,
and `apiKeyHelper` are not used. Each would move a credential through the
operator host, a manifest, or an environment variable that the contract
forbids. They remain possible later behind a reviewed external approval
boundary, which ADR-003 lists as follow-up work.

## Consequences

### Positive

- No credential ever reaches a manifest, image, backup, or the operator host.
- One login per workspace lifetime, not per pod.
- Revocation is explicit: log out, revoke at the provider, or destroy the
  workspace.
- The mechanism is provider-neutral: any tool with an in-terminal login and
  a relocatable state directory fits.

### Negative

- A credential is at rest on cluster storage for the workspace's lifetime.
  Encryption at rest is the storage environment's responsibility, and
  destroy is the only PAW-side erasure beyond logout.
- Legacy `persistent-v1` workspaces fail preflight and need recreation.

## Alternatives considered

### Persist provider state on the state claim and scrub backups

Rejected. It would put the credential in every backup and restore, and makes
backup safety depend on a scrub list.

### Keep provider state on a memory-backed volume

Rejected after use: updates replace the pod routinely, and a new login after
each one, with ephemeral transcripts that broke T3 provider thread resume, is
the wrong cost ([ADR-017](ADR-017-retained-provider-session-claim.md)).

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
