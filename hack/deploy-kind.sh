#!/usr/bin/env bash
# Create (if needed) a kind cluster, build + load both images, and deploy the operator.
set -euo pipefail
cd "$(dirname "$0")/.."
CLUSTER="${KIND_CLUSTER:-fleet-e2e}"
MGR_IMG="fleet-operator:e2e"
TF_IMG="fleet-operator-tfrender:e2e"

kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --image "${KIND_NODE_IMAGE:-kindest/node:v1.34.0}" --wait 180s
docker build -t "$MGR_IMG" .
docker build -t "$TF_IMG" -f Dockerfile.tfrender .
kind load docker-image "$MGR_IMG" --name "$CLUSTER"
kind load docker-image "$TF_IMG" --name "$CLUSTER"

kubectl config use-context "kind-$CLUSTER"
make deploy IMG="$MGR_IMG"
kubectl -n fleet-operator-system patch deployment fleet-operator-controller-manager --type=json \
  -p "[{\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",\"value\":\"--task-image=$TF_IMG\"}]"
kubectl -n fleet-operator-system patch deployment fleet-operator-controller-manager --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'
kubectl -n fleet-operator-system rollout status deployment/fleet-operator-controller-manager --timeout=180s
