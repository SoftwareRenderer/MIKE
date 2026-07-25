// Package main exports Pi adapter functions for wazero WASM runtime.
// This is a thin wrapper around the pi package that exports functions
// with the names expected by the agent runner.
package main

import (
	"encoding/json"
	"kanban/extensions/agent/pi"
)

var adapter *pi.Adapter

//export Init
func Init(configJSON []byte) bool {
	if adapter != nil {
		adapter.Close()
	}
	adapter = pi.NewAdapter()
	err := adapter.Init(configJSON)
	return err == nil
}

//export SubprocessConfig
func SubprocessConfig() []byte {
	if adapter == nil {
		return nil
	}
	cfg := adapter.SubprocessConfig()
	data, _ := json.Marshal(cfg)
	return data
}

//export EncodeCommand
func EncodeCommand(request []byte) []byte {
	if adapter == nil {
		return nil
	}
	var req struct {
		Command string          `json:"command"`
		Data    json.RawMessage `json:"data"`
	}
	if len(request) > 0 {
		json.Unmarshal(request, &req)
	}
	return adapter.EncodeCommand(req.Command, req.Data)
}

//export ParseOutput
func ParseOutput(data []byte) []byte {
	if adapter == nil {
		return []byte("[]")
	}
	events := adapter.ParseOutput(data)
	result, _ := json.Marshal(events)
	return result
}

//export HandleUIRequest
func HandleUIRequest(request []byte) []byte {
	if adapter == nil {
		return nil
	}
	var req struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
	if len(request) > 0 {
		json.Unmarshal(request, &req)
	}
	return adapter.HandleUIRequest(req.ID, req.Method)
}

//export Close
func Close() {
	if adapter != nil {
		adapter.Close()
		adapter = nil
	}
}

func main() {
	// WASM entry point — no-op. Functions above are exported to wazero.
}
