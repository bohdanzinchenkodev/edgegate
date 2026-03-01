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

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required. Install it first."
  exit 1
fi

VEGETA_IMAGE="edgegate-vegeta"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if ! docker image inspect "$VEGETA_IMAGE" >/dev/null 2>&1; then
  echo "Building vegeta docker image"
  docker build -t "$VEGETA_IMAGE" "$ROOT_DIR/docker/vegeta"
fi

vegeta() {
  docker run --rm --network=host -v "$BENCH_DIR:/data" "$VEGETA_IMAGE" "$@"
}

cleanup() {
  rm -f "$TARGETS_FILE"
}
trap cleanup EXIT

# vegeta runs inside a Docker container and needs host.docker.internal
# to reach edgegate running on the host machine (macOS Docker Desktop)
VEGETA_PROXY_HOST="host.docker.internal"
printf 'GET http://%s:%s/get\nHost: %s\n\n' "$VEGETA_PROXY_HOST" "$PROXY_PORT" "$TARGET_HOST" > "$TARGETS_FILE"

TARGETS_CONTAINER="/data/$(basename "$TARGETS_FILE")"
RESULT_CONTAINER="/data/proxy-via-edgegate.bin"

echo "Running proxy load test"
echo "  target : http://$PROXY_HOST:$PROXY_PORT/get"
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

PLOT_CONTAINER="/data/proxy-via-edgegate.html"
PLOT_FILE="$BENCH_DIR/proxy-via-edgegate.html"
vegeta plot "$RESULT_CONTAINER" > "$PLOT_FILE"

echo ""
echo "Result saved: $RESULT_FILE"
echo "Plot saved:   $PLOT_FILE"
open "$PLOT_FILE" 2>/dev/null || true
