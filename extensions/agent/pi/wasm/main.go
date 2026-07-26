// Package main exports Pi adapter functions for wazero WASI reactor runtime.
// Built with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared
//
// Memory model: NO buffers. Pure string passing.
// - Input: host writes data to linear memory, passes as `string` param
//   (Go runtime handles (i32 ptr, i32 len))
// - Output: via //go:wasmimport host callback store_result(string)
//   No shared buffer.
package main

import (
	"encoding/json"

	"kanban/extensions/agent/pi"
)

var adapter *pi.Adapter

//go:wasmimport env store_result
//go:noescape
// Host callback: receives a string (i32 ptr + i32 len) from the WASM module.
// The host reads the string data from linear memory and stores it.
func storeResult(data string)

//go:wasmexport Init
// config is a JSON string passed from the host. Returns 0 on success, 1 on error.
func Init(config string) int32 {
	if adapter != nil {
		adapter.Close()
	}
	adapter = pi.NewAdapter()
	if len(config) == 0 {
		return 1
	}
	err := adapter.Init([]byte(config))
	if err != nil {
		return 1
	}
	return 0
}

//go:wasmexport SubprocessConfig
// Emits JSON of the subprocess config via the store_result callback.
func SubprocessConfig() {
	if adapter == nil {
		storeResult("")
		return
	}
	cfg := adapter.SubprocessConfig()
	data, _ := json.Marshal(cfg)
	storeResult(string(data))
}

//go:wasmexport EncodeCommand
// req is a JSON string {"cmd":"...","data":...}.
// Emits encoded protocol bytes via the store_result callback.
func EncodeCommand(req string) {
	if adapter == nil {
		storeResult("")
		return
	}
	var r struct {
		Cmd  string `json:"cmd"`
		Data []byte `json:"data"`
	}
	if len(req) > 0 {
		json.Unmarshal([]byte(req), &r)
	}
	result := adapter.EncodeCommand(r.Cmd, r.Data)
	storeResult(string(result))
}

//go:wasmexport ParseOutput
// data is raw stdout bytes passed as a string.
// Emits a JSON array of parsed events via the store_result callback.
func ParseOutput(data string) {
	var events []pi.Event
	if adapter != nil {
		events = adapter.ParseOutput([]byte(data))
	}
	result, _ := json.Marshal(events)
	storeResult(string(result))
}

//go:wasmexport HandleUIRequest
// req is a JSON string {"id":"...","method":"..."}.
// Emits the UI response bytes via the store_result callback.
func HandleUIRequest(req string) {
	if adapter == nil {
		storeResult("")
		return
	}
	var r struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
	if len(req) > 0 {
		json.Unmarshal([]byte(req), &r)
	}
	result := adapter.HandleUIRequest(r.ID, r.Method)
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
