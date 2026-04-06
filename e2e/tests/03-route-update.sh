#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
source "$SCRIPT_DIR/../helpers.sh"

info "deploying second upstream (nginx)..."
kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx-upstream
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx-upstream
  template:
    metadata:
      labels:
        app: nginx-upstream
    spec:
      containers:
        - name: nginx
          image: nginx:alpine
          ports:
            - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: nginx-upstream
spec:
  selector:
    app: nginx-upstream
  ports:
    - port: 80
      targetPort: 80
EOF

wait_for_deployment nginx-upstream

info "updating http-route to point to nginx..."
kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: http-route
  namespace: default
spec:
  parentRefs:
    - name: edgegate
      sectionName: http
  rules:
    - backendRefs:
        - name: nginx-upstream
          port: 80
EOF

start_port_forward edgegate 8080:80

info "testing route update (should get nginx response)..."
if ! retry 30 1 assert_body_contains http://localhost:8080/ "nginx"; then
    stop_port_forward
    fail "route update: expected nginx response after backend switch"
    exit 1
fi

stop_port_forward

info "restoring original http-route..."
kubectl apply -f "$ROOT_DIR/k8s/samples/httproute.yaml"

info "cleaning up nginx upstream..."
kubectl delete deployment nginx-upstream
kubectl delete service nginx-upstream

pass "route update works"
