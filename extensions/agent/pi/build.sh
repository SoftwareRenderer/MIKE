#!/usr/bin/env bash
# Build the Pi agent adapter as a WASM module.
# Requires Go 1.26+ with wasip1 support.
set -euo pipefail
cd "$(dirname "$0")/wasm"
echo "Building pi.wasm..."
GOOS=wasip1 GOARCH=wasm go build -o ../pi.wasm .
echo "Built pi.wasm ($(ls -lh ../pi.wasm | awk '{print $5}'))"
