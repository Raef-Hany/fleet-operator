# ADR-1: Instance is a cluster-scoped CRD

Status: accepted

## Context

The control plane models one customer environment per custom resource. Each tenant gets its own namespace, so the owner of that namespace cannot itself live in a namespace.

## Decision

`Instance` (`fleet.example.com/v1alpha1`) is cluster-scoped, one per tenant. A cluster-scoped owner may own both the tenant Namespace and namespaced objects inside it.

## Consequences

Garbage collection and `Owns()` watches work across namespaces. RBAC for tenants cannot be scoped by namespace, which is acceptable for an operator-only API.
