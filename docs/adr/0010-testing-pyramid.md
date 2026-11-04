# ADR-10: Testing pyramid

Status: accepted

## Context

envtest runs a real API server but no controllers, so Deployments never become ready there.

## Decision

Unit tests cover rendering and validation, envtest covers the reconciler (tests set Deployment and Job status by hand), kind covers real readiness end to end in CI.

## Consequences

Fast feedback for most logic. envtest cannot prove rollouts, scheduling, namespace termination or garbage collection; the e2e test covers what it can.
