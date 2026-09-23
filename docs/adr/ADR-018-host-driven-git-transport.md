# ADR-018: Host-driven Git transport into the workspace

- Status: Accepted
- Date: 2026-09-23
- Extends: ADR-004 bundle materialization; ADR-008 patch export

## Context

Repositories enter a workspace as a streamed bundle of one named ref, and
work leaves it as a reviewed patch. Both are credential-free by design, and
ADR-003 forbids remote Git push from the workspace and any ambient
credential inside it. In daily use the pair is not practical: bringing new
commits into an existing checkout means re-importing, and a patch discards
the commit history an agent produced.

The operator's host already has the Git identity that matters, an SSH key
for GitHub and Forgejo. Forwarding that key or its agent into the pod would
give the workspace push authority over the operator's repositories, which is
exactly what the contract prohibits. What is needed is the reverse: let the
host's Git talk to the workspace copy as if it were a remote, so the host
fetches and pushes with its own identity and the workspace never holds one.

Git's `ext::` transport runs a command and speaks the Git wire protocol over
its stdin and stdout, and `kubectl exec` carries a byte stream into a pod.
Together they make the workspace repository reachable from the host without
any network path, remote, or credential inside the workspace.

## Decision

Add `paw workspace repository remote --name NAME --service SERVICE` as a Git
`ext::` helper. Git invokes it with `%S` substituted by `git-upload-pack` or
`git-receive-pack`; PAW validates the name and service, then tunnels the
protocol stream to that service running against `/workspace/work/NAME`
through `kubectl exec --stdin`. Only those two services are accepted, and
PAW writes nothing to stdout outside the Git stream.

On the host:

```sh
git config --global protocol.ext.allow user   # Git disables ext:: by default
git remote add paw "ext::paw workspace repository remote --adapter minikube \
  --context CONTEXT --name paw --service %S"
git push paw dev            # host commits into the workspace copy
git fetch paw               # workspace commits back to the host
```

Every transfer is initiated by the operator on the host, under the host's
Git identity and kubectl access. The workspace keeps no remote, so nothing
inside it can push anywhere; publishing to GitHub or Forgejo remains a host
action after review. Pushes into the workspace land on branches; Git's
default refusal to update the checked-out branch stays in force, so a push
never rewrites a working tree an agent is editing.

Bundle import remains the way to create a workspace copy, and patch export
remains available. This decision adds the transport; it does not grant the
workspace any egress purpose or credential, and it does not change the
egress, login, or backup contracts.

## Consequences

### Positive

- Native `git fetch` and `git push` between host and workspace, with full
  history in both directions and no credential in the pod.
- Works identically on Minikube and on any cluster the host can reach with
  kubectl; no ingress, registry, or Git-host egress is required.
- Review and publication stay on the host, as ADR-008 intends.

### Negative

- Transfers need the operator's kubectl access; the workspace cannot pull on
  its own. Read-only Git egress with a leased credential remains separate
  follow-up work.
- Large transfers ride the API server exec channel, which is slower than a
  direct Git connection.

## Alternatives considered

### Forward the host SSH agent into the pod

Rejected. It gives the workspace the operator's push authority.

### A read token for the Git host inside the workspace

Deferred. It needs the `selected-git-read` egress purpose and a leased
credential mechanism; ADR-003 lists that boundary as follow-up work.

### Keep bundle import and patch export only

Rejected as impractical for daily development; history and incremental
updates matter.

## Validation

1. `git fetch` from the host over the transport returns the workspace HEAD,
   and `git push` from the host creates a branch inside the workspace.
2. A commit made inside the workspace reaches the host by `git fetch` with
   its history intact.
3. The workspace repository still has no remote configured afterwards.
