#!/bin/bash
set -euo pipefail

PROXY_HOST="${PROXY_HOST:-127.0.0.1}"
PROXY_PORT="${PROXY_PORT:-8001}"
TARGET_HOST="${TARGET_HOST:-svc0.example.com}"
RATE="${RATE:-10000}"
DURATION="${DURATION:-30s}"
WORKERS="${WORKERS:-500}"
CONNECTIONS="${CONNECTIONS:-5000}"
OUT_PREFIX="${OUT_PREFIX:-proxy-via-edgegate}"

if ! command -v vegeta >/dev/null 2>&1; then
  echo "vegeta is required. Install it first."
  exit 1
fi

TARGETS_FILE=$(mktemp)
trap "rm -f $TARGETS_FILE" EXIT

printf 'GET http://%s:%s/get\nHost: %s\n\n' "$PROXY_HOST" "$PROXY_PORT" "$TARGET_HOST" > "$TARGETS_FILE"

echo "Running proxy load test"
echo "target=http://$PROXY_HOST:$PROXY_PORT/get Host=$TARGET_HOST rate=$RATE duration=$DURATION workers=$WORKERS connections=$CONNECTIONS"

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
