# ADR-8: Terraform rendering runs as a Kubernetes Job

Status: accepted

## Context

CDKTF needs Node at synth time (jsii). Putting Node into the manager would bloat it and widen its attack surface.

## Decision

Synthesis runs in a separate `tfrender` image executed as a Kubernetes Job per instance, the same shape as a task dispatched per instance.

## Consequences

The manager stays a small pure-Go binary. Results come back through Job status, not stdout.
