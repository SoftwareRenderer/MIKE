#!/usr/bin/env bash
# Build the Pi agent adapter as a WASI reactor module.
# Requires Go 1.24+ with wasip1 and c-shared support.
set -euo pipefail
cd "$(dirname "$0")/wasm"
echo "Building pi.wasm..."
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o ../pi.wasm .
echo "Built pi.wasm ($(ls -lh ../pi.wasm | awk '{print $5}'))"
