#!/bin/bash
set -euo pipefail

TARGETS_FILE="${1:-./targets-upstream.txt}"
RATE="${RATE:-10000}"
DURATION="${DURATION:-30s}"
WORKERS="${WORKERS:-500}"
CONNECTIONS="${CONNECTIONS:-5000}"
OUT_PREFIX="${OUT_PREFIX:-upstream-direct}"

if ! command -v vegeta >/dev/null 2>&1; then
  echo "vegeta is required. Install it first."
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required. Install it first."
  exit 1
fi

cleanup() {
  docker rm -f httpbin-direct >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Starting upstream container on :9100"
docker run --rm -d --name httpbin-direct -p 9100:8080 mccutchen/go-httpbin >/dev/null

echo "Waiting for upstream readiness"
until curl -sf "http://127.0.0.1:9100/get" >/dev/null 2>&1; do
  sleep 1
done

echo "Running upstream-direct load test"
echo "targets=$TARGETS_FILE rate=$RATE duration=$DURATION workers=$WORKERS connections=$CONNECTIONS"

vegeta attack \
  -targets="$TARGETS_FILE" \
  -rate="$RATE" \
  -duration="$DURATION" \
  -workers="$WORKERS" \
  -connections="$CONNECTIONS" \
  -keepalive=true \
  > "$OUT_PREFIX.bin"

echo ""
echo "Summary report"
vegeta report "$OUT_PREFIX.bin"

echo ""
echo "Latency histogram"
vegeta report -type='hist[0,1ms,2ms,5ms,10ms,20ms,50ms,100ms,200ms,500ms,1s]' "$OUT_PREFIX.bin"

echo ""
echo "Saved binary result: $OUT_PREFIX.bin"
