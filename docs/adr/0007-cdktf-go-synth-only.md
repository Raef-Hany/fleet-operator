# ADR-7: Terraform via CDKTF in Go, synth only

Status: accepted

## Context

The target role involves debugging CDK for Terraform. This project should exercise it without needing a cloud account.

## Decision

Terraform is generated with CDKTF in Go using the prebuilt `cdktf-provider-google-go` module. It is only synthesized and checked with `terraform validate`; it is never applied.

## Consequences

No credentials anywhere. Note that HashiCorp has announced that CDKTF is deprecated, so this is a learning exercise and not a recommendation for new systems.
