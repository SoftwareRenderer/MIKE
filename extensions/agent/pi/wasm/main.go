//go:build wasip1

// Package main exports Pi adapter functions for the wazero WASI reactor runtime.
// Built with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
//
// Memory model (per wazero allocation/tinygo/greet.go pattern):
//
//	Host → WASM: the host calls Alloc(size), writes data, then passes
//	(offset, length) as a `string` param to the exported function.
//	Go maps `string` → (i32 ptr, i32 len). Pattern: Alloc → Write → Call → Free
//
//	WASM → Host: the module calls store_result(data string), which the
//	host reads via m.Memory().Read(ptr, length). Safe because the
//	callback fires synchronously during fn.Call — before it returns.
package main

import (
	"context"
	"encoding/json"
	"unsafe"

	"kanban/extensions/agent/pi"
	"kanban/pkg/agent"
)

// adapter holds the Pi protocol adapter instance. Created in Init, used by
// all subsequent exported functions.
var adapter *pi.Adapter

//go:wasmimport env store_result
//go:noescape
func storeResult(data string)

// 1 MiB fixed scratchpad buffer. The host writes input data here via
// Alloc; valid only for the duration of one fn.Call.
var memBuffer = make([]byte, 1<<20)
var memOffset int32

// Alloc reserves a block of linear memory for the host. Uses an 8-byte
// aligned bump allocator. Wraps to 0 on overflow (safe because allocations
// are short-lived — only used during a single fn.Call).
//
//go:wasmexport Alloc
func Alloc(size int32) int32 {
	if size <= 0 || size > int32(len(memBuffer)) {
		return 0 // NULL pointer
	}

	alignedSize := (size + 7) &^ 7

	if memOffset+alignedSize > int32(len(memBuffer)) {
		memOffset = 0
	}

	base := int32(uintptr(unsafe.Pointer(&memBuffer[0])))
	ptr := base + memOffset
	memOffset += alignedSize

	return ptr
}

// Free is a no-op; memory is reclaimed on the next Alloc wrap-around.
//
//go:wasmexport Free
func Free(_ int32) {}

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
// the store_result callback.
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

// EncodeCommand encodes a high-level command into Pi JSONL bytes.
// cmd is one of: prompt, compact, stats, get_state, abort.
// data is the command-specific JSON payload. Result via store_result.
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

// ParseOutput parses raw Pi JSONL stdout bytes into agent events.
// Maintains state across calls (line buffering, turn state).
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

// HandleUIRequest generates a response for an agent UI request.
// id is the UI request ID; method is the extension method name.
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
