# ADR-4: Status uses conditions

Status: accepted

## Context

Consumers need machine-readable progress, not log scraping.

## Decision

Status carries `phase`, `observedGeneration`, `terraformHash`, `lastTaskName` and conditions `Ready`, `KubernetesReady`, `TerraformRendered`, `TerraformValidated`.

## Consequences

Follows Kubernetes API conventions. Status is only written when it changed, which avoids reconcile loops.
