# ADR-11: Ringed rollout (stretch)

Status: accepted

## Context

Changing a version on every Instance by hand does not scale.

## Decision

A `FleetRollout` controller advances a target version ring by ring (internal, trial, prod), waiting for a bake time and pausing on failure.

## Consequences

Only built if time allows; if absent from the README's feature list it was not built.
