#!/usr/bin/env bash
# Build the Pi agent adapter as a WASI reactor module.
#
# The WASM module uses the wazero allocation/tinygo/greet.go memory model:
#   - Host calls Alloc(size) → writes data → calls function with string params → Free(ptr)
#   - WASM calls store_result(data string) callback for WASM→host results
#   - Go maps `string` params to (i32 ptr, i32 len) automatically
#
# Requires Go 1.24+ with wasip1 and c-shared support.
set -euo pipefail

cd "$(dirname "$0")/wasm"

# Verify Go version (wasip1 needs Go 1.21+; c-shared WASI needs 1.24+)
if ! command -v go &>/dev/null; then
    echo "Error: Go is not installed" >&2
    exit 1
fi

echo "Building pi.wasm..."
GOOS=wasip1 GOARCH=wasm go build -trimpath -buildmode=c-shared -o ../pi.wasm .

if [ ! -f ../pi.wasm ]; then
    echo "Error: pi.wasm was not built" >&2
    exit 1
fi

echo "Built pi.wasm ($(ls -lh ../pi.wasm | awk '{print $5}'))"
