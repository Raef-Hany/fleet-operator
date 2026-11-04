# ADR-6: Pure rendering functions

Status: accepted

## Context

Reconcilers that build objects inline are hard to test without a cluster.

## Decision

`internal/render/k8s` and `internal/render/tf` are pure functions with golden-file tests. The reconciler only orchestrates.

## Consequences

Most behavior is unit-testable in milliseconds. Goldens must be reviewed by hand when regenerated.
