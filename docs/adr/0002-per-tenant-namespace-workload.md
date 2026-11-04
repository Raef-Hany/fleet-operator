# ADR-2: Per-tenant namespace with podinfo workload

Status: accepted

## Context

The project needs a real workload with versions, health endpoints and clean rollouts so upgrades can be demonstrated without building an application.

## Decision

Each Instance reconciles into namespace `inst-<tenant>` containing a ServiceAccount, ResourceQuota, default-deny NetworkPolicy plus an explicit allow rule, a Deployment and a Service. The image is `ghcr.io/stefanprodan/podinfo:<spec.version>`.

## Consequences

Upgrades are a one-field change. podinfo is a stand-in; nothing here claims to run a real product.
