#!/bin/bash
set -euo pipefail

BENCH_DIR_REL="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$BENCH_DIR_REL/.." && pwd)"

source "$BENCH_DIR_REL/bench.env"

mkdir -p "$BENCH_DIR"
RESULT_FILE="$BENCH_DIR/proxy-via-edgegate.bin"
TARGETS_FILE="$BENCH_DIR/targets-proxy.txt"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required. Install it first."
  exit 1
fi

if ! docker image inspect "$VEGETA_IMAGE" >/dev/null 2>&1; then
  echo "Building vegeta docker image"
  docker build -t "$VEGETA_IMAGE" "$ROOT_DIR/docker/vegeta"
fi

vegeta() {
  docker run --rm --network "$DOCKER_NETWORK" -v "$BENCH_DIR:/data" "$VEGETA_IMAGE" "$@"
}

cleanup() {
  rm -f "$TARGETS_FILE"
}
trap cleanup EXIT

printf 'GET http://%s:%s/get\nHost: %s\n\n' "$EDGEGATE_CONTAINER" "$PROXY_PORT" "$TARGET_HOST" > "$TARGETS_FILE"

TARGETS_CONTAINER="/data/targets-proxy.txt"
RESULT_CONTAINER="/data/proxy-via-edgegate.bin"

echo "Running proxy load test"
echo "  target : http://$EDGEGATE_CONTAINER:$PROXY_PORT/get"
echo "  host   : $TARGET_HOST"
echo "  rate   : $RATE  duration: $DURATION  workers: $WORKERS  connections: $CONNECTIONS"

vegeta attack \
  -targets="$TARGETS_CONTAINER" \
  -rate="$RATE" \
  -duration="$DURATION" \
  -workers="$WORKERS" \
  -connections="$CONNECTIONS" \
  -keepalive=true \
  -output="$RESULT_CONTAINER"

echo ""
echo "Summary report"
vegeta report "$RESULT_CONTAINER"

echo ""
echo "Latency histogram"
vegeta report -type='hist[0,1ms,2ms,5ms,10ms,20ms,50ms,100ms,200ms,500ms,1s]' "$RESULT_CONTAINER"

PLOT_FILE="$BENCH_DIR/proxy-via-edgegate.html"
vegeta plot "$RESULT_CONTAINER" > "$PLOT_FILE"

echo ""
echo "Result saved: $RESULT_FILE"
echo "Plot saved:   $PLOT_FILE"
open "$PLOT_FILE" 2>/dev/null || true
