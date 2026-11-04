# ADR-9: Deterministic Job names

Status: accepted

## Context

A reconciler that creates a Job per reconcile would flood the cluster.

## Decision

Job name is `tf-<tenant>-<hash8>`, one Job per (instance, hash), with a TTL and `backoffLimit` set.

## Consequences

Creating the Job is idempotent (AlreadyExists is success). A changed hash produces a new Job and the old one is not recreated.
