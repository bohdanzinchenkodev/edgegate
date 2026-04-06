#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
source "$SCRIPT_DIR/helpers.sh"

info "creating kind cluster '$CLUSTER_NAME'..."
kind create cluster --name "$CLUSTER_NAME" --wait 60s

info "building docker image..."
docker build -t "$IMAGE_NAME" -f "$ROOT_DIR/k8s/Dockerfile" "$ROOT_DIR"

info "loading image into kind cluster..."
kind load docker-image "$IMAGE_NAME" --name "$CLUSTER_NAME"

info "installing gateway API CRDs..."
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml

info "installing edgegate helm chart..."
helm install edgegate "$ROOT_DIR/k8s/helm" \
    --set image="$IMAGE_REPO" \
    --set tag="$IMAGE_TAG" \
    --set imagePullPolicy=Never

info "deploying upstream (httpbin)..."
kubectl apply -f "$ROOT_DIR/k8s/samples/upstream.yaml"

wait_for_deployment edgegate
wait_for_deployment httpbin

info "cluster ready"
