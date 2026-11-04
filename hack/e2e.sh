#!/usr/bin/env bash
# Deploy the operator to a kind cluster and run the e2e test. KEEP_CLUSTER=1 keeps the cluster.
set -euo pipefail
cd "$(dirname "$0")/.."
CLUSTER="${KIND_CLUSTER:-fleet-e2e}"
cleanup() { if [ "${KEEP_CLUSTER:-0}" != "1" ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi; }
trap cleanup EXIT
hack/deploy-kind.sh
go test -tags=e2e ./test/e2e/ -v -timeout 40m
