# ADR-016: All-providers image composition

- Status: Accepted
- Date: 2026-09-23
- Amends: ADR-002 provider layers

## Context

ADR-002 composes images from a headless T3 core, one provider layer, and
profile tooling, and the image contracts enforce that a Codex image contains
no Claude Code or OpenCode and vice versa. That keeps each image small and its
attack surface reviewable, and it is the right shape for a workspace dedicated
to one provider.

It is the wrong shape for the pilot's actual use: one T3 instance where the
operator picks Codex or Claude Code per thread, or switches when one
subscription runs out of quota. PAW runs one workspace per cluster, so two
single-provider workspaces cannot stand in for one T3 with both providers.

Bounded egress (ADR-013) resolves destinations per provider, and pod-scoped
login state (ADR-014) already reserves a session directory per provider, so
nothing in the runtime contract assumes one provider per pod.

## Decision

Add `all` as a provider value alongside `none`, `codex`, `claude-code`, and
`opencode`. For each profile it composes every reviewed provider layer into
one image: `paw-all`, `paw-developer-all`, and `paw-platform-readonly-all`.
Single-provider images remain available and remain the lean choice; choosing
one or all is the operator's decision at create time.

An `all` image sets every provider's state variable and Claude Code's
nonessential-traffic switch, exactly as the single images do. Its egress
destination list is the union of the members' reviewed lists, in member
order. `paw workspace login --provider PROVIDER` accepts any member provider
when the workspace records `all`. The image contracts assert that every member
package is present, that the providers label names all three, and that the
environment carries exactly the members' variables.

Arbitrary subsets are not built. Fifteen images across three profiles is the
matrix CI measures and budgets; every subset would triple it for one use
case.

## Consequences

### Positive

- One T3 offers Codex, Claude Code, and OpenCode side by side, and the
  operator can move between them without replacing the workspace.
- No change to egress, login, or storage contracts; each provider keeps its
  own destinations and session directory.

### Negative

- The `all` images are the largest in the matrix and carry three provider
  runtimes that are all updated together.
- OpenCode is present but its login and state directory are not yet reviewed
  under ADR-014, so it is not usable for authenticated work in any image.

## Alternatives considered

### Two workspaces on one cluster

Rejected. PAW runs one workspace per cluster today, and two T3 instances are
not one thread list.

### Every provider subset

Rejected as a matrix explosion for no additional capability.

## Validation

1. The three `all` images build, pass their runtime and security contracts,
   and stay within their recorded budgets.
2. A rendered `all` workspace carries the union destination list and both
   provider state variables, and the contract script asserts it.
3. On a live `all` workspace, both Codex and Claude Code log in through
   `paw workspace login` and T3 reports both authenticated.
