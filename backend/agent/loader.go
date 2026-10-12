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

// adapterRegistry holds registered adapter factories; tests can use RegisterAdapterFactory to mock.
var adapterRegistry = struct {
	sync.RWMutex
	factories map[string]agent.AdapterFactory
}{
	factories: make(map[string]agent.AdapterFactory),
}

// RegisterAdapterFactory overrides adapter factory for tests; not for production config.
func RegisterAdapterFactory(name string, fn agent.AdapterFactory) {
	adapterRegistry.Lock()
	defer adapterRegistry.Unlock()
	adapterRegistry.factories[name] = fn
}

// wasmAdapter wraps a wazero WASI reactor module.
//
// Memory model:
//
//	Host → WASM: Alloc(size), write bytes, pass (offset, size) as a `string`
//	  parameter. Alloc → Write → Call → Free.
//	WASM → host: the module calls host-imported store_result(data string), which reads
//	  the string and delivers it on resultCh — synchronous, before fn.Call returns.
//
// serialized under mu: module, allocator and resultCh are not concurrency-safe.
type wasmAdapter struct {
	mu       sync.Mutex
	runtime  wazero.Runtime
	module   api.Module
	memory   api.Memory
	resultCh chan []byte // receives results from store_result host callback
}

// LoadAdapter loads AgentAdapter from factory (tests) or WASM.
func LoadAdapter(ext *extensions.Extension) (agent.AgentAdapter, error) {
	adapterRegistry.RLock()
	factory, ok := adapterRegistry.factories[ext.Name]
	adapterRegistry.RUnlock()
	if ok {
		return factory(), nil
	}

	return loadWasmAdapter(ext)
}

// loadWasmAdapter instantiates a WASI reactor module using -buildmode=c-shared.
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

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("inject wasi: %w", err)
	}

	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("compile wasm module: %w", err)
	}

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

// allocString allocates WASM linear memory and writes data; must be freed.
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

func (a *wasmAdapter) freeString(ptr int32) {
	freeFn := a.module.ExportedFunction(agent.WasmFnFree)
	if freeFn != nil {
		freeFn.Call(context.Background(), api.EncodeI32(ptr))
	}
}

// callFnStrings calls exported WASM function with string parameters; requires mu.
func (a *wasmAdapter) callFnStrings(ctx context.Context, name string, strs ...string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.callFnStringsLocked(ctx, name, strs...)
}

// callFnStringsLocked is callFnStrings without locking.
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
		// drain resultCh to prevent deadlock on next call.
		select {
		case <-a.resultCh:
		default:
		}
		return nil, fmt.Errorf("call %s: %w", name, err)
	}

	return a.readResult(ctx)
}

// readResult blocks until store_result delivers data or ctx is cancelled.
func (a *wasmAdapter) readResult(ctx context.Context) ([]byte, error) {
	select {
	case data := <-a.resultCh:
		return data, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("adapter call cancelled while waiting for result: %w", ctx.Err())
	}
}

// Init initializes adapter with config JSON; returns error if init or validation fails.
func (a *wasmAdapter) Init(ctx context.Context, config []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	fn := a.module.ExportedFunction(agent.WasmFnInit)
	if fn == nil {
		return fmt.Errorf("function %s not exported", agent.WasmFnInit)
	}

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

func (a *wasmAdapter) EncodeCommand(ctx context.Context, cmd string, data []byte) []byte {
	result, err := a.callFnStrings(ctx, agent.WasmFnEncodeCommand, cmd, string(data))
	if err != nil {
		logging.For(logging.Agent).Warnf("EncodeCommand(%q) failed: %v", cmd, err)
		return nil
	}
	return result
}

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

func (a *wasmAdapter) HandleUIRequest(ctx context.Context, id, method string) []byte {
	result, err := a.callFnStrings(ctx, agent.WasmFnHandleUIRequest, id, method)
	if err != nil {
		logging.For(logging.Agent).Warnf("HandleUIRequest(%q, %q) failed: %v", id, method, err)
		return nil
	}
	return result
}

// Close releases wazero runtime and calls WASM Close; requires mu.
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

// ListModels returns models by running binary with --list-models; implemented natively.
func (a *wasmAdapter) ListModels(ctx context.Context) []agent.AgentModel {
	cfg := a.SubprocessConfig(ctx)
	if cfg.Binary == "" {
		logging.For(logging.Agent).Infof("ListModels: no binary configured")
		return nil
	}

	cmd := exec.CommandContext(ctx, cfg.Binary, "--list-models")
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

// parseContextWindow parses context window strings (e.g. 256K, 1M).
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
