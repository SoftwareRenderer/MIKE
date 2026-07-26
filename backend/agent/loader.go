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
}{
	factories: make(map[string]AdapterFactory),
}

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
//
// Memory model (per wazero allocation/tinygo/greet.go pattern):
//
//	Host → WASM (input): Host calls the module's Alloc(size) to obtain a linear
//	  memory offset, writes data there via api.Memory.Write, then passes
//	  (offset, size) as a `string` parameter to the exported function.
//	  The Go compiler maps `string` → (i32 ptr, i32 len) automatically.
//	  Pattern: Alloc → Write → Call → Free
//
//	WASM → host (output): The module calls the host-imported
//	  store_result(data string) callback, which reads the string from
//	  linear memory and delivers it via resultCh. Safe because the
//	  callback fires synchronously during fn.Call — before it returns.
type wasmAdapter struct {
	runtime  wazero.Runtime
	module   api.Module
	memory   api.Memory
	resultCh chan []byte // receives results from store_result host callback
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

	// Register WASI imports.
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("inject wasi: %w", err)
	}

	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("compile wasm module: %w", err)
	}

	// Register host callback: env.store_result
	// Called by the WASM module to pass results back to the host.
	resultCh := make(chan []byte, 1)
	_, err = runtime.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, ptr, length uint32) {
			if length == 0 {
				resultCh <- nil
				return
			}
			data, ok := m.Memory().Read(ptr, length)
			if !ok {
				resultCh <- nil
				return
			}
			result := make([]byte, length)
			copy(result, data)
			resultCh <- result
		}).
		Export("store_result").
		Instantiate(ctx)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("instantiate env host module: %w", err)
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
		runtime:  runtime,
		module:   inst,
		memory:   mem,
		resultCh: resultCh,
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

// allocString allocates WASM linear memory via the module's Alloc function,
// writes data there, and returns the offset. The caller must free it via
// freeString when done.
func (a *wasmAdapter) allocString(data []byte) (int32, error) {
	if len(data) == 0 {
		return 0, nil
	}
	allocFn := a.module.ExportedFunction(WasmFnAlloc)
	if allocFn == nil {
		return 0, fmt.Errorf("function %s not exported", WasmFnAlloc)
	}
	results, err := allocFn.Call(context.Background(), api.EncodeI32(int32(len(data))))
	if err != nil {
		return 0, fmt.Errorf("call %s: %w", WasmFnAlloc, err)
	}
	ptr := api.DecodeI32(results[0])
	if ptr == 0 {
		return 0, fmt.Errorf("%s returned 0 (buffer exhausted, size=%d)", WasmFnAlloc, len(data))
	}
	if !a.memory.Write(uint32(ptr), data) {
		return 0, fmt.Errorf("memory write failed at offset %d for %d bytes", ptr, len(data))
	}
	return ptr, nil
}

// freeString frees WASM memory allocated by allocString via the module's Free function.
func (a *wasmAdapter) freeString(ptr int32) {
	freeFn := a.module.ExportedFunction(WasmFnFree)
	if freeFn != nil {
		freeFn.Call(context.Background(), api.EncodeI32(ptr))
	}
}

// callFnStrings calls an exported WASM function with multiple string parameters.
// Each string is allocated via the module's Alloc function, written to linear
// memory, then passed as (ptr, length) parameters to the function.
// Results are delivered via the store_result host callback and read via readResult.
func (a *wasmAdapter) callFnStrings(name string, strs ...string) ([]byte, error) {
	fn := a.module.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("function %q not exported", name)
	}

	ctx := context.Background()
	var ptrs []int32
	var args []uint64

	for _, s := range strs {
		ptr, err := a.allocString([]byte(s))
		if err != nil {
			for _, p := range ptrs {
				a.freeString(p)
			}
			return nil, err
		}
		ptrs = append(ptrs, ptr)
		args = append(args, api.EncodeI32(ptr), api.EncodeI32(int32(len(s))))
	}

	defer func() {
		for _, p := range ptrs {
			a.freeString(p)
		}
	}()

	if _, err := fn.Call(ctx, args...); err != nil {
		return nil, fmt.Errorf("call %s: %w", name, err)
	}

	return a.readResult()
}

// readResult blocks until the store_result host callback delivers data.
// This is safe because store_result is called synchronously by the WASM
// runtime during fn.Call — the callback fires before fn.Call returns.
func (a *wasmAdapter) readResult() ([]byte, error) {
	select {
	case data := <-a.resultCh:
		return data, nil
	case <-context.Background().Done():
		return nil, fmt.Errorf("context cancelled waiting for result")
	}
}

// Init initializes the adapter with config JSON.
// Init returns int32 (0=success, 1=error) directly — not via store_result.
// The config is passed as a single string parameter: the host calls Alloc,
// writes the config, then passes (ptr, len) to the Init function.
func (a *wasmAdapter) Init(config []byte) error {
	fn := a.module.ExportedFunction(WasmFnInit)
	if fn == nil {
		return fmt.Errorf("function %s not exported", WasmFnInit)
	}

	ctx := context.Background()

	// Alloc + write the config string (even empty, passes (0,0) as empty string).
	ptr, err := a.allocString(config)
	if err != nil {
		return err
	}
	defer a.freeString(ptr)

	result, err := fn.Call(ctx, api.EncodeI32(ptr), api.EncodeI32(int32(len(config))))
	if err != nil {
		return fmt.Errorf("call %s: %w", WasmFnInit, err)
	}
	if api.DecodeI32(result[0]) != 0 {
		return fmt.Errorf("adapter init returned error")
	}

	// Validate adapter is functional by checking SubprocessConfig returns data.
	// This catches stale WASM binaries or Init implementations that return 0
	// but leave the adapter in an unusable state.
	cfg := a.SubprocessConfig()
	if cfg.Binary == "" {
		return fmt.Errorf("adapter init succeeded but SubprocessConfig returned empty binary (stale WASM?)")
	}

	return nil
}

// SubprocessConfig returns the agent process configuration from the adapter.
// SubprocessConfig takes no parameters; the result is delivered via store_result.
func (a *wasmAdapter) SubprocessConfig() SubprocessConfig {
	data, err := a.callFnStrings(WasmFnSubprocessConfig)
	if err != nil || len(data) == 0 {
		return SubprocessConfig{}
	}
	var cfg SubprocessConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return SubprocessConfig{}
	}
	return cfg
}

// EncodeCommand encodes a high-level command into protocol bytes.
// cmd and data are passed as separate string parameters directly to the
// WASM function (no JSON wrapper), matching the wazero string-passing pattern.
func (a *wasmAdapter) EncodeCommand(cmd string, data []byte) []byte {
	result, err := a.callFnStrings(WasmFnEncodeCommand, cmd, string(data))
	if err != nil {
		return nil
	}
	return result
}

// ParseOutput parses raw agent stdout bytes into events.
func (a *wasmAdapter) ParseOutput(data []byte) []Event {
	result, err := a.callFnStrings(WasmFnParseOutput, string(data))
	if err != nil || len(result) == 0 {
		return nil
	}
	var events []Event
	if err := json.Unmarshal(result, &events); err != nil {
		return nil
	}
	return events
}

// HandleUIRequest generates a response for an agent UI request.
// id and method are passed as separate string parameters directly to the
// WASM function (no JSON wrapper), matching the wazero string-passing pattern.
func (a *wasmAdapter) HandleUIRequest(id, method string) []byte {
	result, err := a.callFnStrings(WasmFnHandleUIRequest, id, method)
	if err != nil {
		return nil
	}
	return result
}

// Close releases the wazero runtime and calls the WASM adapter's Close function.
func (a *wasmAdapter) Close() {
	// Call the WASM Close function to clean up adapter state (e.g. adapter.Close()).
	fn := a.module.ExportedFunction(WasmFnClose)
	if fn != nil {
		fn.Call(context.Background())
	}
	if a.module != nil {
		a.module.Close(context.Background())
	}
	if a.runtime != nil {
		a.runtime.Close(context.Background())
	}
}
