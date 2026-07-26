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
// Memory model: host manages all allocation via api.Memory.
// Input data is written to linear memory; results are read from a shared buffer.
type wasmAdapter struct {
	runtime     wazero.Runtime
	module      api.Module
	memory      api.Memory
	lastResultPtrFn api.Function
	lastResultLenFn api.Function
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

	return &wasmAdapter{
		runtime:         runtime,
		module:          inst,
		memory:          mem,
		lastResultPtrFn: inst.ExportedFunction("lastResultPtr"),
		lastResultLenFn: inst.ExportedFunction("lastResultLen"),
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

// writeData writes data to WASM linear memory at a fixed offset (0).
// Returns the offset (always 0) since the host manages the buffer.
// inputOffset is a safe offset in WASM linear memory for input data.
// Low offsets are the WASM text/data section; 1MB is safe scratch space.
const inputOffset = 1 << 20

func (a *wasmAdapter) writeData(data []byte) (int32, error) {
	if len(data) > 1<<20 { // 1MB limit
		return 0, fmt.Errorf("input data too large: %d bytes", len(data))
	}
	if !a.memory.Write(inputOffset, data) {
		return 0, fmt.Errorf("memory write failed for %d bytes", len(data))
	}
	return inputOffset, nil
}

// callFn calls an exported WASM function with a byte slice argument.
// Returns the result data read from the shared result buffer.
func (a *wasmAdapter) callFn(name string, data []byte) ([]byte, error) {
	fn := a.module.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("function %q not exported", name)
	}

	ctx := context.Background()

	if len(data) == 0 {
		if _, err := fn.Call(ctx, api.EncodeI32(0), api.EncodeI32(0)); err != nil {
			return nil, fmt.Errorf("call %s: %w", name, err)
		}
	} else {
		ptr, err := a.writeData(data)
		if err != nil {
			return nil, err
		}
		if _, err := fn.Call(ctx, api.EncodeI32(ptr), api.EncodeI32(int32(len(data)))); err != nil {
			return nil, fmt.Errorf("call %s: %w", name, err)
		}
	}

	return a.readResult()
}

// callFnVoid calls an exported WASM function that returns void (no result data).
func (a *wasmAdapter) callFnVoid(name string, data []byte) error {
	fn := a.module.ExportedFunction(name)
	if fn == nil {
		return fmt.Errorf("function %q not exported", name)
	}

	ctx := context.Background()

	if len(data) == 0 {
		_, err := fn.Call(ctx, api.EncodeI32(0), api.EncodeI32(0))
		return err
	}

	ptr, err := a.writeData(data)
	if err != nil {
		return err
	}
	_, err = fn.Call(ctx, api.EncodeI32(ptr), api.EncodeI32(int32(len(data))))
	return err
}

// readResult reads data from the shared result buffer.
func (a *wasmAdapter) readResult() ([]byte, error) {
	ctx := context.Background()

	// Get result length
	lenResult, err := a.lastResultLenFn.Call(ctx)
	if err != nil {
		return nil, fmt.Errorf("call lastResultLen: %w", err)
	}
	length := api.DecodeI32(lenResult[0])
	if length == 0 {
		return nil, nil
	}

	// Get result pointer (base of shared buffer)
	var ptr uint32
	if a.lastResultPtrFn != nil {
		ptrResult, err := a.lastResultPtrFn.Call(ctx)
		if err != nil {
			return nil, fmt.Errorf("call lastResultPtr: %w", err)
		}
		ptr = uint32(api.DecodeI32(ptrResult[0]))
	}

	data, ok := a.memory.Read(ptr, uint32(length))
	if !ok {
		return nil, fmt.Errorf("memory read failed at offset %d length %d", ptr, length)
	}
	result := make([]byte, length)
	copy(result, data)
	return result, nil
}

// Init initializes the adapter with config JSON.
func (a *wasmAdapter) Init(config []byte) error {
	fn := a.module.ExportedFunction("Init")
	if fn == nil {
		return fmt.Errorf("function Init not exported")
	}

	ctx := context.Background()

	if len(config) == 0 {
		result, err := fn.Call(ctx, api.EncodeI32(0), api.EncodeI32(0))
		if err != nil {
			return fmt.Errorf("call Init: %w", err)
		}
		if api.DecodeI32(result[0]) != 0 {
			return fmt.Errorf("adapter init returned error")
		}
		return nil
	}

	ptr, err := a.writeData(config)
	if err != nil {
		return err
	}
	result, err := fn.Call(ctx, api.EncodeI32(ptr), api.EncodeI32(int32(len(config))))
	if err != nil {
		return fmt.Errorf("call Init: %w", err)
	}
	if api.DecodeI32(result[0]) != 0 {
		return fmt.Errorf("adapter init returned error")
	}
	return nil
}

// SubprocessConfig returns the agent process configuration from the adapter.
func (a *wasmAdapter) SubprocessConfig() SubprocessConfig {
	data, err := a.callFn("SubprocessConfig", nil)
	if err != nil {
		return SubprocessConfig{}
	}
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
	result, err := a.callFn("EncodeCommand", req)
	if err != nil {
		return nil
	}
	return result
}

// ParseOutput parses raw agent stdout bytes into events.
func (a *wasmAdapter) ParseOutput(data []byte) []Event {
	result, err := a.callFn("ParseOutput", data)
	if err != nil {
		return nil
	}
	if len(result) == 0 {
		return nil
	}
	var events []Event
	if err := json.Unmarshal(result, &events); err != nil {
		return nil
	}
	return events
}

// HandleUIRequest generates a response for an agent UI request.
func (a *wasmAdapter) HandleUIRequest(id, method string) []byte {
	req, _ := json.Marshal(map[string]string{"id": id, "method": method})
	result, err := a.callFn("HandleUIRequest", req)
	if err != nil {
		return nil
	}
	return result
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
