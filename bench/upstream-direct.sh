#!/bin/bash
set -euo pipefail

BENCH_DIR_REL="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$BENCH_DIR_REL/.." && pwd)"

source "$BENCH_DIR_REL/bench.env"

# standalone network — this script runs independently of start-proxy.sh
DOCKER_NETWORK="edgegate-bench-direct"

mkdir -p "$BENCH_DIR"
RESULT_FILE="$BENCH_DIR/upstream-direct.bin"
TARGETS_FILE="$BENCH_DIR/targets-upstream.txt"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required. Install it first."
  exit 1
fi

if ! docker image inspect "$VEGETA_IMAGE" >/dev/null 2>&1; then
  echo "Building vegeta docker image"
  docker build -t "$VEGETA_IMAGE" "$ROOT_DIR/docker/vegeta"
fi

docker network create "$DOCKER_NETWORK" 2>/dev/null || true

vegeta() {
  docker run --rm --network "$DOCKER_NETWORK" -v "$BENCH_DIR:/data" "$VEGETA_IMAGE" "$@"
}

cleanup() {
  rm -f "$TARGETS_FILE"
  docker rm -f httpbin-direct >/dev/null 2>&1 || true
  docker network rm "$DOCKER_NETWORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Starting upstream container on :$UPSTREAM_PORT"
docker run --rm -d --name httpbin-direct --network "$DOCKER_NETWORK" -p "$UPSTREAM_PORT:8080" mccutchen/go-httpbin >/dev/null

echo "Waiting for upstream readiness"
RETRIES=0
until curl -sf "http://127.0.0.1:$UPSTREAM_PORT/get" >/dev/null 2>&1; do
  RETRIES=$((RETRIES + 1))
  if [ "$RETRIES" -ge 30 ]; then
    echo "upstream failed to start after 30s"
    exit 1
  fi
  sleep 1
done

printf 'GET http://httpbin-direct:8080/get\n\n' > "$TARGETS_FILE"

echo "Running upstream-direct load test"
echo "  target : http://httpbin-direct:8080/get"
echo "  rate   : $RATE  duration: $DURATION  workers: $WORKERS  connections: $CONNECTIONS"

TARGETS_CONTAINER="/data/targets-upstream.txt"
RESULT_CONTAINER="/data/upstream-direct.bin"

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

PLOT_FILE="$BENCH_DIR/upstream-direct.html"
vegeta plot "$RESULT_CONTAINER" > "$PLOT_FILE"

echo ""
echo "Result saved: $RESULT_FILE"
echo "Plot saved:   $PLOT_FILE"
open "$PLOT_FILE" 2>/dev/null || true
