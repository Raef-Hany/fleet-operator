#!/usr/bin/env bash
# Run a command inside a Linux Go container with envtest assets (envtest does not build on Windows).
# usage: hack/dtest.sh go test ./...
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"
MSYS_NO_PATHCONV=1 docker run --rm -v "$ROOT:/src" -v fleet-gomod:/go/pkg/mod -v fleet-gobuild:/root/.cache/go-build \
  -w /src golang:1.27 bash -c '
    export PATH=$PATH:/go/bin
    if ! command -v node >/dev/null; then (apt-get update -qq && apt-get install -y -qq nodejs >/dev/null) 2>&1 | tail -1; fi
    if [ ! -d /root/.local/share/kubebuilder-envtest ]; then go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest >/dev/null; fi
    export KUBEBUILDER_ASSETS=$(setup-envtest use 1.34.1 -p path)
    '"$*"
