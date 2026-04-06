#!/usr/bin/env bash

CLUSTER_NAME="edgegate-e2e"
IMAGE_REPO="edgegate-k8s"
IMAGE_TAG="e2e"
IMAGE_NAME="$IMAGE_REPO:$IMAGE_TAG"
PORT_FORWARD_PID=""

info()  { printf "\033[1;34m[INFO]\033[0m  %s\n" "$*"; }
pass()  { printf "\033[1;32m[PASS]\033[0m  %s\n" "$*"; }
fail()  { printf "\033[1;31m[FAIL]\033[0m  %s\n" "$*"; }

retry() {
    local max=$1 interval=$2
    shift 2
    for i in $(seq 1 "$max"); do
        if "$@"; then
            return 0
        fi
        sleep "$interval"
    done
    return 1
}

assert_status() {
    local url=$1 expected=$2
    shift 2
    local got
    got=$(curl -s -o /dev/null -w '%{http_code}' "$@" "$url")
    if [ "$got" != "$expected" ]; then
        fail "expected status $expected, got $got for $url"
        return 1
    fi
    return 0
}

assert_body_contains() {
    local url=$1 substring=$2
    shift 2
    local body
    body=$(curl -s "$@" "$url")
    if echo "$body" | grep -q "$substring"; then
        return 0
    fi
    fail "body does not contain '$substring' for $url"
    echo "  got: $body"
    return 1
}

start_port_forward() {
    local svc=$1 ports=$2
    kubectl port-forward "svc/$svc" "$ports" &
    PORT_FORWARD_PID=$!
    sleep 2
}

stop_port_forward() {
    if [ -n "$PORT_FORWARD_PID" ]; then
        kill "$PORT_FORWARD_PID"
        wait "$PORT_FORWARD_PID" || true
        PORT_FORWARD_PID=""
    fi
}

dump_debug() {
    echo ""
    info "=== edgegate logs ==="
    kubectl logs deployment/edgegate --tail=50 2>&1 || true
    echo ""
    info "=== edgegate service ==="
    kubectl get svc edgegate -o yaml 2>&1 || true
    echo ""
    info "=== gateway resources ==="
    kubectl get gateway,httproute,gatewayclass -A 2>&1 || true
    echo ""
    info "=== pod status ==="
    kubectl get pods -A 2>&1 || true
    echo ""
}

wait_for_deployment() {
    local name=$1 timeout=${2:-120}
    info "waiting for deployment/$name to be ready..."
    kubectl rollout status "deployment/$name" --timeout="${timeout}s"
}

wait_for_service_port() {
    local svc=$1 port=$2
    info "waiting for service/$svc to have port $port..."
    retry 30 2 kubectl get svc "$svc" -o jsonpath="{.spec.ports[?(@.port==$port)].port}"
}
