#!/bin/bash
set -euo pipefail

BENCH_DIR_REL="$(cd "$(dirname "$0")" && pwd)"

source "$BENCH_DIR_REL/bench.env"

VARIANTS_DIR="$BENCH_DIR_REL/configs"
CONFIG_FILE="$BENCH_DIR/edgegate.yaml"

VARIANT="${1:-}"
if [ -z "$VARIANT" ]; then
  echo "Usage: $0 <variant>"
  echo ""
  echo "Available variants:"
  for f in "$VARIANTS_DIR"/*.yaml; do
    echo "  $(basename "$f" .yaml)"
  done
  exit 1
fi

SOURCE="$VARIANTS_DIR/${VARIANT}.yaml"
if [ ! -f "$SOURCE" ]; then
  echo "Variant not found: $VARIANT"
  echo ""
  echo "Available variants:"
  for f in "$VARIANTS_DIR"/*.yaml; do
    echo "  $(basename "$f" .yaml)"
  done
  exit 1
fi

export PROXY_PORT TARGET_HOST
envsubst < "$SOURCE" > "$CONFIG_FILE"
echo "Config swapped to '$VARIANT' ($CONFIG_FILE)"
