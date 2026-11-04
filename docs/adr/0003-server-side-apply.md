# ADR-3: Server-side apply with a fixed field owner

Status: accepted

## Context

Controllers that Update whole objects fight with other writers and cannot detect drift cheaply.

## Decision

All rendered objects are applied with server-side apply, field owner `fleet-operator`, forced ownership. Before a real apply the reconciler does a dry-run apply and compares resourceVersion, so a steady-state reconcile persists no writes.

## Consequences

Idempotent, drift-correcting and conflict-aware. Costs one extra dry-run request per object per reconcile.
