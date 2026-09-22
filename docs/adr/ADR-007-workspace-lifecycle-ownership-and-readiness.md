# ADR-007: Workspace lifecycle ownership and readiness

- Status: Accepted
- Date: 2026-09-07
- Upgrade/storage scope extended by ADR-011; create and destroy remain explicit.

## Context

The initial CLI applies a fixed workspace namespace and workload. Repeating
creation can update an existing workspace, while API acceptance alone reports
success even when its image or storage is unavailable. Deleting a rendered
manifest can remove an unrelated namespace with the same name. A preliminary
lookup is insufficient to protect deletion against namespace replacement.

ADR-003 requires a scoped workspace boundary; ADR-005 requires behavioral
network evidence. The CLI must uphold these requirements during lifecycle
operations as well as in the opt-in conformance script.

## Decision

- Creation runs the baseline environment verifier on the selected context.
  Failure, inconclusive results, or cleanup errors prevent workspace creation.
- Create the namespace atomically with a dedicated PAW ownership label before
  applying workload resources. Refuse any existing namespace. Updates are not
  implicit in the create operation.
- Wait up to two minutes for StatefulSet readiness. If application or readiness
  fails, return nonzero and retain partial resources for inspection and explicit
  deletion. If namespace creation has an ambiguous result, do not apply further
  resources or attempt speculative rollback.
- Deletion reads namespace ownership, then supplies its UID and resource version
  as Kubernetes deletion preconditions. Refuse unmarked namespaces and stale
  ownership. An absent namespace is an idempotent success.
- Wait up to two minutes for namespace deletion. Continue to distinguish
  Kubernetes object deletion from backing-volume reclamation and erasure.
- Existing namespaces without the ownership marker require operator-reviewed
  cleanup; PAW does not automatically adopt them.

The baseline network check does not establish provider, identity, storage, or
external egress conformance. Those accepted requirements remain separate work.

## Alternatives

- Keep apply-style creation: rejected because it silently changes an existing
  workspace and its selected profile or provider.
- Automatically delete partial creations: deferred until ownership and storage
  rollback can be guaranteed across ambiguous API responses and interruptions.
- Check labels and delete by name: rejected because a namespace can change
  between the ownership check and the deletion request.
- Treat API acceptance as readiness: rejected because pending storage or missing
  images would continue to produce misleading success.

## Validation

Tests cover failed verification, namespace collisions, application and readiness
failures, absent and unowned namespaces, UID/resource-version preconditions,
deletion failures, and bounded waits. Live qualification exercises the revised
network probe independently and keeps earlier probe-version evidence distinct.
