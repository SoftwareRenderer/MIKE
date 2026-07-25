package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"kanban/backend/extensions"
)

// adapterRegistry holds registered adapter factories.
// Each agent extension registers its factory via RegisterAdapterFactory.
var adapterRegistry = struct {
	sync.RWMutex
	factories map[string]AdapterFactory
}{factories: make(map[string]AdapterFactory)}

// AdapterFactory creates a new AgentAdapter instance.
// Registered by each agent extension package.
type AdapterFactory func() AgentAdapter

// RegisterAdapterFactory registers an adapter factory for an extension name.
// Called by agent extension packages (e.g., pi adapter init).
func RegisterAdapterFactory(name string, fn AdapterFactory) {
	adapterRegistry.Lock()
	defer adapterRegistry.Unlock()
	adapterRegistry.factories[name] = fn
}

// loadAdapter loads an AgentAdapter for the given extension.
// It first checks for a registered Go factory, then falls back to WASM loading.
func loadAdapter(ext *extensions.Extension) (AgentAdapter, error) {
	// Check for registered Go factory (development / built-in adapters)
	adapterRegistry.RLock()
	factory, ok := adapterRegistry.factories[ext.Name]
	adapterRegistry.RUnlock()
	if ok {
		return factory(), nil
	}

	// Fall back to WASM module loading
	return loadWasmAdapter(ext)
}

// wasmAdapter wraps a wazero-loaded WASM module behind the AgentAdapter interface.
// Go-compiled WASM (wasip1) communicates via stdin/stdout JSON protocol.
// The adapter module runs as a long-running WASI process; the runner pipes
// JSON commands via stdin and reads JSON responses from stdout.
type wasmAdapter struct {
	runtime    wazero.Runtime
	module     api.Module
	stdin      *stdinBuffer
	stdout     *capturedBuffer
	config     []byte
	initialized bool
}

// stdinBuffer is a thread-safe buffer that acts as an io.Reader for WASI stdin.
type stdinBuffer struct {
	sync.Mutex
	data []byte
	read int
}

func (s *stdinBuffer) Write(p []byte) (int, error) {
	s.Lock()
	defer s.Unlock()
	s.data = append(s.data, p...)
	return len(p), nil
}

func (s *stdinBuffer) Read(p []byte) (int, error) {
	s.Lock()
	defer s.Unlock()
	if s.read >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.read:])
	s.read += n
	return n, nil
}

func (s *stdinBuffer) Reset() {
	s.Lock()
	defer s.Unlock()
	s.data = s.data[:0]
	s.read = 0
}

func loadWasmAdapter(ext *extensions.Extension) (AgentAdapter, error) {
	if ext.Manifest.Agent == nil || ext.Manifest.Agent.WasmModule == "" {
		return nil, fmt.Errorf("extension %s has no wasm_module configured", ext.Name)
	}

	// Resolve WASM module path
	wasmPath := resolveWasmPath(ext)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read wasm module %s: %w", wasmPath, err)
	}

	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)

	// Inject WASI for Go-compiled (wasip1) modules
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("inject wasi: %w", err)
	}

	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("compile wasm module: %w", err)
	}

	stdin := &stdinBuffer{}
	stdout := &capturedBuffer{}

	config := wazero.NewModuleConfig().
		WithStdin(stdin).
		WithStdout(stdout).
		WithStderr(os.Stderr).
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep()

	inst, err := runtime.InstantiateModule(ctx, compiled, config)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("instantiate wasm module: %w", err)
	}

	return &wasmAdapter{
		runtime: runtime,
		module:  inst,
		stdin:   stdin,
		stdout:  stdout,
	}, nil
}

// resolveWasmPath finds the WASM module file for an extension.
func resolveWasmPath(ext *extensions.Extension) string {
	wasmModule := ext.Manifest.Agent.WasmModule

	// Try relative to extension directory
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

	// Return the last candidate (will produce a clear error from os.ReadFile)
	return candidates[len(candidates)-1]
}

// wasmCommand represents a JSON command sent to the WASM adapter.
type wasmCommand struct {
	Type string         `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// sendCommand sends a JSON command to the WASM adapter via stdin and reads the response from stdout.
func (a *wasmAdapter) sendCommand(cmdType string, data []byte) ([]byte, error) {
	a.stdout.Reset()

	cmd := wasmCommand{Type: cmdType, Data: data}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	cmdBytes = append(cmdBytes, '\n')

	if _, err := a.stdin.Write(cmdBytes); err != nil {
		return nil, fmt.Errorf("write to adapter stdin: %w", err)
	}

	// For Go wasip1 modules, the module runs as main() and processes
	// commands from stdin in a loop. The response is written to stdout.
	// Since wazero runs synchronously, we need the module to be designed
	// to process one command and return, or we need a different approach.
	//
	// For now, use the exported function approach for direct calls.
	_ = cmd

	// Try direct function call first
	fn := a.module.ExportedFunction(cmdType)
	if fn != nil {
		// Write data to stdin buffer for the function to read
		if data != nil {
			a.stdin.Reset()
			a.stdin.Write(data)
		}
		a.stdout.Reset()

		_, err := fn.Call(context.Background())
		if err != nil {
			return nil, fmt.Errorf("call wasm function %s: %w", cmdType, err)
		}
		return a.stdout.Bytes(), nil
	}

	// Fall back: return captured stdout
	return a.stdout.Bytes(), nil
}

// Init initializes the adapter with config JSON.
func (a *wasmAdapter) Init(config []byte) error {
	a.config = config
	result, err := a.sendCommand(WasmFnInit, config)
	if err != nil {
		return err
	}
	// Check for error response
	if len(result) > 0 {
		var resp map[string]any
		if err := json.Unmarshal(result, &resp); err == nil {
			if errMsg, ok := resp["error"].(string); ok && errMsg != "" {
				return fmt.Errorf("adapter init error: %s", errMsg)
			}
		}
	}
	a.initialized = true
	return nil
}

// SubprocessConfig returns the agent process configuration from the adapter.
func (a *wasmAdapter) SubprocessConfig() SubprocessConfig {
	result, err := a.sendCommand(WasmFnSubprocessConfig, nil)
	if err != nil {
		return SubprocessConfig{}
	}
	var cfg SubprocessConfig
	if err := json.Unmarshal(result, &cfg); err != nil {
		return SubprocessConfig{}
	}
	return cfg
}

// EncodeCommand encodes a high-level command into protocol bytes.
func (a *wasmAdapter) EncodeCommand(cmd string, data []byte) []byte {
	result, err := a.sendCommand(WasmFnEncodeCommand, encodeCommandRequest(cmd, data))
	if err != nil {
		return nil
	}
	return result
}

// encodeCommandRequest builds the JSON request for EncodeCommand.
func encodeCommandRequest(cmd string, data []byte) []byte {
	req := map[string]any{"command": cmd}
	if data != nil {
		req["data"] = json.RawMessage(data)
	}
	result, _ := json.Marshal(req)
	return result
}

// ParseOutput parses raw agent stdout bytes into events.
func (a *wasmAdapter) ParseOutput(data []byte) []Event {
	result, err := a.sendCommand(WasmFnParseOutput, data)
	if err != nil {
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
	result, err := a.sendCommand(WasmFnHandleUIRequest, req)
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

// capturedBuffer is a writable buffer for capturing WASM stdout.
type capturedBuffer struct {
	data []byte
}

func (c *capturedBuffer) Write(p []byte) (int, error) {
	c.data = append(c.data, p...)
	return len(p), nil
}

func (c *capturedBuffer) Bytes() []byte {
	return c.data
}

func (c *capturedBuffer) Reset() {
	c.data = c.data[:0]
}
