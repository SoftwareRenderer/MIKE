// Package main exports Pi adapter functions for wazero WASI reactor runtime.
// Built with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
//
// The -buildmode=c-shared flag produces a WASI reactor with _initialize
// instead of _start. The host calls _initialize first, then exported functions.
// main() is not called.
//
// > go:wasmexport functions use int32 for pointers/lengths (WASM is 32-bit).
// Data is passed via shared linear memory.
package main

import (
	"encoding/json"
	"unsafe"

	"kanban/extensions/agent/pi"
)

var adapter *pi.Adapter
var lastLen int32 // length of last data-returning result

// Memory allocation in WASM linear memory.
var memPool []byte
var memOffset int32

const poolSize = 16 * 1024 * 1024 // 16MB pool

//go:wasmexport malloc
func malloc(size int32) int32 {
	if memPool == nil {
		memPool = make([]byte, poolSize)
		memOffset = 0
	}
	ptr := memOffset
	memOffset += size + 7
	memOffset &= ^int32(7) // align to 8 bytes
	if memOffset > poolSize {
		return 0
	}
	return ptr
}

//go:wasmexport free
func free(ptr int32) {
	// No-op for bump allocator
}

//go:wasmexport lastResultLen
func lastResultLen() int32 {
	return lastLen
}

// Config: ptr to JSON bytes, len byte count. Returns 0=ok, 1=error.
//
//go:wasmexport Init
func Init(ptr int32, length int32) int32 {
	if adapter != nil {
		adapter.Close()
	}
	adapter = pi.NewAdapter()
	if ptr == 0 || length == 0 {
		return 1
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
	err := adapter.Init(data)
	if err != nil {
		return 1
	}
	return 0
}

// Returns pointer to JSON bytes in linear memory. Length via lastResultLen().
//
//go:wasmexport SubprocessConfig
func SubprocessConfig() int32 {
	if adapter == nil {
		lastLen = 0
		return 0
	}
	cfg := adapter.SubprocessConfig()
	data, _ := json.Marshal(cfg)
	if len(data) == 0 {
		lastLen = 0
		return 0
	}
	ptr := malloc(int32(len(data)))
	if ptr == 0 {
		lastLen = 0
		return 0
	}
	dest := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), len(data))
	copy(dest, data)
	lastLen = int32(len(data))
	return ptr
}

// Input: ptr to JSON {"cmd":"...","data":...}, len byte count.
// Returns pointer to raw protocol bytes. Length via lastResultLen().
//
//go:wasmexport EncodeCommand
func EncodeCommand(ptr int32, length int32) int32 {
	if adapter == nil {
		lastLen = 0
		return 0
	}
	var req struct {
		Cmd  string `json:"cmd"`
		Data []byte `json:"data"`
	}
	if ptr != 0 && length != 0 {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
		json.Unmarshal(raw, &req)
	}
	result := adapter.EncodeCommand(req.Cmd, req.Data)
	if len(result) == 0 {
		lastLen = 0
		return 0
	}
	outPtr := malloc(int32(len(result)))
	if outPtr == 0 {
		lastLen = 0
		return 0
	}
	dest := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(outPtr))), len(result))
	copy(dest, result)
	lastLen = int32(len(result))
	return outPtr
}

// Input: ptr to raw stdout bytes, len byte count.
// Returns pointer to JSON event array. Length via lastResultLen().
//
//go:wasmexport ParseOutput
func ParseOutput(ptr int32, length int32) int32 {
	if adapter == nil {
		result, _ := json.Marshal([]any{})
		outPtr := malloc(int32(len(result)))
		if outPtr == 0 {
			lastLen = 0
			return 0
		}
		copy(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(outPtr))), len(result)), result)
		lastLen = int32(len(result))
		return outPtr
	}
	var data []byte
	if ptr != 0 && length != 0 {
		data = unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
	}
	events := adapter.ParseOutput(data)
	result, _ := json.Marshal(events)
	outPtr := malloc(int32(len(result)))
	if outPtr == 0 {
		lastLen = 0
		return 0
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(outPtr))), len(result)), result)
	lastLen = int32(len(result))
	return outPtr
}

// Input: ptr to JSON {"id":"...","method":"..."}, len byte count.
// Returns pointer to response bytes. Length via lastResultLen().
//
//go:wasmexport HandleUIRequest
func HandleUIRequest(ptr int32, length int32) int32 {
	if adapter == nil {
		lastLen = 0
		return 0
	}
	var req struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
	if ptr != 0 && length != 0 {
		raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
		json.Unmarshal(raw, &req)
	}
	result := adapter.HandleUIRequest(req.ID, req.Method)
	if len(result) == 0 {
		lastLen = 0
		return 0
	}
	outPtr := malloc(int32(len(result)))
	if outPtr == 0 {
		lastLen = 0
		return 0
	}
	dest := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(outPtr))), len(result))
	copy(dest, result)
	lastLen = int32(len(result))
	return outPtr
}

//go:wasmexport Close
func Close() {
	if adapter != nil {
		adapter.Close()
		adapter = nil
	}
}

// main is not called in reactor mode (-buildmode=c-shared).
func main() {}
