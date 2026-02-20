#!/bin/bash
set -euo pipefail

UPSTREAM_HOST="${UPSTREAM_HOST:-127.0.0.1}"
UPSTREAM_PORT="${UPSTREAM_PORT:-9100}"
RATE="${RATE:-10000}"
DURATION="${DURATION:-30s}"
WORKERS="${WORKERS:-500}"
CONNECTIONS="${CONNECTIONS:-5000}"

BENCH_DIR="/tmp/edgegate-bench"
mkdir -p "$BENCH_DIR"
RESULT_FILE="$BENCH_DIR/upstream-direct.bin"
TARGETS_FILE=$(mktemp "$BENCH_DIR/targets-upstream.XXXXXX")

if ! command -v vegeta >/dev/null 2>&1; then
  echo "vegeta is required. Install it first."
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required. Install it first."
  exit 1
fi

cleanup() {
  rm -f "$TARGETS_FILE"
  docker rm -f httpbin-direct >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Starting upstream container on :$UPSTREAM_PORT"
docker run --rm -d --name httpbin-direct -p "$UPSTREAM_PORT:8080" mccutchen/go-httpbin >/dev/null

echo "Waiting for upstream readiness"
RETRIES=0
until curl -sf "http://$UPSTREAM_HOST:$UPSTREAM_PORT/get" >/dev/null 2>&1; do
  RETRIES=$((RETRIES + 1))
  if [ "$RETRIES" -ge 30 ]; then
    echo "upstream failed to start after 30s"
    exit 1
  fi
  sleep 1
done

printf 'GET http://%s:%s/get\n\n' "$UPSTREAM_HOST" "$UPSTREAM_PORT" > "$TARGETS_FILE"

echo "Running upstream-direct load test"
echo "  target : http://$UPSTREAM_HOST:$UPSTREAM_PORT/get"
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
