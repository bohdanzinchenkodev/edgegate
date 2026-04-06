#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/../helpers.sh"

CN="app.example.com"
TLS_DIR=$(mktemp -d)

info "generating self-signed TLS certificate..."
openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "$TLS_DIR/tls.key" -out "$TLS_DIR/tls.crt" \
    -days 1 -subj "/CN=$CN"

info "creating TLS secret..."
kubectl create secret tls app-tls \
    --cert="$TLS_DIR/tls.crt" --key="$TLS_DIR/tls.key" \
    --dry-run=client -o yaml | kubectl apply -f -

rm -rf "$TLS_DIR"

wait_for_service_port edgegate 443

start_port_forward edgegate 8443:443

info "testing HTTPS with TLS termination..."
if ! retry 30 1 assert_status "https://$CN:8443/get" 200 \
    --resolve "$CN:8443:127.0.0.1" -k; then
    stop_port_forward
    fail "TLS: expected 200 from HTTPS /get"
    exit 1
fi

if ! assert_body_contains "https://$CN:8443/get" '"url"' \
    --resolve "$CN:8443:127.0.0.1" -k; then
    stop_port_forward
    fail "TLS: response body missing httpbin JSON"
    exit 1
fi

stop_port_forward
pass "TLS termination works"
