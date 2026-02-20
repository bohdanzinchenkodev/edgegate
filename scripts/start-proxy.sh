#!/bin/bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BASE_PORT="${BASE_PORT:-9100}"
PROXY_PORT="${PROXY_PORT:-8001}"
TARGET_HOST="${TARGET_HOST:-svc0.example.com}"

BENCH_DIR="/tmp/edgegate-bench"
mkdir -p "$BENCH_DIR"
CONFIG_FILE="$BENCH_DIR/edgegate.yaml"
EDGEGATE_LOG="$BENCH_DIR/edgegate.log"
EDGEGATE_BIN="$BENCH_DIR/edgegate-bin"
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
  rm -f "$EDGEGATE_BIN" "$CONFIG_FILE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Starting upstream container on :$BASE_PORT"
docker run --rm -d --name httpbin-proxy -p "$BASE_PORT:8080" mccutchen/go-httpbin >/dev/null

echo "Waiting for upstream readiness"
RETRIES=0
until curl -sf "http://127.0.0.1:$BASE_PORT/get" >/dev/null 2>&1; do
  RETRIES=$((RETRIES + 1))
  if [ "$RETRIES" -ge 30 ]; then
    echo "upstream failed to start after 30s"
    exit 1
  fi
  sleep 1
done

cat > "$CONFIG_FILE" <<EOF
listeners:
  - listen: ":$PROXY_PORT"
    routes:
      - match:
          host: "$TARGET_HOST"
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
RETRIES=0
until curl -sf -H "Host: $TARGET_HOST" "http://127.0.0.1:$PROXY_PORT/get" >/dev/null 2>&1; do
  RETRIES=$((RETRIES + 1))
  if [ "$RETRIES" -ge 30 ]; then
    echo "proxy failed to start after 30s"
    echo "check log: $EDGEGATE_LOG"
    exit 1
  fi
  sleep 1
done

echo ""
echo "Stack ready:"
echo "  upstream direct : http://127.0.0.1:$BASE_PORT/get"
echo "  via edgegate    : curl -H 'Host: $TARGET_HOST' http://127.0.0.1:$PROXY_PORT/get"
echo "  edgegate log    : $EDGEGATE_LOG"
echo "  config          : $CONFIG_FILE"
echo ""
echo "Press Ctrl+C to stop and clean up."

while true; do
  sleep 3600
done
