//go:build wasip1

// Package main exports Pi adapter functions for the wazero WASI reactor runtime.
// Built with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
//
// Memory model (per wazero allocation/tinygo/greet.go pattern):
//
//	Host → WASM (input): The host calls Alloc(size) to obtain a linear
//	  memory offset, writes data there via api.Memory.Write, then passes
//	  (offset, length) as a `string` parameter to the exported function.
//	  Go maps `string` → (i32 ptr, i32 len) automatically.
//	  Pattern: Alloc → Write → Call → Free
//
//	WASM → Host (output): The module calls the host-imported
//	  store_result(data string) callback. Go maps the `string` param to
//	  (i32 ptr, i32 len) into linear memory; the host reads via
//	  m.Memory().Read(ptr, length). Safe because the callback fires
//	  synchronously during fn.Call — before it returns.
package main

import (
	"context"
	"encoding/json"
	"unsafe"

	"kanban/extensions/agent/pi"
	"kanban/pkg/agent"
)

// adapter holds the Pi protocol adapter instance. It is created in Init
// and used by all subsequent exported functions.
var adapter *pi.Adapter

// ---------------------------------------------------------------------------
// Host callback: WASM → host result passing.
//
// The host imports this function from the "env" module. When the WASM module
// calls store_result(data string), Go passes (i32 ptr, i32 len) pointing
// into linear memory. The host reads the data and delivers it via a channel.
// ---------------------------------------------------------------------------

//go:wasmimport env store_result
//go:noescape
func storeResult(data string)

// ---------------------------------------------------------------------------
// Host → WASM input passing: bump allocator in a fixed-size linear memory region.
//
// In Go's WASI runtime, Go variables reside in the module's linear memory.
// &memBuffer[0] is a valid linear memory offset that the host can write to
// via api.Memory.Write(offset, data).
//
// The host calls Alloc(size) → writes data → calls exported function with
// (ptr, len) as string params → Free(ptr). Allocations are short-lived
// (valid only for the duration of one fn.Call), so the bump allocator
// resets on overflow.
// ---------------------------------------------------------------------------

// 1 MiB fixed scratchpad buffer allocated on the Go heap.
var memBuffer = make([]byte, 1<<20)
var memOffset int32

// Alloc reserves a block of linear memory for the Wasm host.
// It uses an 8-byte aligned bump-allocator strategy.
//
// CONTRACT: This allocator uses a ring-buffer reset. When the buffer
// fills up, memOffset resets to 0. The host must ensure that previous
// allocations are no longer in use across wrap-around boundaries.
//
//go:wasmexport Alloc
func Alloc(size int32) int32 {
	if size <= 0 || size > int32(len(memBuffer)) {
		return 0 // Address 0 acts as a NULL pointer in Wasm linear memory
	}

	// Align the requested size to an 8-byte boundary:
	// e.g., 3 bytes becomes 8, 12 bytes becomes 16.
	alignedSize := (size + 7) &^ 7

	// If the allocation exceeds the remaining capacity, reset to the start.
	if memOffset+alignedSize > int32(len(memBuffer)) {
		memOffset = 0
	}

	base := int32(uintptr(unsafe.Pointer(&memBuffer[0])))
	ptr := base + memOffset
	memOffset += alignedSize

	return ptr
}

// Free is a no-op for the bump allocator. Memory is reclaimed when the
// bump allocator resets on the next Alloc call that would overflow the
// buffer. This is safe because all allocations are short-lived — valid
// only for the duration of a single fn.Call.
//
//go:wasmexport Free
func Free(_ int32) {}

// ---------------------------------------------------------------------------
// Exported functions.
//
// Each function uses `string` parameters (auto-mapped to i32 ptr + i32 len
// by the Go compiler) for host→WASM input, and calls store_result for
// WASM→host output. This matches the wazero allocation/tinygo/greet.go
// pattern.
// ---------------------------------------------------------------------------

// Init initializes the adapter with extension config JSON.
// Returns 0 on success, 1 on error.
//
//go:wasmexport Init
func Init(config string) int32 {
	if adapter != nil {
		adapter.Close()
	}
	adapter = pi.NewAdapter()
	if len(config) == 0 {
		return 1
	}
	if err := adapter.Init(context.Background(), []byte(config)); err != nil {
		return 1
	}
	return 0
}

// SubprocessConfig returns the agent process configuration as JSON via
// the store_result callback. Takes no parameters.
//
//go:wasmexport SubprocessConfig
func SubprocessConfig() {
	if adapter == nil {
		storeResult("")
		return
	}
	cfg := adapter.SubprocessConfig(context.Background())
	data, _ := json.Marshal(cfg)
	storeResult(string(data))
}

// EncodeCommand encodes a high-level command into Pi JSONL protocol bytes.
// cmd is the command name (e.g. "prompt", "compact", "stats", "get_state", "abort").
// data is the command-specific JSON payload. Result is delivered via store_result.
//
//go:wasmexport EncodeCommand
func EncodeCommand(cmd string, data string) {
	if adapter == nil {
		storeResult("")
		return
	}
	result := adapter.EncodeCommand(context.Background(), cmd, []byte(data))
	storeResult(string(result))
}

// ParseOutput parses raw Pi JSONL stdout bytes into generic agent events.
// The adapter maintains state across calls (e.g. line buffering, turn state).
// Result is a JSON array of events, delivered via store_result.
//
//go:wasmexport ParseOutput
func ParseOutput(data string) {
	if adapter == nil {
		storeResult("[]")
		return
	}
	events := adapter.ParseOutput(context.Background(), []byte(data))
	if events == nil {
		events = []agent.Event{}
	}
	result, _ := json.Marshal(events)
	storeResult(string(result))
}

// HandleUIRequest generates a response for an agent UI request (e.g. cancel a dialog).
// id is the UI request ID; method is the extension method name.
// Result is delivered via store_result.
//
//go:wasmexport HandleUIRequest
func HandleUIRequest(id string, method string) {
	if adapter == nil {
		storeResult("")
		return
	}
	result := adapter.HandleUIRequest(context.Background(), id, method)
	storeResult(string(result))
}

// Close cleans up adapter state.
//
//go:wasmexport Close
func Close() {
	if adapter != nil {
		adapter.Close()
		adapter = nil
	}
}

// main is not called in reactor mode (-buildmode=c-shared produces _initialize).
func main() {}
