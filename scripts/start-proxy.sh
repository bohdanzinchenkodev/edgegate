#!/bin/bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BASE_PORT="${BASE_PORT:-9100}"
PROXY_PORT="${PROXY_PORT:-8001}"
CONFIG_FILE="${CONFIG_FILE:-/tmp/edgegate-bench.yaml}"
EDGEGATE_LOG="${EDGEGATE_LOG:-/tmp/edgegate-bench.log}"
EDGEGATE_BIN="${EDGEGATE_BIN:-/tmp/edgegate-bench-bin}"
EDGEGATE_PID=""

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required. Install it first."
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "go is required. Install it first."
  exit 1
fi

cleanup() {
  if [ -n "$EDGEGATE_PID" ]; then
    kill "$EDGEGATE_PID" >/dev/null 2>&1 || true
    wait "$EDGEGATE_PID" >/dev/null 2>&1 || true
  fi
  local port_pid
  port_pid=$(lsof -t -nP -iTCP:"$PROXY_PORT" -sTCP:LISTEN 2>/dev/null || true)
  if [ -n "$port_pid" ]; then
    kill "$port_pid" >/dev/null 2>&1 || true
  fi
  docker rm -f httpbin-proxy >/dev/null 2>&1 || true
  rm -f "$EDGEGATE_BIN" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Starting upstream container on :$BASE_PORT"
docker run --rm -d --name httpbin-proxy -p "$BASE_PORT:8080" mccutchen/go-httpbin >/dev/null

echo "Waiting for upstream readiness"
until curl -sf "http://127.0.0.1:$BASE_PORT/get" >/dev/null 2>&1; do
  sleep 1
done

cat > "$CONFIG_FILE" <<EOF
listeners:
  - listen: ":$PROXY_PORT"
    routes:
      - match:
          path_prefix: "/svc0"
        upstream: "http://127.0.0.1:$BASE_PORT"
EOF

existing_pid=$(lsof -t -nP -iTCP:"$PROXY_PORT" -sTCP:LISTEN 2>/dev/null || true)
if [ -n "$existing_pid" ]; then
  echo "port :$PROXY_PORT already in use by pid $existing_pid"
  exit 1
fi

echo "Building edgegate"
(cd "$ROOT_DIR" && go build -o "$EDGEGATE_BIN" ./cmd/edgegate)

echo "Starting edgegate on :$PROXY_PORT"
"$EDGEGATE_BIN" -conf "$CONFIG_FILE" > "$EDGEGATE_LOG" 2>&1 &
EDGEGATE_PID=$!

echo "Waiting for proxy readiness"
until curl -sf "http://127.0.0.1:$PROXY_PORT/svc0/get" >/dev/null 2>&1; do
  sleep 1
done

echo ""
echo "Stack ready:"
echo "  upstream direct: http://127.0.0.1:$BASE_PORT/get"
echo "  via edgegate:    http://127.0.0.1:$PROXY_PORT/svc0/get"
echo "  edgegate log:    $EDGEGATE_LOG"
echo ""
echo "Press Ctrl+C to stop and clean up."

while true; do
  sleep 3600
done
