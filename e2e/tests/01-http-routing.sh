#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
source "$SCRIPT_DIR/../helpers.sh"

info "applying gateway and http route..."
kubectl apply -f "$ROOT_DIR/k8s/samples/gateway.yaml"
kubectl apply -f "$ROOT_DIR/k8s/samples/httproute.yaml"

wait_for_service_port edgegate 80

start_port_forward edgegate 8080:80

info "testing HTTP routing..."
if ! retry 30 1 assert_status http://localhost:8080/get 200; then
    stop_port_forward
    fail "HTTP routing: expected 200 from /get"

    exit 1
fi
if ! assert_body_contains http://localhost:8080/get '"url"'; then
    stop_port_forward
    fail "HTTP routing: response body missing httpbin JSON"

    exit 1
fi

stop_port_forward
pass "HTTP routing works"
