//go:build wasip1

// Package main exports Pi adapter functions for wazero WASI reactor runtime.
// Built with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
//
// Memory model:
//   Host → WASM: Host calls Alloc(size) -> writes data -> calls exported function.
//     Go maps `string` parameters to (i32 ptr, i32 len) automatically.
//   WASM → Host: Module calls store_result(string) callback synchronously.
package main

import (
	"encoding/json"
	"unsafe"

	"kanban/extensions/agent/pi"
)

var adapter *pi.Adapter

// ---------------------------------------------------------------------------
// Host callback: WASM → host result passing.
// ---------------------------------------------------------------------------

//go:wasmimport env store_result
//go:noescape
func storeResult(data string)

// ---------------------------------------------------------------------------
// Host → WASM input passing: bump allocator in a pre-allocated buffer.
// ---------------------------------------------------------------------------

// allocBuffer is a 1 MiB pre-allocated buffer to avoid immediate memory growth
// crashes during startup (initial linear memory is ~2.7 MiB). 
//
// The host writes input data here via Alloc(), then passes the offset to 
// exported functions. Buffer resets on overflow; safe as allocations are 
// short-lived (valid only for one fn.Call).
var allocBuffer = make([]byte, 1*1024*1024) // 1 MiB

// allocOffset is the current write offset within allocBuffer (bump allocator).
var allocOffset int32

// Alloc allocates `size` bytes from allocBuffer and returns the linear memory offset.
// Returns 0 if size is invalid or exceeds buffer capacity.
//
//go:wasmexport Alloc
func Alloc(size int32) int32 {
	if size <= 0 || size > int32(len(allocBuffer)) {
		return 0
	}
	if allocOffset+size > int32(len(allocBuffer)) {
		allocOffset = 0 // reset: all prior allocations are stale (post-call)
	}
	base := int32(uintptr(unsafe.Pointer(&allocBuffer[0])))
	ptr := base + allocOffset
	allocOffset += size
	return ptr
}

// Free is a no-op for the bump allocator.
//
//go:wasmexport Free
func Free(_ int32) {}

// ---------------------------------------------------------------------------
// Exported functions — use `string` parameters and return via store_result.
// ---------------------------------------------------------------------------

//go:wasmexport Init
func Init(config string) int32 {
	if adapter != nil {
		adapter.Close()
	}
	adapter = pi.NewAdapter()
	if len(config) == 0 {
		return 1
	}
	if err := adapter.Init([]byte(config)); err != nil {
		return 1
	}
	return 0
}

//go:wasmexport SubprocessConfig
func SubprocessConfig() {
	if adapter == nil {
		storeResult("")
		return
	}
	cfg := adapter.SubprocessConfig()
	data, _ := json.Marshal(cfg)
	storeResult(string(data))
}

// cmd is the command name; data is the JSON payload.
//
//go:wasmexport EncodeCommand
func EncodeCommand(cmd string, data string) {
	if adapter == nil {
		storeResult("")
		return
	}
	result := adapter.EncodeCommand(cmd, []byte(data))
	storeResult(string(result))
}

// data is raw agent stdout bytes. Emits parsed events as JSON array.
//
//go:wasmexport ParseOutput
func ParseOutput(data string) {
	if adapter == nil {
		storeResult("[]")
		return
	}
	events := adapter.ParseOutput([]byte(data))
	if events == nil {
		events = []pi.Event{}
	}
	result, _ := json.Marshal(events)
	storeResult(string(result))
}

// id is the UI request ID; method is the extension method.
//
//go:wasmexport HandleUIRequest
func HandleUIRequest(id string, method string) {
	if adapter == nil {
		storeResult("")
		return
	}
	result := adapter.HandleUIRequest(id, method)
	storeResult(string(result))
}

//go:wasmexport Close
func Close() {
	if adapter != nil {
		adapter.Close()
		adapter = nil
	}
}

// main is not called in reactor mode (-buildmode=c-shared produces _initialize).
func main() {}
