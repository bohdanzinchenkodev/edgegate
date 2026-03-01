#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BENCH_CONFIG="${BENCH_CONFIG:-/tmp/edgegate-bench/edgegate.yaml}"
VARIANTS_DIR="$ROOT_DIR/configs/bench"

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
  echo "Variant not found: $SOURCE"
  echo ""
  echo "Available variants:"
  for f in "$VARIANTS_DIR"/*.yaml; do
    echo "  $(basename "$f" .yaml)"
  done
  exit 1
fi

cp "$SOURCE" "$BENCH_CONFIG"
echo "Config swapped to '$VARIANT' ($BENCH_CONFIG)"
