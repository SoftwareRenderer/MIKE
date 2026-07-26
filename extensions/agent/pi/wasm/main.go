//go:build wasip1

// Package main exports Pi adapter functions for wazero WASI reactor runtime.
// Built with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
//
// Memory model: Zero-buffer string passing via Alloc/Free (per wazero
// allocation/tinygo/greet.go pattern — Alloc → Write → Call → Free).
//
//	Host → WASM (input):  The host calls the module's Alloc(size) to obtain a
//	  WASM linear memory offset, writes data there via api.Memory.Write, then
//	  passes (offset, size) as a `string` parameter to the exported function.
//	  The Go compiler maps `string` → (i32 ptr, i32 len) automatically.
//
//	WASM → host (output): The module calls the host-imported
//	  store_result(data string) callback, which reads the string from
//	  linear memory and delivers it to the host. Safe because the
//	  callback fires synchronously during fn.Call — before it returns.
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

// allocBuffer is a pre-allocated byte slice in Go's heap (which lives in
// WASM linear memory for the wasip1 target). The host writes input data
// to offsets within this buffer via api.Memory.Write, then passes those
// offsets to exported WASM functions as string parameters.
//
// The buffer is never GC-collected (it's a package-level global), and Go's
// GC is non-moving, so offsets returned by Alloc remain valid for the
// lifetime of the module.
var allocBuffer = make([]byte, 4*1024*1024) // 4 MiB — ample for all input sizes

// allocOffset is the current write offset within allocBuffer (bump allocator).
var allocOffset int32

// Alloc allocates `size` bytes from allocBuffer and returns the WASM
// linear memory offset where the host should write data.
//
// The host writes input data to this offset using api.Memory.Write, then
// passes (offset, size) as a `string` parameter to the exported WASM
// function. The Go compiler maps the `string` parameter to (i32 ptr, i32 len),
// which references the same linear memory the host wrote to.
//
// Returns 0 if size is invalid or the buffer cannot satisfy the request
// (the buffer resets on overflow — safe because allocations are short-lived,
// valid only during a single fn.Call).
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

// Free is a no-op for the bump allocator. Allocations remain valid until
// the buffer resets (on the next Alloc that would overflow). Since every
// allocation is consumed during the immediately following fn.Call (before
// the next Alloc), stale data is never read.
//
//go:wasmexport Free
func Free(_ int32) {}

// ---------------------------------------------------------------------------
// Exported functions — each takes `string` parameters (host writes via Alloc)
// and returns results via the store_result host callback.
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

// cmd is the command name (e.g. "prompt", "compact").
// data is the command-specific JSON payload (may be empty string).
// Emits encoded protocol bytes via store_result.
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

// data is raw agent stdout bytes (passed as a string).
// Emits a JSON array of parsed events via store_result.
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

// id is the UI request identifier.
// method is the extension method to handle.
// Emits the UI response bytes via store_result.
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
