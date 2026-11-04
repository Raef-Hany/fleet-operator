# ADR-5: Finalizer for namespace cleanup

Status: accepted

## Context

Deleting the Instance must not leave tenant resources behind or report success while the namespace is still terminating.

## Decision

Finalizer `fleet.example.com/cleanup`: on delete the reconciler deletes the namespace, requeues until it is gone, then removes the finalizer.

## Consequences

Deletion is observable (phase `Deleting`). A stuck namespace blocks Instance deletion, which is the intended signal.
