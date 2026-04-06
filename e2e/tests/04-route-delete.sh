#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
source "$SCRIPT_DIR/../helpers.sh"

info "deleting http-route..."
kubectl delete httproute http-route

start_port_forward edgegate 8080:80

info "testing that deleted route returns 502..."
if ! retry 30 1 assert_status http://localhost:8080/get 502; then
    stop_port_forward
    fail "route delete: expected 502 after route removal"
    exit 1
fi

stop_port_forward

info "restoring routes..."
kubectl apply -f "$ROOT_DIR/k8s/samples/httproute.yaml"

pass "route deletion works"
