package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"kanban/backend/extensions"
)

// adapterRegistry holds registered adapter factories.
// Used by tests to inject mock adapters. Production uses WASM loading.
var adapterRegistry = struct {
	sync.RWMutex
	factories map[string]AdapterFactory
}{factories: make(map[string]AdapterFactory)}

// AdapterFactory creates a new AgentAdapter instance.
type AdapterFactory func() AgentAdapter

// RegisterAdapterFactory registers an adapter factory for an extension name.
// Used by tests to inject mock adapters.
func RegisterAdapterFactory(name string, fn AdapterFactory) {
	adapterRegistry.Lock()
	defer adapterRegistry.Unlock()
	adapterRegistry.factories[name] = fn
}

// wasmAdapter wraps a wazero-loaded WASI reactor module behind the AgentAdapter interface.
// Uses shared linear memory and exported functions for communication.
type wasmAdapter struct {
	runtime   wazero.Runtime
	module    api.Module
	memory    api.Memory
	mallocFn  api.Function
	lastLenFn api.Function
}

// loadAdapter loads an AgentAdapter for the given extension.
// It first checks for a registered factory (test mocks), then falls back to WASM.
func loadAdapter(ext *extensions.Extension) (AgentAdapter, error) {
	adapterRegistry.RLock()
	factory, ok := adapterRegistry.factories[ext.Name]
	adapterRegistry.RUnlock()
	if ok {
		return factory(), nil
	}

	return loadWasmAdapter(ext)
}

// loadWasmAdapter loads and instantiates a WASI reactor module.
// The module is built with -buildmode=c-shared which produces _initialize
// instead of _start. The host calls _initialize to set up the Go runtime,
// then calls exported functions.
func loadWasmAdapter(ext *extensions.Extension) (AgentAdapter, error) {
	if ext.Manifest.Agent == nil || ext.Manifest.Agent.WasmModule == "" {
		return nil, fmt.Errorf("extension %q has no wasm_module configured", ext.Name)
	}

	wasmPath := resolveWasmPath(ext)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read wasm module %s: %w", wasmPath, err)
	}

	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)

	// Instantiate WASI for basic system calls (time, etc.)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("inject wasi: %w", err)
	}

	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("compile wasm module: %w", err)
	}

	// Use _initialize to init Go runtime (reactor mode, -buildmode=c-shared).
	config := wazero.NewModuleConfig().
		WithStartFunctions("_initialize").
		WithStderr(os.Stderr)

	inst, err := runtime.InstantiateModule(ctx, compiled, config)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("instantiate wasm module: %w", err)
	}

	mem := inst.Memory()
	if mem == nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("wasm module has no exported memory")
	}

	mallocFn := inst.ExportedFunction("malloc")
	if mallocFn == nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("malloc not exported")
	}

	return &wasmAdapter{
		runtime:   runtime,
		module:    inst,
		memory:    mem,
		mallocFn:  mallocFn,
		lastLenFn: inst.ExportedFunction("lastResultLen"),
	}, nil
}

// resolveWasmPath finds the WASM module file for an extension.
func resolveWasmPath(ext *extensions.Extension) string {
	wasmModule := ext.Manifest.Agent.WasmModule

	candidates := []string{
		filepath.Join("extensions", "agent", ext.Name, wasmModule),
		filepath.Join(ext.Name, wasmModule),
		wasmModule,
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return candidates[len(candidates)-1]
}

// writeData allocates memory in WASM and writes data to it. Returns the pointer.
func (a *wasmAdapter) writeData(data []byte) (int32, error) {
	ctx := context.Background()
	result, err := a.mallocFn.Call(ctx, api.EncodeI32(int32(len(data))))
	if err != nil {
		return 0, fmt.Errorf("malloc: %w", err)
	}
	ptr := api.DecodeI32(result[0])
	if ptr == 0 {
		return 0, fmt.Errorf("malloc returned null")
	}
	if !a.memory.Write(uint32(ptr), data) {
		return 0, fmt.Errorf("memory write failed at offset %d", ptr)
	}
	return ptr, nil
}

// callFn calls an exported WASM function with a byte slice argument.
// Returns the result pointer (data written to WASM memory).
func (a *wasmAdapter) callFn(name string, data []byte) (int32, error) {
	fn := a.module.ExportedFunction(name)
	if fn == nil {
		return 0, fmt.Errorf("function %q not exported", name)
	}

	ctx := context.Background()

	if len(data) == 0 {
		result, err := fn.Call(ctx, api.EncodeI32(0), api.EncodeI32(0))
		if err != nil {
			return 0, fmt.Errorf("call %s: %w", name, err)
		}
		return api.DecodeI32(result[0]), nil
	}

	ptr, err := a.writeData(data)
	if err != nil {
		return 0, err
	}

	result, err := fn.Call(ctx, api.EncodeI32(ptr), api.EncodeI32(int32(len(data))))
	if err != nil {
		return 0, fmt.Errorf("call %s: %w", name, err)
	}

	return api.DecodeI32(result[0]), nil
}

// callFnNoArgs calls an exported WASM function with no arguments.
func (a *wasmAdapter) callFnNoArgs(name string) (int32, error) {
	fn := a.module.ExportedFunction(name)
	if fn == nil {
		return 0, fmt.Errorf("function %q not exported", name)
	}

	ctx := context.Background()
	result, err := fn.Call(ctx)
	if err != nil {
		return 0, fmt.Errorf("call %s: %w", name, err)
	}

	return api.DecodeI32(result[0]), nil
}

// readResult reads data from WASM memory using the result pointer and lastResultLen.
func (a *wasmAdapter) readResult(ptr int32) []byte {
	if ptr == 0 {
		return nil
	}
	ctx := context.Background()
	lenResult, err := a.lastLenFn.Call(ctx)
	if err != nil {
		return nil
	}
	length := api.DecodeI32(lenResult[0])
	if length == 0 {
		return nil
	}
	data, ok := a.memory.Read(uint32(ptr), uint32(length))
	if !ok {
		return nil
	}
	result := make([]byte, length)
	copy(result, data)
	return result
}

// Init initializes the adapter with config JSON.
func (a *wasmAdapter) Init(config []byte) error {
	result, err := a.callFn("Init", config)
	if err != nil {
		return err
	}
	if result != 0 {
		return fmt.Errorf("adapter init returned error")
	}
	return nil
}

// SubprocessConfig returns the agent process configuration from the adapter.
func (a *wasmAdapter) SubprocessConfig() SubprocessConfig {
	ptr, err := a.callFnNoArgs("SubprocessConfig")
	if err != nil {
		return SubprocessConfig{}
	}
	data := a.readResult(ptr)
	if len(data) == 0 {
		return SubprocessConfig{}
	}
	var cfg SubprocessConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return SubprocessConfig{}
	}
	return cfg
}

// EncodeCommand encodes a high-level command into protocol bytes.
func (a *wasmAdapter) EncodeCommand(cmd string, data []byte) []byte {
	req, _ := json.Marshal(map[string]any{"cmd": cmd, "data": data})
	ptr, err := a.callFn("EncodeCommand", req)
	if err != nil {
		return nil
	}
	return a.readResult(ptr)
}

// ParseOutput parses raw agent stdout bytes into events.
func (a *wasmAdapter) ParseOutput(data []byte) []Event {
	ptr, err := a.callFn("ParseOutput", data)
	if err != nil {
		return nil
	}
	jsonData := a.readResult(ptr)
	if len(jsonData) == 0 {
		return nil
	}
	var events []Event
	if err := json.Unmarshal(jsonData, &events); err != nil {
		return nil
	}
	return events
}

// HandleUIRequest generates a response for an agent UI request.
func (a *wasmAdapter) HandleUIRequest(id, method string) []byte {
	req, _ := json.Marshal(map[string]string{"id": id, "method": method})
	ptr, err := a.callFn("HandleUIRequest", req)
	if err != nil {
		return nil
	}
	return a.readResult(ptr)
}

// Close releases the wazero runtime.
func (a *wasmAdapter) Close() {
	if a.module != nil {
		a.module.Close(context.Background())
	}
	if a.runtime != nil {
		a.runtime.Close(context.Background())
	}
}
