#!/bin/bash
set -euo pipefail

BENCH_DIR_REL="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$BENCH_DIR_REL/.." && pwd)"

source "$BENCH_DIR_REL/bench.env"

VARIANTS_DIR="$BENCH_DIR_REL/configs"
VARIANT="${1:-base}"
SOURCE="$VARIANTS_DIR/${VARIANT}.yaml"
if [ ! -f "$SOURCE" ]; then
  echo "Config variant not found: $VARIANT"
  echo ""
  echo "Available variants:"
  for f in "$VARIANTS_DIR"/*.yaml; do
    echo "  $(basename "$f" .yaml)"
  done
  exit 1
fi

mkdir -p "$BENCH_DIR"
CONFIG_FILE="$BENCH_DIR/edgegate.yaml"
EDGEGATE_CONFIG="/etc/edgegate/edgegate.yaml"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required. Install it first."
  exit 1
fi

if ! docker image inspect "$EDGEGATE_IMAGE" >/dev/null 2>&1; then
  echo "Building edgegate docker image"
  docker build -t "$EDGEGATE_IMAGE" -f "$ROOT_DIR/docker/edgegate/Dockerfile" "$ROOT_DIR"
fi

docker network create "$DOCKER_NETWORK" 2>/dev/null || true

cleanup() {
  docker rm -f "$EDGEGATE_CONTAINER" >/dev/null 2>&1 || true
  docker rm -f httpbin-proxy >/dev/null 2>&1 || true
  docker network rm "$DOCKER_NETWORK" >/dev/null 2>&1 || true
  rm -f "$CONFIG_FILE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Starting upstream container on :$UPSTREAM_PORT"
docker run --rm -d --name httpbin-proxy --network "$DOCKER_NETWORK" -p "$UPSTREAM_PORT:8080" mccutchen/go-httpbin >/dev/null

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

export PROXY_PORT TARGET_HOST
envsubst < "$SOURCE" > "$CONFIG_FILE"

echo "Starting edgegate on :$PROXY_PORT (config: $VARIANT)"
docker run --rm -d \
  --name "$EDGEGATE_CONTAINER" \
  --network "$DOCKER_NETWORK" \
  -p "$PROXY_PORT:$PROXY_PORT" \
  -v "$BENCH_DIR:/etc/edgegate" \
  "$EDGEGATE_IMAGE" \
  -conf "$EDGEGATE_CONFIG" \
  > /dev/null

echo "Waiting for proxy readiness"
RETRIES=0
until curl -sf -H "Host: $TARGET_HOST" "http://127.0.0.1:$PROXY_PORT/get" >/dev/null 2>&1; do
  RETRIES=$((RETRIES + 1))
  if [ "$RETRIES" -ge 30 ]; then
    echo "proxy failed to start after 30s"
    echo "check logs: docker logs $EDGEGATE_CONTAINER"
    exit 1
  fi
  sleep 1
done

echo ""
echo "Stack ready:"
echo "  config variant  : $VARIANT"
echo "  upstream direct : http://127.0.0.1:$UPSTREAM_PORT/get"
echo "  via edgegate    : curl -H 'Host: $TARGET_HOST' http://127.0.0.1:$PROXY_PORT/get"
echo "  edgegate logs   : docker logs -f $EDGEGATE_CONTAINER"
echo "  active config   : $CONFIG_FILE"
echo ""
echo "Swap config mid-test: ./bench/swap-config.sh <variant>"
echo "Press Ctrl+C to stop and clean up."

while true; do
  sleep 3600
done
