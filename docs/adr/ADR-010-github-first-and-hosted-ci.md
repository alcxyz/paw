# ADR-010: Public GitHub-first hosting and hosted CI

- Status: Accepted
- Date: 2026-09-22
- Amends: ADR-009 hosting selection

## Context

PAW is intended for multiple operators and teams. Its initial private Forgejo
repository and container-backed CI were useful during development, but that
runner environment cannot provide the namespaces required by Nix sandboxing.
Creating a bespoke runner platform would add infrastructure before the focused
pilot is complete. The project owner has selected public GitHub-first hosting.

## Decision

- `github.com/alcxyz/paw` is the public canonical repository for source, issues,
  milestones, pull requests, CI, and releases.
- Preserve the reviewed source history. Do not publish private tracker
  discussions, untracked review reports, credentials, or operational transcripts.
  Migrate open planning items as reviewed public summaries.
- Keep `dev` as the integration/default branch and protected `main` for explicit
  promotion. Squash-merge feature PRs after current-head CI passes.
- Retain the original private Forgejo repository as a secondary source copy and
  historical tracker. It must not push back into GitHub or gate GitHub merges.
  Continuity synchronization is GitHub to Forgejo only; do not claim unattended
  synchronization until it is configured and verified.
- Use standard GitHub-hosted Linux VMs for initial native amd64 validation.
  Keep builds and the runner capability probe provider-neutral. Do not add a
  dedicated Forgejo VM service merely to qualify PAW.
- Require real Nix sandboxing, with fallback disabled. Before larger checks,
  perform a fresh local build proving host-marker exclusion and separate
  mount, PID, and network namespaces. Hosted placement alone is not proof.
- Keep action revisions pinned, checkout credential persistence disabled,
  read-only validation permissions, and explicitly bounded update-job writes.
  Do not store the workflow token in Nix configuration.
- Adapt the update publisher to GitHub directly, without a speculative
  multi-forge abstraction. Updates remain reviewed proposals, never deployments.

## Alternatives

- Dedicated ephemeral Forgejo VM runners: feasible in principle but deferred;
  their lifecycle, resource, credential and networking controls add maintenance
  unrelated to the current pilot.
- Disable Nix sandboxing or make shared runner containers privileged: rejected;
  neither satisfies ADR-009's build isolation requirement.
- Publish the original private issue discussions wholesale: rejected; reviewed
  summaries preserve planning without exposing private operational context.

## Validation and consequences

Publication requires a history secret scan and a content review; neither proves
the absence of every possible sensitive value. Local checks and an observed
GitHub-hosted run must pass before the migrated PR is considered merge-ready.
Nightly dispatch and publication need separate evidence after the workflow
reaches the default branch. ARM runtime qualification, provider login and the
useful cross-device pilot remain outstanding.

The migration changes hosting and CI, not the deployed PAW workspace or its
authentication. Historical issue numbers refer to the previous tracker; see
[the migration map](../project-status.md).
