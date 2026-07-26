// Package main exports Pi adapter functions for wazero WASI reactor runtime.
// Built with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
//
// Memory model: the HOST manages all memory allocation via wazero's api.Memory.
// - Input: host writes data to linear memory, passes (offset, length) to exported functions
// - Output: module writes results to a shared result buffer; host reads via lastResultPtr/lastResultLen
//
// go:wasmexport functions use int32 for pointers/lengths (WASM is 32-bit).
package main

import (
	"encoding/json"
	"unsafe"

	"kanban/extensions/agent/pi"
)

var adapter *pi.Adapter

// Shared result buffer — host reads results from here.
// 1MB should be enough for any single result.
var resultBuf [1 << 20]byte
var resultLen int32

//go:wasmexport lastResultPtr
// Returns the offset of the result buffer in linear memory.
func lastResultPtr() int32 {
	return int32(uintptr(unsafe.Pointer(&resultBuf[0])))
}

//go:wasmexport lastResultLen
// Returns the length of the last result written to resultBuf.
func lastResultLen() int32 {
	return resultLen
}

// setResult copies data into the shared result buffer.
// Caller must ensure len(data) <= len(resultBuf).
func setResult(data []byte) {
	if len(data) > len(resultBuf) {
		// Truncate — shouldn't happen for normal results
		data = data[:len(resultBuf)]
	}
	copy(resultBuf[:], data)
	resultLen = int32(len(data))
}

//go:wasmexport Init
// Config: ptr to JSON bytes in linear memory, len byte count. Returns 0=ok, 1=error.
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

//go:wasmexport SubprocessConfig
// Writes JSON to result buffer. Length via lastResultLen().
func SubprocessConfig() {
	if adapter == nil {
		resultLen = 0
		return
	}
	cfg := adapter.SubprocessConfig()
	data, _ := json.Marshal(cfg)
	setResult(data)
}

//go:wasmexport EncodeCommand
// Input: ptr to JSON {"cmd":"...","data":...} in linear memory, len byte count.
// Writes raw protocol bytes to result buffer. Length via lastResultLen().
func EncodeCommand(ptr int32, length int32) {
	if adapter == nil {
		resultLen = 0
		return
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
	setResult(result)
}

//go:wasmexport ParseOutput
// Input: ptr to raw stdout bytes in linear memory, len byte count.
// Writes JSON event array to result buffer. Length via lastResultLen().
func ParseOutput(ptr int32, length int32) {
	var data []byte
	if ptr != 0 && length != 0 {
		data = unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
	}
	var events []pi.Event
	if adapter != nil {
		events = adapter.ParseOutput(data)
	}
	result, _ := json.Marshal(events)
	setResult(result)
}

//go:wasmexport HandleUIRequest
// Input: ptr to JSON {"id":"...","method":"..."} in linear memory, len byte count.
// Writes response bytes to result buffer. Length via lastResultLen().
func HandleUIRequest(ptr int32, length int32) {
	if adapter == nil {
		resultLen = 0
		return
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
	setResult(result)
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
