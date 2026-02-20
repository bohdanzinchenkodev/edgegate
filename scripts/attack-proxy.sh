#!/bin/bash
set -euo pipefail

PROXY_HOST="${PROXY_HOST:-127.0.0.1}"
PROXY_PORT="${PROXY_PORT:-8001}"
TARGET_HOST="${TARGET_HOST:-svc0.example.com}"
RATE="${RATE:-10000}"
DURATION="${DURATION:-30s}"
WORKERS="${WORKERS:-500}"
CONNECTIONS="${CONNECTIONS:-5000}"

BENCH_DIR="/tmp/edgegate-bench"
mkdir -p "$BENCH_DIR"
RESULT_FILE="$BENCH_DIR/proxy-via-edgegate.bin"
TARGETS_FILE=$(mktemp "$BENCH_DIR/targets-proxy.XXXXXX")

if ! command -v vegeta >/dev/null 2>&1; then
  echo "vegeta is required. Install it first."
  exit 1
fi

cleanup() {
  rm -f "$TARGETS_FILE"
}
trap cleanup EXIT

printf 'GET http://%s:%s/get\nHost: %s\n\n' "$PROXY_HOST" "$PROXY_PORT" "$TARGET_HOST" > "$TARGETS_FILE"

echo "Running proxy load test"
echo "  target : http://$PROXY_HOST:$PROXY_PORT/get"
echo "  host   : $TARGET_HOST"
echo "  rate   : $RATE  duration: $DURATION  workers: $WORKERS  connections: $CONNECTIONS"

vegeta attack \
  -targets="$TARGETS_FILE" \
  -rate="$RATE" \
  -duration="$DURATION" \
  -workers="$WORKERS" \
  -connections="$CONNECTIONS" \
  -keepalive=true \
  > "$RESULT_FILE"

echo ""
echo "Summary report"
vegeta report "$RESULT_FILE"

echo ""
echo "Latency histogram"
vegeta report -type='hist[0,1ms,2ms,5ms,10ms,20ms,50ms,100ms,200ms,500ms,1s]' "$RESULT_FILE"

echo ""
echo "Result saved: $RESULT_FILE"
