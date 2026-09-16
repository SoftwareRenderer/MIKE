package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"mike/backend/extensions"
	"mike/pkg/agent"
	"mike/pkg/logging"
)

// adapterRegistry holds registered adapter factories. Production loads WASM; tests
// register mocks through RegisterAdapterFactory.
var adapterRegistry = struct {
	sync.RWMutex
	factories map[string]agent.AdapterFactory
}{
	factories: make(map[string]agent.AdapterFactory),
}

// RegisterAdapterFactory overrides how the adapter for an extension name is produced. A test seam,
// not a configuration path: nothing in the server calls it, and a real extension's adapter is
// always its own WASM module. It lives outside _test.go so another package's test can register the
// fake — backend/exec holds the test that a sandboxed task probes the binary its adapter named.
func RegisterAdapterFactory(name string, fn agent.AdapterFactory) {
	adapterRegistry.Lock()
	defer adapterRegistry.Unlock()
	adapterRegistry.factories[name] = fn
}

// wasmAdapter wraps a wazero-loaded WASI reactor module behind the AgentAdapter interface.
//
// Memory model (the wazero tinygo/greet.go pattern):
//
//	Host → WASM: Alloc(size), write the bytes there, pass (offset, size) as a `string`
//	  parameter. Alloc → Write → Call → Free.
//	WASM → host: the module calls the host-imported store_result(data string), which reads
//	  the string out of linear memory and delivers it on resultCh — safe because that
//	  callback fires synchronously, before fn.Call returns.
//
// Every exported call must be serialized under mu: the module, its bump allocator and the
// size-1 resultCh are not concurrency-safe, and AgentRunner.readOutput calls ParseOutput
// *without* holding r.mu.
type wasmAdapter struct {
	mu       sync.Mutex
	runtime  wazero.Runtime
	module   api.Module
	memory   api.Memory
	resultCh chan []byte // receives results from store_result host callback
}

// LoadAdapter loads an AgentAdapter for the given extension.
// It first checks for a registered factory (test mocks), then falls back to WASM.
func LoadAdapter(ext *extensions.Extension) (agent.AgentAdapter, error) {
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
func loadWasmAdapter(ext *extensions.Extension) (agent.AgentAdapter, error) {
	if ext.Manifest.Capabilities.GetAgent() == nil || ext.Manifest.Capabilities.GetAgent().WasmModule == "" {
		return nil, fmt.Errorf("extension %q has no wasm_module configured", ext.Name)
	}

	wasmPath := resolveWasmPath(ext)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read wasm module %s: %w", wasmPath, err)
	}

	ctx := context.Background()
	runtime := wazero.NewRuntimeWithConfig(ctx,
		wazero.NewRuntimeConfig().
			WithMemoryLimitPages(512).       // 32 MiB max — ensures safety while providing headroom for persistence spikes
			WithMemoryCapacityFromMax(true), // Size memory to the module's declared max, not just initial
	)

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
	wasmModule := ext.Manifest.Capabilities.GetAgent().WasmModule

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
	allocFn := a.module.ExportedFunction(agent.WasmFnAlloc)
	if allocFn == nil {
		return 0, fmt.Errorf("function %s not exported", agent.WasmFnAlloc)
	}
	results, err := allocFn.Call(context.Background(), api.EncodeI32(int32(len(data))))
	if err != nil {
		return 0, fmt.Errorf("call %s: %w", agent.WasmFnAlloc, err)
	}
	ptr := api.DecodeI32(results[0])
	if ptr == 0 {
		return 0, fmt.Errorf("%s returned 0 (buffer exhausted, size=%d)", agent.WasmFnAlloc, len(data))
	}
	if !a.memory.Write(uint32(ptr), data) {
		return 0, fmt.Errorf("memory write failed at offset %d for %d bytes", ptr, len(data))
	}
	return ptr, nil
}

// freeString frees WASM memory allocated by allocString via the module's Free function.
func (a *wasmAdapter) freeString(ptr int32) {
	freeFn := a.module.ExportedFunction(agent.WasmFnFree)
	if freeFn != nil {
		freeFn.Call(context.Background(), api.EncodeI32(ptr))
	}
}

// callFnStrings calls an exported WASM function with several string parameters: each is
// allocated with the module's Alloc, written to linear memory, then passed as (ptr, len).
// The result arrives through store_result (see readResult); ctx cancels fn.Call and the wait
// for it. Callers must hold mu - see wasmAdapter.
func (a *wasmAdapter) callFnStrings(ctx context.Context, name string, strs ...string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.callFnStringsLocked(ctx, name, strs...)
}

// callFnStringsLocked is for callers already holding a.mu (e.g. Init); callFnStrings there re-enters
// the lock.
func (a *wasmAdapter) callFnStringsLocked(ctx context.Context, name string, strs ...string) ([]byte, error) {
	fn := a.module.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("function %q not exported", name)
	}

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
		// Drain any leftover result from the store_result callback that may have
		// been sent before the error. Without this, resultCh (capacity 1) stays
		// full and the next callFnStrings invocation deadlocks when store_result
		// blocks on the full channel.
		select {
		case <-a.resultCh:
		default:
		}
		return nil, fmt.Errorf("call %s: %w", name, err)
	}

	return a.readResult(ctx)
}

// readResult blocks until the store_result host callback delivers data or ctx is cancelled.
// Safe because store_result is called synchronously by the WASM runtime during fn.Call:
// the callback fires before fn.Call returns. If the module never calls it, ctx.Done() is
// the way out.
func (a *wasmAdapter) readResult(ctx context.Context) ([]byte, error) {
	select {
	case data := <-a.resultCh:
		return data, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("adapter call cancelled while waiting for result: %w", ctx.Err())
	}
}

// Init initializes the adapter with config JSON.
// Init returns int32 (0=success, 1=error) directly — not via store_result.
// The config is passed as a single string parameter: the host calls Alloc,
// writes the config, then passes (ptr, len) to the Init function.
// ctx is used for cancellation during initialization and the validation call.
func (a *wasmAdapter) Init(ctx context.Context, config []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	fn := a.module.ExportedFunction(agent.WasmFnInit)
	if fn == nil {
		return fmt.Errorf("function %s not exported", agent.WasmFnInit)
	}

	// Alloc + write the config string (even empty, passes (0,0) as empty string).
	ptr, err := a.allocString(config)
	if err != nil {
		return err
	}
	defer a.freeString(ptr)

	result, err := fn.Call(ctx, api.EncodeI32(ptr), api.EncodeI32(int32(len(config))))
	if err != nil {
		return fmt.Errorf("call %s: %w", agent.WasmFnInit, err)
	}
	if api.DecodeI32(result[0]) != 0 {
		return fmt.Errorf("adapter init returned error")
	}

	// Validate adapter is functional by checking SubprocessConfig returns data.
	// This catches stale WASM binaries or Init implementations that return 0
	// but leave the adapter in an unusable state. Call callFnStringsLocked
	// directly since we already hold a.mu.
	cfgData, err := a.callFnStringsLocked(ctx, agent.WasmFnSubprocessConfig)
	if err != nil {
		return fmt.Errorf("validate SubprocessConfig: %w", err)
	}
	var cfg agent.SubprocessConfig
	if len(cfgData) > 0 {
		json.Unmarshal(cfgData, &cfg)
	}
	if cfg.Binary == "" {
		return fmt.Errorf("adapter init succeeded but SubprocessConfig returned empty binary (stale WASM?)")
	}

	return nil
}

// SubprocessConfig returns the agent process configuration from the adapter.
// SubprocessConfig takes no parameters; the result is delivered via store_result.
// ctx is used for cancellation during the store_result callback wait.
func (a *wasmAdapter) SubprocessConfig(ctx context.Context) agent.SubprocessConfig {
	data, err := a.callFnStrings(ctx, agent.WasmFnSubprocessConfig)
	if err != nil || len(data) == 0 {
		logging.For(logging.Agent).Warnf("SubprocessConfig failed: %v", err)
		return agent.SubprocessConfig{}
	}
	var cfg agent.SubprocessConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		logging.For(logging.Agent).Warnf("SubprocessConfig unmarshal failed: %v", err)
		return agent.SubprocessConfig{}
	}
	return cfg
}

// EncodeCommand encodes a high-level command into protocol bytes.
// cmd and data are passed as separate string parameters directly to the
// WASM function (no JSON wrapper), matching the wazero string-passing pattern.
// ctx is used for cancellation during the store_result callback wait.
func (a *wasmAdapter) EncodeCommand(ctx context.Context, cmd string, data []byte) []byte {
	result, err := a.callFnStrings(ctx, agent.WasmFnEncodeCommand, cmd, string(data))
	if err != nil {
		logging.For(logging.Agent).Warnf("EncodeCommand(%q) failed: %v", cmd, err)
		return nil
	}
	return result
}

// ParseOutput parses raw agent stdout bytes into events.
// ctx is used for cancellation during the store_result callback wait.
func (a *wasmAdapter) ParseOutput(ctx context.Context, data []byte) []agent.Event {
	result, err := a.callFnStrings(ctx, agent.WasmFnParseOutput, string(data))
	if err != nil || len(result) == 0 {
		logging.For(logging.Agent).Warnf("ParseOutput failed: %v", err)
		return nil
	}
	var events []agent.Event
	if err := json.Unmarshal(result, &events); err != nil {
		logging.For(logging.Agent).Warnf("ParseOutput unmarshal failed: %v", err)
		return nil
	}
	return events
}

// HandleUIRequest generates a response for an agent UI request.
// id and method are passed as separate string parameters directly to the
// WASM function (no JSON wrapper), matching the wazero string-passing pattern.
// ctx is used for cancellation during the store_result callback wait.
func (a *wasmAdapter) HandleUIRequest(ctx context.Context, id, method string) []byte {
	result, err := a.callFnStrings(ctx, agent.WasmFnHandleUIRequest, id, method)
	if err != nil {
		logging.For(logging.Agent).Warnf("HandleUIRequest(%q, %q) failed: %v", id, method, err)
		return nil
	}
	return result
}

// Close releases the wazero runtime and calls the WASM adapter's Close function.
// The mutex prevents Close from racing with concurrent callFnStrings/Init calls.
func (a *wasmAdapter) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	fn := a.module.ExportedFunction(agent.WasmFnClose)
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

// ListModels returns the list of models available from the agent.
// It runs the agent binary with "--list-models" flag and parses the tabular output.
// This is implemented natively (not via WASM) because it requires subprocess execution.
func (a *wasmAdapter) ListModels(ctx context.Context) []agent.AgentModel {
	cfg := a.SubprocessConfig(ctx)
	if cfg.Binary == "" {
		logging.For(logging.Agent).Infof("ListModels: no binary configured")
		return nil
	}

	cmd := exec.CommandContext(ctx, cfg.Binary, "--list-models")
	// Same rule as the agent runner: the allowlisted environment only (see SafeAgentEnv).
	cmd.Env = SafeAgentEnv(cfg.Env...)
	output, err := cmd.Output()
	if err != nil {
		logging.For(logging.Agent).Warnf("ListModels: failed to run %s --list-models: %v", cfg.Binary, err)
		return nil
	}

	var models []agent.AgentModel
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "provider") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			fullName := fields[0] + "/" + fields[1]
			var ctxWin *int
			if len(fields) >= 3 {
				if v, err := parseContextWindow(fields[2]); err == nil && v > 0 {
					ctxWin = &v
				}
			}
			models = append(models, agent.AgentModel{Name: fullName, ContextWindow: ctxWin})
		}
	}
	return models
}

// parseContextWindow parses context window strings like "256K", "196.6K", "1M" into integers.
func parseContextWindow(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty string")
	}

	var multiplier int64
	lastChar := s[len(s)-1]
	switch lastChar {
	case 'K', 'k':
		multiplier = 1000
		s = s[:len(s)-1]
	case 'M', 'm':
		multiplier = 1000000
		s = s[:len(s)-1]
	case 'G', 'g':
		multiplier = 1000000000
		s = s[:len(s)-1]
	default:
		multiplier = 1
	}

	if multiplier == 1 {
		return strconv.Atoi(s)
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int(f * float64(multiplier)), nil
}
