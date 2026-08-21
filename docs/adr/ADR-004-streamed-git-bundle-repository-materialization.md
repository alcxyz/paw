# ADR-004: Streamed Git bundle repository materialization

- Status: Accepted
- Date: 2026-08-21

## Context

ADR-001 requires selected repository clones or worktrees and explicitly rejects
ambient organization checkouts. ADR-003 assigns repository selection to an
environment adapter with read-only source authority. The initial Kubernetes
base therefore starts with an empty `/workspace/work` volume.

The Minikube proof of concept needs a useful repository path without mounting a
host checkout, placing Git credentials in the pod, enabling unrestricted Git
egress, or introducing a second deployment model. It must also preserve the v0
prohibition on remote Git push.

## Decision

Add an operator-side command with an exact source, named revision, destination,
adapter, and Kubernetes context:

```text
paw workspace repository add \
  --adapter minikube \
  --context CONTEXT \
  --source PATH \
  --revision REF \
  --name NAME
```

For the initial local adapter:

- `PATH` must be the canonical root of one Git worktree, not a parent,
  subdirectory, organization checkout, or implicit current directory;
- `REF` must resolve to a local branch, tag, or remote-tracking ref; moving
  pseudo-refs such as `HEAD` and `@` are rejected;
- PAW resolves the ref to both its exact advertised object and peeled commit
  before transfer;
- Git writes a bundle to stdout and PAW streams it directly to `kubectl exec`
  stdin, without a host temporary archive;
- the pod receives the stream in its isolated `/tmp`, verifies that the bundle
  advertises exactly the selected ref at the selected object, imports it into
  an atomic staging directory without creating a remote, verifies and checks
  out the peeled commit in detached mode, and moves the result under
  `/workspace/work/NAME`; and
- materialization uses a no-clobber final rename and refuses to replace an
  existing destination, including one created concurrently.

The bundle includes committed objects reachable from the selected ref. It does
not include the host worktree's uncommitted files, Git configuration, credential
helpers, or usable remote. The operation does not add Git egress or credentials
to the workspace. A later repository-source adapter may clone from an approved
remote using read-only runtime identity and bounded egress while preserving the
same selection and destination contract.

Repository content remains untrusted and may itself contain sensitive material.
Explicit selection limits scope but is not content classification or secret
scanning. Operators and environment policy remain responsible for selecting a
revision suitable for the workspace and approved provider.

Change export remains a separate operator-controlled workflow. This proposal
does not add remote push, provider credentials, or a path for the workspace to
modify its Kubernetes policy.

## Consequences

### Positive

- Local repository materialization works through the same Kubernetes control
  plane used by remote workspaces.
- No ambient host path, host archive, Git credential, or remote configuration
  enters the pod.
- The exact selected commit is recorded in command output and the workspace
  starts from a detached revision.
- Atomic staging avoids leaving a partially materialized destination after a
  failed stream.

### Negative

- A full bundle may transfer more history than a shallow clone.
- Only committed named refs are supported initially; dirty worktrees and raw
  object IDs are rejected.
- Git submodules are transferred only as gitlink entries; their repositories
  and working trees are not materialized.
- Git LFS objects are not transferred; only committed LFS pointer files are
  present unless a future adapter explicitly supports LFS.
- The initial stream has no configurable deadline or resume support. Process
  failures are reported and pod-side staging is cleaned, but an interrupted or
  slow transfer must be retried from the beginning.
- The first command is operator-driven and does not yet model reusable
  multi-repository bundles in workspace metadata.
- The pod briefly stores the bundle in isolated ephemeral `/tmp` while
  importing it.

## Validation

- Unit tests reject subdirectories, unsafe destination names, and raw commits.
- CLI tests require every selection field and preserve it exactly.
- A live test must verify the detached commit, absent remotes, writable working
  tree, refusal to overwrite an existing destination, and workspace deletion.
- Structural deployment checks continue to reject host paths and credential
  attachment.
