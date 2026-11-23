package extensions

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"io/fs"
	"mike/pkg/sandboxruntime"
)

func testHandler(t *testing.T) *ExtensionHandler {
	t.Helper()
	h, _ := testHandlerWithStore(t)
	return h
}

// testHandlerWithStore is testHandler with the store handed back, for the tests
// that need to break it (a handler answers 500 only when the store really fails).
func testHandlerWithStore(t *testing.T) (*ExtensionHandler, *ExtensionStore) {
	t.Helper()
	extStore := testStore(t)
	// The handler's registry builds images on install and update, so it carries the committed bridge:
	// a build stages that binary into its context and has no other way to find it.
	reg := NewRegistryClientWithBoot(bridgeBoot(t))
	cfg := &ExtensionConfig{
		RegistryURL: "",
	}
	sandbox := NewExtensionSandboxManager(reg, extStore, sandboxruntime.Boot{})
	return NewExtensionHandler(extStore, sandbox, reg, cfg), extStore
}

// guardHolds reports whether the in-flight guard is held for name. The map is guarded by
// inFlight.mu, so an observation has to take the lock.
func guardHolds(g *inFlight, name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.busy[name]
}

// readSSEEvents parses the SSE payloads written to a recorder body.
func readSSEEvents(t *testing.T, body []byte) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("parse SSE event %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// The board's list is what Settings → Extensions renders: an empty install set is an empty array
// with 200 (never an error the panel would render as "nothing installed"), and a row arrives with the
// fields the card reads — name, type and the enabled switch it toggles.
func TestHandleList(t *testing.T) {
	h := testHandler(t)
	list := func(t *testing.T) []ExtensionResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/extensions", nil)
		w := httptest.NewRecorder()
		h.HandleList(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var result []ExtensionResponse
		if err := json.UnmarshalRead(w.Body, &result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return result
	}

	t.Run("nothing installed", func(t *testing.T) {
		if got := list(t); len(got) != 0 {
			t.Errorf("result count = %d, want 0", len(got))
		}
	})

	t.Run("an installed extension is described", func(t *testing.T) {
		for _, name := range []string{"gopls", "typescript"} {
			ext := makeTestExt(name, name, ExtensionTypeLSP)
			if err := h.store.Create(ext); err != nil {
				t.Fatalf("Create(%s): %v", name, err)
			}
		}
		result := list(t)
		if len(result) != 2 {
			t.Fatalf("result count = %d, want 2", len(result))
		}
		if result[0].Name == "" {
			t.Error("result[0].Name is empty")
		}
		if result[0].Type != ExtensionTypeLSP {
			t.Errorf("result[0].Type = %q, want lsp", result[0].Type)
		}
		if !result[0].Enabled {
			t.Error("result[0].Enabled = false, want true")
		}
	})
}

func TestHandleList_SortedByPriority(t *testing.T) {
	h := testHandler(t)
	// Create extensions with different priorities (insert in random order)
	extA := makeTestExt("ext-a", "ext-a", ExtensionTypeUI)
	extA.Manifest.Priority = 10
	extA.Type = ExtensionTypeUI
	if err := h.store.Create(extA); err != nil {
		t.Fatalf("Create(ext-a): %v", err)
	}
	extB := makeTestExt("ext-b", "ext-b", ExtensionTypeUI)
	extB.Manifest.Priority = 0
	extB.Type = ExtensionTypeUI
	if err := h.store.Create(extB); err != nil {
		t.Fatalf("Create(ext-b): %v", err)
	}
	extC := makeTestExt("ext-c", "ext-c", ExtensionTypeUI)
	extC.Manifest.Priority = 5
	extC.Type = ExtensionTypeUI
	if err := h.store.Create(extC); err != nil {
		t.Fatalf("Create(ext-c): %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/extensions", nil)
	w := httptest.NewRecorder()

	h.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result []ExtensionResponse
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("result count = %d, want 3", len(result))
	}
	// Should be sorted by priority ASC: 0, 5, 10
	if result[0].Name != "ext-b" {
		t.Errorf("First = %q (priority %d), want ext-b (priority 0)", result[0].Name, result[0].Manifest.Priority)
	}
	if result[1].Name != "ext-c" {
		t.Errorf("Second = %q (priority %d), want ext-c (priority 5)", result[1].Name, result[1].Manifest.Priority)
	}
	if result[2].Name != "ext-a" {
		t.Errorf("Third = %q (priority %d), want ext-a (priority 10)", result[2].Name, result[2].Manifest.Priority)
	}
}

func TestHandleList_SamePriority_SortedByName(t *testing.T) {
	h := testHandler(t)
	// Create extensions with same priority
	for _, name := range []string{"zebra", "alpha", "beta"} {
		ext := makeTestExt(name, name, ExtensionTypeUI)
		ext.Manifest.Priority = 0
		ext.Type = ExtensionTypeUI
		if err := h.store.Create(ext); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/extensions", nil)
	w := httptest.NewRecorder()

	h.HandleList(w, req)

	var result []ExtensionResponse
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("result count = %d, want 3", len(result))
	}
	// Same priority → sorted by name
	if result[0].Name != "alpha" {
		t.Errorf("First = %q, want alpha", result[0].Name)
	}
	if result[2].Name != "zebra" {
		t.Errorf("Last = %q, want zebra", result[2].Name)
	}
}

// The wrong-method case is gone from here on purpose: the route pattern declares GET, so the mux
// refuses anything else before HandleList runs, and no handler-level call can reach that decision.
// It is asserted at backend/server/routes_method_test.go instead.

func TestHandleGet_Found(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/test-ext", nil)
	w := httptest.NewRecorder()

	h.HandleGet(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result ExtensionResponse
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Name != "test-extension" {
		t.Errorf("Name = %q, want test-extension", result.Name)
	}
}

func TestHandleUpdate(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := map[string]any{
		"version": "2.0.0",
		"config": map[string]any{
			"memoryLimit": 1024,
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result ExtensionResponse
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Version != "2.0.0" {
		t.Errorf("Version = %q, want 2.0.0", result.Version)
	}
}

func TestHandleUpdate_ConfigMemoryLimitSync(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	ext.SandboxConfig.MemoryLimit = 256 // a previously-applied limit
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update with memory_limit in config (as the frontend edit form sends)
	body := map[string]any{
		"config": map[string]any{
			"memory_limit": float64(512), // JSON numbers are float64
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	// Verify SandboxConfig.MemoryLimit was synced from config
	got, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SandboxConfig.MemoryLimit != 512 {
		t.Errorf("SandboxConfig.MemoryLimit = %d, want 512", got.SandboxConfig.MemoryLimit)
	}
	// Config should also contain the value
	if ml, ok := got.Config["memory_limit"]; !ok || ml != float64(512) {
		t.Errorf("Config[\"memory_limit\"] = %v, want 512", ml)
	}
}

func TestHandleUpdate_MemoryLimitZeroClearsLimit(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	ext.SandboxConfig.MemoryLimit = 512 // a previously-applied limit
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Save 0 (no limit) via the edit form's config payload. No sandbox is
	// running, so the live-apply path is a no-op and the update still succeeds.
	body := map[string]any{
		"config": map[string]any{
			"memory_limit": float64(0),
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	got, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SandboxConfig.MemoryLimit != 0 {
		t.Errorf("SandboxConfig.MemoryLimit = %d, want 0 (cleared)", got.SandboxConfig.MemoryLimit)
	}
	if ml, ok := got.Config["memory_limit"]; !ok || ml != float64(0) {
		t.Errorf("Config[\"memory_limit\"] = %v, want 0", ml)
	}
}

func TestHandleUpdate_MemoryLimitNegativeClampedToZero(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := map[string]any{
		"config": map[string]any{
			"memory_limit": float64(-64),
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	got, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SandboxConfig.MemoryLimit != 0 {
		t.Errorf("SandboxConfig.MemoryLimit = %d, want 0 (negative clamped)", got.SandboxConfig.MemoryLimit)
	}
}

func TestHandleUpdate_NotFound(t *testing.T) {
	h := testHandler(t)
	body := map[string]any{
		"version": "2.0.0",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/nonexistent", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "nonexistent")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleUpdate_LSPConfig(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	ext.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{
		Command: "typescript-language-server",
		Args:    []string{"--stdio"},
		FileTypes: []LSPFileType{
			{Extension: "ts", LanguageID: "typescript"},
			{Extension: "tsx", LanguageID: "typescriptreact"},
			{Extension: "js", LanguageID: "javascript"},
			{Extension: "jsx", LanguageID: "javascriptreact"},
		},
		Language: "typescript",
	}}
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := map[string]any{
		"manifest": map[string]any{
			"capabilities": map[string]any{
				"lsp": map[string]any{
					"initParamsOverride": map[string]any{
						"tsserver.path": "/usr/local/lib/node_modules/typescript",
					},
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	// Verify LSP config was updated in DB
	updated, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Manifest.Capabilities.GetLSP() == nil {
		t.Fatal("LSP config is nil after update")
	}
	if updated.Manifest.Capabilities.GetLSP().InitParamsOverride == nil {
		t.Fatal("InitParamsOverride is nil after update")
	}
	tsserverPath, ok := updated.Manifest.Capabilities.GetLSP().InitParamsOverride["tsserver.path"]
	if !ok {
		t.Fatal("tsserver.path not found in InitParamsOverride")
	}
	if tsserverPath != "/usr/local/lib/node_modules/typescript" {
		t.Errorf("tsserver.path = %v, want /usr/local/lib/node_modules/typescript", tsserverPath)
	}
}

func TestHandleUpdate_LSPConfig_BoolFlags(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	ext.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{
		Command:   "gopls",
		Args:      []string{"serve"},
		FileTypes: []LSPFileType{{Extension: "go", LanguageID: "go"}},
		Language:  "go",
	}}
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update editor_enabled, tool_calls_enabled and chat_diagnostics_enabled
	body := map[string]any{
		"manifest": map[string]any{
			"capabilities": map[string]any{
				"lsp": map[string]any{
					"editor_enabled":           false,
					"tool_calls_enabled":       true,
					"chat_diagnostics_enabled": true,
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	updated, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	lspCfg := updated.Manifest.Capabilities.GetLSP()
	if lspCfg == nil {
		t.Fatal("LSP config is nil after update")
	}
	if lspCfg.EditorEnabled == nil {
		t.Fatal("EditorEnabled is nil after update")
	}
	if *lspCfg.EditorEnabled {
		t.Error("EditorEnabled = true, want false")
	}
	if lspCfg.ToolCallsEnabled == nil {
		t.Fatal("ToolCallsEnabled is nil after update")
	}
	if !*lspCfg.ToolCallsEnabled {
		t.Error("ToolCallsEnabled = false, want true")
	}
	if lspCfg.ChatDiagnosticsEnabled == nil || !*lspCfg.ChatDiagnosticsEnabled {
		t.Error("ChatDiagnosticsEnabled = nil or false, want true (opting in must land)")
	}

	// And back off: an explicit false has to overwrite the stored true, or the Settings
	// toggle could be switched on but never off.
	off, _ := json.Marshal(map[string]any{
		"manifest": map[string]any{"capabilities": map[string]any{
			"lsp": map[string]any{"chat_diagnostics_enabled": false},
		}},
	})
	offReq := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(off))
	offReq.Header.Set("Content-Type", "application/json")
	offW := httptest.NewRecorder()
	h.HandleUpdate(offW, offReq, "test-ext")
	if offW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", offW.Code)
	}
	switched, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got := switched.Manifest.Capabilities.GetLSP(); got.ChatDiagnosticsEnabled == nil || *got.ChatDiagnosticsEnabled {
		t.Error("ChatDiagnosticsEnabled = nil or true, want false (an explicit false must land)")
	}
}

func TestHandleUpdate_LSPConfig_BoolFlags_PartialUpdate(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	falseVal := false
	ext.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{
		Command:                "gopls",
		Args:                   []string{"serve"},
		FileTypes:              []LSPFileType{{Extension: "go", LanguageID: "go"}},
		Language:               "go",
		EditorEnabled:          &falseVal,
		ToolCallsEnabled:       &falseVal,
		ChatDiagnosticsEnabled: &falseVal,
	}}
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update only editor_enabled (tool_calls_enabled should stay false)
	body := map[string]any{
		"manifest": map[string]any{
			"capabilities": map[string]any{
				"lsp": map[string]any{
					"editor_enabled": true,
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	updated, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	lspCfg := updated.Manifest.Capabilities.GetLSP()
	if lspCfg == nil {
		t.Fatal("LSP config is nil after update")
	}
	// editor_enabled should be true
	if lspCfg.EditorEnabled == nil || !*lspCfg.EditorEnabled {
		t.Error("EditorEnabled should be true after update")
	}
	// tool_calls_enabled should still be false (not in request)
	if lspCfg.ToolCallsEnabled == nil || *lspCfg.ToolCallsEnabled {
		t.Error("ToolCallsEnabled should still be false (not in request)")
	}
	// chat_diagnostics_enabled should still be false (not in request)
	if lspCfg.ChatDiagnosticsEnabled == nil || *lspCfg.ChatDiagnosticsEnabled {
		t.Error("ChatDiagnosticsEnabled should still be false (not in request)")
	}
}

func TestHandleUpdate_LSPConfig_CreateNilLSP(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	// No LSP config initially
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := map[string]any{
		"manifest": map[string]any{
			"capabilities": map[string]any{
				"lsp": map[string]any{
					"command":  "my-lsp",
					"language": "mylang",
					"file_types": []any{
						map[string]any{"extension": "mylang", "language_id": "mylang"},
					},
					"initParamsOverride": map[string]any{
						"customOption": true,
					},
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	updated, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Manifest.Capabilities.GetLSP() == nil {
		t.Fatal("LSP config is nil after update")
	}
	if updated.Manifest.Capabilities.GetLSP().Command != "my-lsp" {
		t.Errorf("Command = %q, want my-lsp", updated.Manifest.Capabilities.GetLSP().Command)
	}
}

func TestHandleSetEnabled(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := map[string]any{
		"enabled": false,
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/extensions/test-ext/enabled", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleSetEnabled(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	// Verify disabled
	got, _ := h.store.GetByID("test-ext")
	if got.Enabled {
		t.Error("Enabled = true, want false")
	}
}

func TestHandleRegistryIndex_NoURL(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/extensions/registry", nil)
	w := httptest.NewRecorder()

	h.HandleRegistryIndex(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleLocalExtensions(t *testing.T) {
	// Build a temp extensions dir with manifests for an LSP and a UI extension.
	extDir := t.TempDir()
	writeManifest := func(rel, content string) {
		t.Helper()
		p := filepath.Join(extDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}
	writeManifest("sveltejs/svelte-language-server/manifest.json", `{
		"display_name": "Svelte Language Server",
		"description": "Svelte language server",
		"author": "Svelte",
		"version": "1.0.0",
		"capabilities": {"lsp": {"command": "svelte-language-server", "args": ["--stdio"], "language": "svelte", "file_types": [{"extension": "svelte", "language_id": "svelte"}]}}
	}`)
	writeManifest("softwarerenderer/markdown-preview/manifest.json", `{
		"display_name": "Markdown Preview",
		"description": "Preview markdown",
		"author": "SoftwareRenderer",
		"version": "1.0.0",
		"capabilities": {"ui": {"injection_points": ["task-card"]}}
	}`)
	// A malformed manifest should be skipped, not fail the scan.
	writeManifest("bad/broken/manifest.json", `{not valid json`)

	h := testHandler(t)
	h.config.ExtensionsDir = extDir

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/local", nil)
	w := httptest.NewRecorder()

	h.HandleLocalExtensions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result RegistryIndex
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("result count = %d, want 2", len(result))
	}

	// Sort is by author then name: softwarerenderer/markdown-preview comes
	// before sveltejs/svelte-language-server.
	if result[0].Name != "markdown-preview" || result[0].Type != ExtensionTypeUI {
		t.Errorf("result[0] = %+v, want markdown-preview/ui", result[0])
	}
	if result[0].Author != "softwarerenderer" {
		t.Errorf("result[0].Author = %q, want softwarerenderer (on-disk namespace)", result[0].Author)
	}
	if result[0].DisplayName != "Markdown Preview" {
		t.Errorf("result[0].DisplayName = %q, want Markdown Preview", result[0].DisplayName)
	}
	want0 := filepath.Join(extDir, "softwarerenderer", "markdown-preview")
	if result[0].LocalPath != want0 {
		t.Errorf("result[0].LocalPath = %q, want %q", result[0].LocalPath, want0)
	}
	// Local (unpublished) entries are never featured — the flag only exists on
	// published registry entries (hand-set in registry.json).
	if result[0].Featured {
		t.Errorf("result[0].Featured = true, want false (local entries are never featured)")
	}

	if result[1].Name != "svelte-language-server" || result[1].Type != ExtensionTypeLSP {
		t.Errorf("result[1] = %+v, want svelte-language-server/lsp", result[1])
	}
	if result[1].Author != "sveltejs" {
		t.Errorf("result[1].Author = %q, want sveltejs (on-disk namespace)", result[1].Author)
	}
	if result[1].DisplayName != "Svelte Language Server" {
		t.Errorf("result[1].DisplayName = %q, want Svelte Language Server", result[1].DisplayName)
	}
	if result[1].Capabilities == nil || result[1].Capabilities.GetLSP() == nil {
		t.Error("result[1].Capabilities.GetLSP() = nil, want LSP config")
	}
	if result[1].Featured {
		t.Errorf("result[1].Featured = true, want false (local entries are never featured)")
	}
}

func TestHandleLocalExtensions_EmptyDir(t *testing.T) {
	h := testHandler(t)
	h.config.ExtensionsDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/local", nil)
	w := httptest.NewRecorder()

	h.HandleLocalExtensions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result RegistryIndex
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("result count = %d, want 0", len(result))
	}
}

func TestHandleRegistryIndex_WithMock(t *testing.T) {
	// Create mock registry server
	index := RegistryIndex{
		{
			Name:        "gopls",
			Version:     "1.0.0",
			Type:        ExtensionTypeLSP,
			DisplayName: "Go Language Server",
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/registry", nil)
	w := httptest.NewRecorder()

	h.HandleRegistryIndex(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result RegistryIndex
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("result count = %d, want 1", len(result))
	}
	if result[0].Name != "gopls" {
		t.Errorf("Name = %q, want gopls", result[0].Name)
	}
}

func TestHandleInstall_NotFound(t *testing.T) {
	// Create mock registry server with empty index
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	body := map[string]any{
		"name": "nonexistent",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleInstall(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleInstall_Success(t *testing.T) {
	// Create mock registry server
	index := RegistryIndex{
		{
			Name:        "gopls",
			Version:     "1.0.0",
			Type:        ExtensionTypeLSP,
			DisplayName: "Go Language Server",
			Description: "Go language server",
			Author:      "Go Team",
			// An lsp entry with no image and no Dockerfile is refused (its own test); this one declares
			// a pre-built image so it can reach the SSE result.
			Image: "docker.io/library/gopls:1.5.0",
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	body := map[string]any{
		"name": "gopls",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleInstall(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (SSE stream)", w.Code)
	}

	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 {
		t.Fatal("expected at least a done SSE event")
	}
	last := events[len(events)-1]
	if !last.Done {
		t.Fatal("expected final done event")
	}
	if last.Error != "" {
		t.Fatalf("unexpected error on done event: %s", last.Error)
	}
	result := last.Result
	if result == nil {
		t.Fatal("expected result on done event")
	}
	if result.Name != "gopls" {
		t.Errorf("Name = %q, want gopls", result.Name)
	}
	if result.InstallSource != "registry" {
		t.Errorf("InstallSource = %q, want registry", result.InstallSource)
	}
	if result.ImageRef != "docker.io/library/gopls:1.5.0" {
		t.Errorf("ImageRef = %q, want the image the entry declared", result.ImageRef)
	}
}

func TestHandleInstallLocal(t *testing.T) {
	h := testHandler(t)
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	// A local language server: manifest plus the Dockerfile its image builds from. Podman is stood in
	// for; this test is about the record the install leaves behind.
	extPath := filepath.Join(extDir, "sveltejs", "svelte-language-server")
	if err := os.MkdirAll(extPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifest := `{
		"display_name": "Svelte Language Server",
		"description": "Svelte language server",
		"author": "Svelte",
		"version": "1.0.0",
		"capabilities": {"lsp": {"command": "svelte-language-server", "args": ["--stdio"], "language": "svelte", "file_types": [{"extension": "svelte", "language_id": "svelte"}]}}
	}`
	if err := os.WriteFile(filepath.Join(extPath, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extPath, "Dockerfile"),
		[]byte("FROM alpine:latest\nCOPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge\n"), 0o644); err != nil {
		t.Fatalf("WriteFile Dockerfile: %v", err)
	}
	fake := fakePodmanOK(t)

	body := map[string]any{"author": "sveltejs", "name": "svelte-language-server"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install-local", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleInstallLocal(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SSE stream)", w.Code)
	}

	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 {
		t.Fatal("expected at least a done SSE event")
	}
	last := events[len(events)-1]
	if !last.Done {
		t.Fatal("expected final done event")
	}
	if last.Error != "" {
		t.Fatalf("unexpected error on done event: %s", last.Error)
	}
	if last.Result == nil {
		t.Fatal("expected result on done event")
	}
	if last.Result.Name != "svelte-language-server" {
		t.Errorf("Name = %q, want svelte-language-server", last.Result.Name)
	}
	if last.Result.InstallSource != "local" {
		t.Errorf("InstallSource = %q, want local", last.Result.InstallSource)
	}
	wantPath := filepath.Join(extDir, "sveltejs", "svelte-language-server")
	if last.Result.LocalPath != wantPath {
		t.Errorf("LocalPath = %q, want %q", last.Result.LocalPath, wantPath)
	}
	if last.Result.Type != ExtensionTypeLSP {
		t.Errorf("Type = %q, want lsp", last.Result.Type)
	}

	// The extension record should be persisted with the local install source.
	stored, err := h.store.GetByID(last.Result.ID)
	if err != nil {
		t.Fatalf("GetByID(%s): %v", last.Result.ID, err)
	}
	if stored.InstallSource != "local" {
		t.Errorf("stored InstallSource = %q, want local", stored.InstallSource)
	}
	// The record carries the local build of that Dockerfile, not an empty image.
	wantImage := ExtensionImageRef("svelte-language-server", "1.0.0")
	if stored.ImageRef != wantImage {
		t.Errorf("stored ImageRef = %q, want %q", stored.ImageRef, wantImage)
	}
	if stored.ContainerConfig.Image != wantImage {
		t.Errorf("stored ContainerConfig.Image = %q, want %q", stored.ContainerConfig.Image, wantImage)
	}
	if calls := fake.calls(t); len(calls) != 1 || calls[0][0] != "build" {
		t.Errorf("podman calls = %v, want one build", calls)
	}
}

// A local install is materialized in place, so the folder the human chose *is* the install, while
// every later reader derives the path from the slugged author. A folder whose name is not that slug
// is refused, and the message names the folder to rename.
func TestHandleInstallLocal_RefusesAFolderNothingCanReadBack(t *testing.T) {
	h := testHandler(t)
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	// Author folder with a capital: the scan records it as-is, the record slugifies it.
	writeManifest(t, extDir, "TestVendor", "theme",
		`{"display_name":"Theme","description":"d","author":"Test Vendor","version":"1.0.0",`+
			`"capabilities":{"css":{"css_url":"theme.css"}}}`)

	bodyBytes, _ := json.Marshal(map[string]any{"author": "TestVendor", "name": "theme"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install-local", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstallLocal(w, req)

	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 {
		t.Fatal("expected a done SSE event")
	}
	last := events[len(events)-1]
	if last.Error == "" {
		t.Fatalf("install was accepted; want a refusal naming the folder to rename (result %+v)", last.Result)
	}
	if !strings.Contains(last.Error, "TestVendor") || !strings.Contains(last.Error, "testvendor") {
		t.Errorf("error does not name both the folder and the slug: %q", last.Error)
	}
	// And nothing was stored: a refused install leaves no row to repair or delete.
	if stored, _ := h.store.GetByAuthorName("testvendor", "theme"); stored != nil {
		t.Errorf("refused install left a row behind: %s", stored.ID)
	}
}

// The other shape of "the folder is not the slug": no author folder at all. A
// manifest directly under the extensions root is scanned as author `local` and is
// legitimately served from `<extensions>/<name>` — the case a client-side guess
// from the manifest author cannot know, and so must still install.
func TestHandleInstallLocal_AcceptsAFlatPackAndReportsWhereItIsServed(t *testing.T) {
	h := testHandler(t)
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	packDir := writeManifest(t, extDir, "", "pack-flat",
		`{"display_name":"Flat Pack","description":"d","author":"Some Vendor","version":"1.0.0",`+
			`"capabilities":{"i18n":{"locale":"fr","messages_url":"messages.json"}}}`)
	if err := os.WriteFile(filepath.Join(packDir, "messages.json"), []byte(`{"a":"b"}`), 0o644); err != nil {
		t.Fatalf("WriteFile catalog: %v", err)
	}

	bodyBytes, _ := json.Marshal(map[string]any{"author": "local", "name": "pack-flat"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install-local", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstallLocal(w, req)

	events := readSSEEvents(t, w.Body.Bytes())
	last := events[len(events)-1]
	if last.Error != "" {
		t.Fatalf("flat install failed: %s", last.Error)
	}
	if last.Result == nil {
		t.Fatal("expected a result")
	}
	if last.Result.ServedURL != "/extensions/pack-flat" {
		t.Errorf("served_url = %q, want /extensions/pack-flat", last.Result.ServedURL)
	}
	// The manifest's own author says otherwise; the served path is the truth.
	if got := last.Result.Manifest.Author; got == "local" {
		t.Errorf("manifest author should stay the author's own string, got %q", got)
	}
}

func TestHandleInstallLocal_NotFound(t *testing.T) {
	// No local extension with this name => 404, regardless of registry.
	h := testHandler(t)
	h.config.ExtensionsDir = t.TempDir()

	body := map[string]any{"author": "sveltejs", "name": "does-not-exist"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install-local", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleInstallLocal(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleInstall_GitPinned(t *testing.T) {
	// A self-hosted extension: a git repo with a manifest (no Dockerfile, so no
	// image build is attempted).
	repo, commit := makeGitRepo(t, map[string]string{
		"github-importer/manifest.json": `{"display_name":"GitHub Importer","description":"d","author":"a","version":"1.0.0","capabilities":{"ui":{"injection_points":["createTask"]}}}`,
	})
	index := RegistryIndex{
		{
			Name:        "github-issue-importer",
			Version:     "1.0.0",
			Type:        ExtensionTypeUI,
			DisplayName: "GitHub Issue Importer",
			Author:      "Software Renderer",
			Repo:        repo,
			Commit:      commit,
			Subdir:      "github-importer",
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	body, _ := json.Marshal(map[string]any{"name": "github-issue-importer"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstall(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SSE stream) (%s)", w.Code, w.Body.String())
	}

	// Files must be materialized from the verified checkout into
	// ./extensions/<author>/<name>.
	if _, err := os.Stat(filepath.Join(extDir, "software-renderer", "github-issue-importer", "manifest.json")); err != nil {
		t.Errorf("manifest not materialized from checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(extDir, "ui", "github-issue-importer")); err == nil {
		t.Error("extension materialized under old <type>/<name> layout")
	}

	// Extension must record the pinned commit (in the final SSE result event).
	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 || !events[len(events)-1].Done {
		t.Fatalf("expected final done SSE event, got %d events", len(events))
	}
	result := events[len(events)-1].Result
	if result == nil {
		t.Fatal("expected result on done event")
	}
	if result.Repo != repo || result.Commit != commit {
		t.Errorf("git source not recorded: repo=%q commit=%q", result.Repo, result.Commit)
	}
	if result.Manifest.DisplayName != "GitHub Importer" {
		t.Errorf("DisplayName = %q, want manifest from checkout", result.Manifest.DisplayName)
	}
}

func TestHandleInstall_GitPinned_FailsOnBadCommit(t *testing.T) {
	repo, _ := makeGitRepo(t, map[string]string{
		"manifest.json": `{"display_name":"X"}`,
	})
	index := RegistryIndex{
		{
			Name:    "evil",
			Version: "1.0.0",
			Type:    ExtensionTypeUI,
			Repo:    repo,
			Author:  "softwarerenderer",
			// A valid-format SHA-256 that is not reachable — install must be refused.
			Commit: strings.Repeat("deadbeef", 8),
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	body, _ := json.Marshal(map[string]any{"name": "evil"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstall(w, req)

	// The unverified commit surfaces as an SSE error event (status 200 stream).
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SSE stream with error event)", w.Code)
	}
	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 || !events[len(events)-1].Done {
		t.Fatalf("expected final done SSE event, got %d events", len(events))
	}
	if events[len(events)-1].Error == "" {
		t.Fatal("expected error event for unverified commit")
	}
	if _, err := os.Stat(filepath.Join(extDir, "softwarerenderer", "evil")); err == nil {
		t.Error("extension dir created despite unverified commit")
	}
	// No extension record should exist.
	exts, _ := h.store.List()
	if len(exts) != 0 {
		t.Errorf("expected no installed extensions, got %d", len(exts))
	}
}

// setupChangelogRegistry writes a registry index (as file:// registry.json) and
// optionally a published changelog file at <base>/<author-slug>/<name>.
func setupChangelogRegistry(t *testing.T, index RegistryIndex, changelog string) string {
	t.Helper()
	base := t.TempDir()
	indexJSON, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "registry.json"), indexJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if changelog != "" {
		entry := index[0]
		dir := filepath.Join(base, authorDir(entry.Author))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.Name), []byte(changelog), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return "file://" + filepath.Join(base, "registry.json")
}

// checkUpdates runs one row against one index and hands back what the panel is told.
func checkUpdates(t *testing.T, h *ExtensionHandler) []ExtensionUpdate {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var result []ExtensionUpdate
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

// registryHandler points the handler at a file:// registry over an index (and optionally the
// published changelog for its first entry).
func registryHandler(t *testing.T, index RegistryIndex, changelog string) *ExtensionHandler {
	t.Helper()
	h := testHandler(t)
	h.config.RegistryURL = setupChangelogRegistry(t, index, changelog)
	return h
}

// What check-updates reports is the whole "Updates Available" panel, and it has two halves with
// different rules. An update is a row whose pin or version the index no longer agrees with — commit
// for a git-pinned row, version for one the registry does not pin, and nothing when both agree. The
// changelog then comes from the commit an update would move to: the entries after the installed hash,
// nothing when no hash matches, and nothing when there is no commit to anchor on — while a missing or
// unreadable changelog never costs the update itself, because that half is what a person acts on.
func TestHandleCheckUpdatesReportsAnUpdateAndItsChangelog(t *testing.T) {
	const installedCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const availableCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const unrelatedCommit = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	dated := func(hash, description string) string {
		return `{"hash":"` + hash + `","version":"1.0.0","description":"` + description + `"}` + "\n"
	}

	tests := []struct {
		name           string
		index          RegistryIndex
		changelog      string
		rowCommit      string
		rowVersion     string
		wantUpdates    int
		wantCurCommit  string
		wantAvCommit   string
		wantCurVersion string
		wantAvVersion  string
		wantChangelog  []string
	}{
		{
			name: "a newer pinned commit, with the entries newer than the installed one",
			index: RegistryIndex{{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP,
				Author: "Go Team", Commit: availableCommit}},
			changelog:   dated(installedCommit, "Initial release") + dated(availableCommit, "Fix hover offsets"),
			rowCommit:   installedCommit,
			wantUpdates: 1, wantCurCommit: installedCommit, wantAvCommit: availableCommit,
			wantChangelog: []string{"Fix hover offsets"},
		},
		{
			name: "the same pinned commit is no update",
			index: RegistryIndex{{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP,
				Author: "Go Team", Commit: installedCommit}},
			rowCommit:   installedCommit,
			wantUpdates: 0,
		},
		{
			name: "a changelog that never names the installed commit shows nothing",
			index: RegistryIndex{{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP,
				Author: "Go Team", Commit: availableCommit}},
			changelog:   dated(unrelatedCommit, "x") + dated(availableCommit, "y"),
			rowCommit:   installedCommit,
			wantUpdates: 1, wantCurCommit: installedCommit, wantAvCommit: availableCommit,
		},
		{
			// The changelog is best-effort: the update it carries is the actionable half, and it
			// would be worse than useless to lose it because a file was never published.
			name: "no published changelog still reports the update",
			index: RegistryIndex{{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP,
				Author: "Go Team", Commit: availableCommit}},
			changelog:   "",
			rowCommit:   installedCommit,
			wantUpdates: 1, wantCurCommit: installedCommit, wantAvCommit: availableCommit,
		},
		{
			name: "a version-only update has no commit to key a changelog on",
			index: RegistryIndex{{Name: "ver-ext", Version: "2.0.0", Type: ExtensionTypeUI,
				Author: "Software Renderer"}},
			changelog:   dated(availableCommit, "should not appear"),
			rowVersion:  "1.0.0",
			wantUpdates: 1, wantCurVersion: "1.0.0", wantAvVersion: "2.0.0",
		},
		{
			name: "the same version is no update",
			index: RegistryIndex{{Name: "ver-ext", Version: "1.0.0", Type: ExtensionTypeUI,
				Author: "Software Renderer"}},
			rowVersion:  "1.0.0",
			wantUpdates: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := tc.index[0]
			h := registryHandler(t, tc.index, tc.changelog)
			ext := makeTestExt(entry.Name, entry.Name, entry.Type)
			if tc.rowVersion != "" {
				ext.Version = tc.rowVersion
			}
			ext.Commit = tc.rowCommit
			if err := h.store.Create(ext); err != nil {
				t.Fatalf("Create: %v", err)
			}

			result := checkUpdates(t, h)
			if len(result) != tc.wantUpdates {
				t.Fatalf("result = %+v, want %d update(s)", result, tc.wantUpdates)
			}
			if tc.wantUpdates == 0 {
				return
			}
			got := result[0]
			// Each rule is checked where it is the one under test: the commit pair for a
			// pinned row, the version pair for a row the index does not pin by commit.
			if tc.wantCurCommit != "" || tc.wantAvCommit != "" {
				if got.CurrentCommit != tc.wantCurCommit || got.AvailableCommit != tc.wantAvCommit {
					t.Errorf("commits = %q → %q, want %q → %q",
						got.CurrentCommit, got.AvailableCommit, tc.wantCurCommit, tc.wantAvCommit)
				}
			}
			if tc.wantCurVersion != "" || tc.wantAvVersion != "" {
				if got.CurrentVersion != tc.wantCurVersion || got.AvailableVersion != tc.wantAvVersion {
					t.Errorf("versions = %q → %q, want %q → %q",
						got.CurrentVersion, got.AvailableVersion, tc.wantCurVersion, tc.wantAvVersion)
				}
			}
			var descs []string
			for _, c := range got.Changelog {
				descs = append(descs, c.Description)
			}
			if len(descs) != len(tc.wantChangelog) {
				t.Fatalf("changelog = %v, want %v", descs, tc.wantChangelog)
			}
			for i, want := range tc.wantChangelog {
				if descs[i] != want {
					t.Errorf("changelog[%d] = %q, want %q", i, descs[i], want)
				}
			}
		})
	}
}

func TestHandleCheckUpdates_NewPermissions(t *testing.T) {
	// Registry version requests more UI permissions than the installed version.
	index := RegistryIndex{
		{
			Name:    "preview",
			Version: "2.0.0",
			Type:    ExtensionTypeUI,
			Capabilities: &Capabilities{
				UI: &UIConfig{
					Permissions: []string{
						"editor.getContent", // already installed
						"editor.setContent", // new
						"editor.save",       // new
					},
				},
			},
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	ext := makeTestExt("preview", "preview", ExtensionTypeUI)
	ext.Version = "1.0.0"
	ext.Manifest.Capabilities = &Capabilities{
		UI: &UIConfig{Permissions: []string{"editor.getContent"}},
	}
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result []ExtensionUpdate
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result count = %d, want 1", len(result))
	}
	got := result[0].NewPermissions
	want := []string{"editor.setContent", "editor.save"}
	if len(got) != len(want) {
		t.Fatalf("new_permissions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("new_permissions[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Adding a host grants no new UI permission, so check-updates reports the host list on its own.
// Dropping one is not reported.
func TestHandleCheckUpdates_DomainsAdded(t *testing.T) {
	const installedCommit = "1111111111111111111111111111111111111111"
	const vettedCommit = "2222222222222222222222222222222222222222"

	index := RegistryIndex{
		{
			Name: "web-search", Version: "2.0.0", Type: ExtensionTypeUI,
			Repo: "https://example.invalid/web-search.git", Commit: vettedCommit,
			// Gains a host.
			Capabilities: &Capabilities{
				Network: &NetworkConfig{AllowedDomains: []string{"search.example.com", "telemetry.example.net"}},
			},
		},
		{
			Name: "notes", Version: "2.0.0", Type: ExtensionTypeUI,
			Repo: "https://example.invalid/notes.git", Commit: vettedCommit,
			// Loses one.
			Capabilities: &Capabilities{
				Network: &NetworkConfig{AllowedDomains: []string{"notes.example.com"}},
			},
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	grew := makeTestExt("search-1", "web-search", ExtensionTypeUI)
	grew.Version = "1.0.0"
	grew.Commit = installedCommit
	grew.InstallSource = "registry"
	grew.Manifest.Capabilities = &Capabilities{
		Network: &NetworkConfig{AllowedDomains: []string{"search.example.com"}},
	}
	_ = h.store.Create(grew)

	shrank := makeTestExt("notes-1", "notes", ExtensionTypeUI)
	shrank.Version = "1.0.0"
	shrank.Commit = installedCommit
	shrank.InstallSource = "registry"
	shrank.Manifest.Capabilities = &Capabilities{
		Network: &NetworkConfig{AllowedDomains: []string{"notes.example.com", "legacy.example.com"}},
	}
	_ = h.store.Create(shrank)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	var result []ExtensionUpdate
	if err := json.UnmarshalRead(w.Body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := make(map[string]ExtensionUpdate, len(result))
	for _, u := range result {
		byName[u.Name] = u
	}

	grewUpdate, ok := byName["web-search"]
	if !ok {
		t.Fatal("web-search update missing")
	}
	if !grewUpdate.DomainsAdded {
		t.Error("domains_added = false, want true for a host the installed manifest never named")
	}
	if len(grewUpdate.NewPermissions) != 0 {
		t.Errorf("new_permissions = %v, want none: this is the case the permission diff cannot see", grewUpdate.NewPermissions)
	}
	if shrankUpdate, ok := byName["notes"]; !ok {
		t.Fatal("notes update missing")
	} else if shrankUpdate.DomainsAdded {
		t.Error("domains_added = true for an update that only removes a host, want false")
	}
}

func TestHandleInstall_FallbackCopiesLicenseURL(t *testing.T) {
	// Registry entry with no git source (no repo/commit) — fallback path should copy LicenseURL
	index := RegistryIndex{
		{
			Name:        "gopls",
			Version:     "1.0.0",
			Type:        ExtensionTypeLSP,
			DisplayName: "Go Language Server",
			Description: "Go language server",
			Author:      "Go Team",
			LicenseURL:  "https://github.com/golang/tools/blob/master/LICENSE",
			Image:       "docker.io/library/gopls:1.5.0",
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	body := map[string]any{
		"name": "gopls",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleInstall(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SSE stream)", w.Code)
	}

	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 || !events[len(events)-1].Done {
		t.Fatalf("expected final done SSE event, got %d events", len(events))
	}
	result := events[len(events)-1].Result
	if result == nil {
		t.Fatal("expected result on done event")
	}
	if result.Manifest.LicenseURL != "https://github.com/golang/tools/blob/master/LICENSE" {
		t.Errorf("Manifest.LicenseURL = %q, want license URL from registry entry", result.Manifest.LicenseURL)
	}
}

// A name that is already installed is refused, whatever shape the stored id has: ids carry an
// install timestamp, so a guard that compared ids would let the same extension install twice and
// leave two rows pointing at one directory.
func TestHandleInstall_AlreadyInstalled(t *testing.T) {
	index := RegistryIndex{
		{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP, DisplayName: "Go Language Server"},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	for _, id := range []string{"gopls", "gopls-9999999999"} {
		t.Run("stored id "+id, func(t *testing.T) {
			h := testHandler(t)
			h.config.RegistryURL = mockServer.URL
			if err := h.store.Create(makeTestExt(id, "gopls", ExtensionTypeLSP)); err != nil {
				t.Fatalf("Create: %v", err)
			}

			body, _ := json.Marshal(map[string]any{"name": "gopls"})
			req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.HandleInstall(w, req)

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", w.Code)
			}
			exts, _ := h.store.List()
			if len(exts) != 1 {
				t.Errorf("installed rows = %d, want the one row untouched", len(exts))
			}
		})
	}
}

func TestHandleInstall_DuplicateInFlight(t *testing.T) {
	index := RegistryIndex{
		{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP, DisplayName: "Go Language Server"},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	// Simulate an in-flight install (e.g. from a closed page).
	if !h.inFlightOps.begin("gopls") {
		t.Fatal("failed to mark gopls in-flight for test")
	}
	defer h.inFlightOps.end("gopls")

	body, _ := json.Marshal(map[string]any{"name": "gopls"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstall(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (duplicate in-flight install)", w.Code)
	}
}

func TestHandleUpdateImage_StreamsProgress(t *testing.T) {
	// Git-pinned with no Dockerfile, so no podman build is needed.
	repo, commit := makeGitRepo(t, map[string]string{
		"importer/manifest.json": `{"display_name":"GitHub Importer","description":"d","author":"a","version":"1.0.0","capabilities":{"ui":{"injection_points":["createTask"]}}}`,
	})
	index := RegistryIndex{
		{
			Name:    "github-issue-importer",
			Version: "1.0.0",
			Type:    ExtensionTypeUI,
			Author:  "Software Renderer",
			Repo:    repo,
			Commit:  commit,
			Subdir:  "importer",
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL
	h.config.ExtensionsDir = t.TempDir()

	ext := makeTestExt("importer-1", "github-issue-importer", ExtensionTypeUI)
	ext.InstallSource = "registry"
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodPost, "/api/extensions/"+ext.ID+"/update", nil)
	w := httptest.NewRecorder()
	h.HandleUpdateImage(w, req, ext.ID)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SSE stream)", w.Code)
	}
	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) < 2 {
		t.Fatalf("expected progress + done events, got %d", len(events))
	}
	last := events[len(events)-1]
	if !last.Done {
		t.Fatal("expected final done event")
	}
	if last.Error != "" {
		t.Fatalf("unexpected error on done event: %s", last.Error)
	}
	if last.Result == nil || last.Result.Name != "github-issue-importer" {
		t.Fatal("expected result on done event")
	}
	// The guard is released by the worker's deferred call, which runs just after the last
	// SSE event - so poll briefly rather than asserting on a race.
	deadline := time.Now().Add(2 * time.Second)
	for guardHolds(h.inFlightOps, "github-issue-importer") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if guardHolds(h.inFlightOps, "github-issue-importer") {
		t.Error("in-flight guard not released after update completed")
	}
}

// newHostUpdateFixture installs a UI extension whose vetted update names one host the installed
// manifest does not.
func newHostUpdateFixture(t *testing.T) (*ExtensionHandler, *Extension, string) {
	t.Helper()
	repo, commit := makeGitRepo(t, map[string]string{
		"search/manifest.json": `{"display_name":"Web Search","description":"d","author":"tester","version":"2.0.0",` +
			`"capabilities":{"network":{"allowed_domains":["search.example.com","telemetry.example.net"]}}}`,
	})
	index := RegistryIndex{{
		Name: "web-search", Version: "2.0.0", Type: ExtensionTypeUI, Author: "tester",
		Repo: repo, Commit: commit, Subdir: "search",
		Capabilities: &Capabilities{
			Network: &NetworkConfig{AllowedDomains: []string{"search.example.com", "telemetry.example.net"}},
		},
	}}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	t.Cleanup(mockServer.Close)

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	ext := makeTestExt("search-1", "web-search", ExtensionTypeUI)
	ext.Version = "1.0.0"
	ext.Commit = "1111111111111111111111111111111111111111"
	ext.InstallSource = "registry"
	ext.Manifest.Capabilities = &Capabilities{
		Network: &NetworkConfig{AllowedDomains: []string{"search.example.com"}},
	}
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("install fixture: %v", err)
	}
	return h, ext, extDir
}

func postUpdate(t *testing.T, h *ExtensionHandler, id, body string) sseEvent {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/"+id+"/update", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleUpdateImage(w, req, id)
	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 {
		t.Fatal("update answered with no SSE event")
	}
	return events[len(events)-1]
}

func TestHandleUpdateImage_NewHostRefused(t *testing.T) {
	h, ext, extDir := newHostUpdateFixture(t)

	last := postUpdate(t, h, ext.ID, `{}`)
	if !last.Done || last.Error == "" {
		t.Fatalf("last event = %+v, want a done event carrying a refusal", last)
	}
	if !strings.Contains(last.Error, "telemetry.example.net") {
		t.Errorf("refusal does not name the host it refuses: %q", last.Error)
	}

	if entries, err := os.ReadDir(extDir); err != nil {
		t.Fatalf("read extensions dir: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("refused update wrote %d file entr(ies) into the extensions dir, want none", len(entries))
	}

	stored, err := h.store.GetByID(ext.ID)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if stored.Commit != ext.Commit {
		t.Errorf("commit = %q, want the installed %q", stored.Commit, ext.Commit)
	}
	if got := stored.Manifest.Capabilities.GetNetwork().AllowedDomains; len(got) != 1 || got[0] != "search.example.com" {
		t.Errorf("stored allowed_domains = %v, want the installed list unchanged", got)
	}
}

// The 400 for a bad body comes before the update is marked in progress, so a refused request cannot
// block every later update.
func TestHandleUpdateImage_MalformedBody(t *testing.T) {
	h, ext, _ := newHostUpdateFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/extensions/"+ext.ID+"/update",
		strings.NewReader(`{"acknowledge_new_domains":"yes"}`))
	w := httptest.NewRecorder()
	h.HandleUpdateImage(w, req, ext.ID)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "acknowledge_new_domains") {
		t.Errorf("refusal does not name the field: %s", w.Body.String())
	}
	if guardHolds(h.inFlightOps, ext.Name) {
		t.Error("the in-flight guard was taken for a request that was refused")
	}
}

// A body that stops half way is a bad request, and a body that never arrived is not. The reader cannot
// tell them apart — both arrive as io.ErrUnexpectedEOF — so the handler decides from the bytes.
func TestHandleUpdateImage_TruncatedBodyRefusedEmptyBodyAccepted(t *testing.T) {
	h, ext, _ := newHostUpdateFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/extensions/"+ext.ID+"/update",
		strings.NewReader(`{"acknowledge_new_domains":tr`))
	w := httptest.NewRecorder()
	h.HandleUpdateImage(w, req, ext.ID)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a half-written body got %d, want 400: %s", w.Code, w.Body.String())
	}
	if guardHolds(h.inFlightOps, ext.Name) {
		t.Error("the in-flight guard was taken for a request that was refused")
	}

	// No body at all is an ordinary update that sent no acknowledgment, which is what `{}` means too.
	last := postUpdate(t, h, ext.ID, "")
	if !last.Done || last.Error == "" {
		t.Errorf("an empty body stopped meaning 'no acknowledgment': %+v", last)
	}
}

func TestHandleUpdateImage_NewHostAcknowledged(t *testing.T) {
	h, ext, _ := newHostUpdateFixture(t)

	last := postUpdate(t, h, ext.ID, `{"acknowledge_new_domains":true}`)
	if !last.Done || last.Error != "" {
		t.Fatalf("last event = %+v, want a clean completion", last)
	}

	stored, err := h.store.GetByID(ext.ID)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	if stored.Commit == ext.Commit {
		t.Fatal("commit unchanged: the acknowledged update did not land")
	}
	if !strings.Contains(strings.Join(stored.Manifest.Capabilities.GetNetwork().AllowedDomains, ","), "telemetry.example.net") {
		t.Errorf("stored allowed_domains = %v, want the updated manifest", stored.Manifest.Capabilities.GetNetwork().AllowedDomains)
	}
}

func TestHandleCheckUpdates_NoURL(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()

	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleStorage_CRUD(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeUI)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("create: %v", err)
	}

	// PUT
	body := bytes.NewBufferString(`{"namespace":"p:proj1/t:t1","key":"tags","value":["a","b"]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext/storage", body)
	w := httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	// GET single
	req = httptest.NewRequest(http.MethodGet, "/api/extensions/test-ext/storage?namespace=p:proj1/t:t1&key=tags", nil)
	w = httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", w.Code)
	}
	var got KVEntry
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Key != "tags" || string(got.Value) != `["a","b"]` {
		t.Fatalf("unexpected entry: %+v", got)
	}

	// GET missing -> 404
	req = httptest.NewRequest(http.MethodGet, "/api/extensions/test-ext/storage?namespace=p:proj1/t:t1&key=missing", nil)
	w = httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", w.Code)
	}

	// DELETE
	req = httptest.NewRequest(http.MethodDelete, "/api/extensions/test-ext/storage?namespace=p:proj1/t:t1&key=tags", nil)
	w = httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", w.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/extensions/test-ext/storage?namespace=p:proj1/t:t1&key=tags", nil)
	w = httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code != http.StatusNotFound {
		t.Fatalf("post-delete status = %d, want 404", w.Code)
	}
}

func TestHandleStorage_List(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeUI)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("create: %v", err)
	}
	h.store.StorageSet("test-ext", "ns", "abc", jsontext.Value(`1`))
	h.store.StorageSet("test-ext", "ns", "abd", jsontext.Value(`2`))
	h.store.StorageSet("test-ext", "ns", "x", jsontext.Value(`3`))

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/test-ext/storage?namespace=ns&prefix=ab", nil)
	w := httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", w.Code)
	}
	var got []KVEntry
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
}

func TestHandleStorage_RejectsOversizedValue(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeUI)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("create: %v", err)
	}
	big := strings.Repeat("a", maxExtensionValueBytes+1)
	body := bytes.NewBufferString(`{"namespace":"ns","key":"k","value":"` + big + `"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext/storage", body)
	w := httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleStorage_UnknownExtension(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/extensions/nope/storage", nil)
	w := httptest.NewRecorder()
	h.HandleStorage(w, req, "nope")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleDelete_CleansStorage(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeUI)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("create: %v", err)
	}
	h.store.StorageSet("test-ext", "p:proj1", "tags", jsontext.Value(`["a"]`))
	h.store.StorageSet("test-ext", "global", "prefs", jsontext.Value(`{"x":1}`))

	req := httptest.NewRequest(http.MethodDelete, "/api/extensions/test-ext", nil)
	w := httptest.NewRecorder()
	h.HandleDelete(w, req, "test-ext")
	if w.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	// Extension gone.
	if _, err := h.store.GetByID("test-ext"); err == nil {
		t.Fatal("extension should be deleted")
	}
	// Storage gone too — no orphaned data.
	entries, err := h.store.StorageList("test-ext", "p:proj1", "")
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected storage cleaned, got %d entries (%v)", len(entries), err)
	}
	entries, _ = h.store.StorageList("test-ext", "global", "")
	if len(entries) != 0 {
		t.Fatalf("expected global storage cleaned, got %d", len(entries))
	}
}

func makeProxyExt(t *testing.T, h *ExtensionHandler, domains []string) {
	t.Helper()
	ext := makeTestExt("proxy-ext", "proxy", ExtensionTypeUI)
	ext.Manifest.Capabilities = &Capabilities{
		Network: &NetworkConfig{AllowedDomains: domains},
	}
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create proxy ext: %v", err)
	}
}

func TestHandleProxy_RejectsNonHTTPScheme(t *testing.T) {
	h := testHandler(t)
	makeProxyExt(t, h, []string{"example.com"})

	body := `{"url":"gopher://example.com:8080/x","method":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/proxy-ext/proxy", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleProxy(w, req, "proxy-ext")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (non-http scheme rejected)", w.Code)
	}
}

func TestHandleProxy_DoesNotFollowRedirects(t *testing.T) {
	allowLoopbackProxy(t)
	// The "internal" server must never be reached via a redirect from an
	// allowed domain (SSRF via redirect).
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("INTERNAL-SECRET"))
	}))
	defer internal.Close()

	// Allowed domain that 302-redirects to the internal server.
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer allowed.Close()

	u, _ := url.Parse(allowed.URL)
	h := testHandler(t)
	makeProxyExt(t, h, []string{u.Hostname()})

	body := `{"url":"` + allowed.URL + `/probe","method":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/proxy-ext/proxy", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.HandleProxy(w, req, "proxy-ext")

	// The proxy should return the redirect response itself, not follow it.
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d (redirect must not be followed)", w.Code, http.StatusFound)
	}
	if strings.Contains(w.Body.String(), "INTERNAL-SECRET") {
		t.Fatal("proxy followed a redirect to a non-allowed host (SSRF via redirect)")
	}
}

// putUpdate sends a PUT /api/extensions/{id} body and returns the recorder.
func putUpdate(t *testing.T, h *ExtensionHandler, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/"+id, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleUpdate(w, req, id)
	return w
}

// TestHandleUpdate_RejectsSandboxWidening: an update must refuse mounts that escape the app's own
// host roots and capabilities that reach past the container, naming the offending value and
// persisting nothing.
func TestHandleUpdate_RejectsSandboxWidening(t *testing.T) {
	mountsBody := func(host, container, mode string) map[string]any {
		return map[string]any{
			"sandbox_config": map[string]any{
				"mounts": []any{map[string]any{"host_path": host, "container_path": container, "mode": mode}},
			},
		}
	}
	capsBody := func(caps ...string) map[string]any {
		return map[string]any{"sandbox_config": map[string]any{"capabilities": caps}}
	}

	cases := []struct {
		name    string
		body    map[string]any
		wantMsg string
	}{
		{"mounting the host root read-write", mountsBody("/", "/host", "rw"), `mount host path "/"`},
		{"mounting outside the allowlist", mountsBody("/etc", "/etc", "rw"), "/etc"},
		{"host path traversal", mountsBody("/tmp/../etc", "/etc", "ro"), "'..'"},
		{"relative host path", mountsBody("etc", "/etc", "ro"), "not absolute"},
		{"container path with ':' (spec rewrite)", mountsBody("/tmp/whatever", "/workspace:rw", "ro"), "/workspace:rw"},
		{"container path with ',' (options rewrite)", mountsBody("/tmp/whatever", "/workspace,rw", "ro"), "/workspace,rw"},
		{"container path traversal", mountsBody("/tmp/whatever", "/mnt/../../etc", "ro"), "'..'"},
		{"capability shaped like a flag", capsBody("--privileged"), "invalid"},
		{"two capabilities in one value", capsBody("SYS_ADMIN ALL"), "invalid"},
		{"capability with a separator", capsBody("SYS_ADMIN,ALL"), "invalid"},
		{"empty capability", capsBody(""), "empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := testHandler(t)
			if err := h.store.Create(makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)); err != nil {
				t.Fatalf("Create: %v", err)
			}

			w := putUpdate(t, h, "test-ext", tc.body)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			var apiErr struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &apiErr); err != nil {
				t.Fatalf("decode error body %q: %v", w.Body.String(), err)
			}
			if !strings.Contains(apiErr.Error, tc.wantMsg) {
				t.Errorf("error %q does not name %q", apiErr.Error, tc.wantMsg)
			}

			// Nothing was persisted — a rejected value must not linger in the DB
			// to be re-read by the next container create.
			got, err := h.store.GetByID("test-ext")
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if len(got.SandboxConfig.Mounts) != 0 {
				t.Errorf("mounts were persisted: %+v", got.SandboxConfig.Mounts)
			}
			if len(got.SandboxConfig.Capabilities) != 0 {
				t.Errorf("capabilities were persisted: %v", got.SandboxConfig.Capabilities)
			}
		})
	}

	// A capability *name* is not what this refuses: the grant lands in a gVisor guest
	// that started from --cap-drop ALL, so SYS_ADMIN is stored like CHOWN. What is stored
	// with it is checked by the container's own posture test
	// (TestBuildExtensionCreateArgs_CapabilityPosture), which pins the runtime pairing the
	// grant assumes.
	t.Run("a plain capability name is accepted and stored", func(t *testing.T) {
		h := testHandler(t)
		if err := h.store.Create(makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		w := putUpdate(t, h, "test-ext", capsBody("SYS_ADMIN", "SYS_PTRACE", "CHOWN"))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
		}
		got, err := h.store.GetByID("test-ext")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if len(got.SandboxConfig.Capabilities) != 3 || got.SandboxConfig.Capabilities[0] != "SYS_ADMIN" {
			t.Errorf("capabilities = %v, want the three requested names as given", got.SandboxConfig.Capabilities)
		}
	})
}

// TestHandleUpdate_AcceptsAllowedSandboxAdditions keeps the legitimate case
// working: a mount inside the app's own roots (the temp dir is allowlisted by
// default) and a harmless capability are stored as requested.
func TestHandleUpdate_AcceptsAllowedSandboxAdditions(t *testing.T) {
	h := testHandler(t)
	if err := h.store.Create(makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	src := filepath.Join(t.TempDir(), "node_modules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	w := putUpdate(t, h, "test-ext", map[string]any{
		"sandbox_config": map[string]any{
			"mounts": []any{map[string]any{
				"host_path": src, "container_path": "/workspace/node_modules", "mode": "ro",
			}},
			"capabilities": []string{"NET_BIND_SERVICE"},
		},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	got, err := h.store.GetByID("test-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(got.SandboxConfig.Mounts) != 1 || got.SandboxConfig.Mounts[0].ContainerPath != "/workspace/node_modules" {
		t.Errorf("mounts = %+v, want the allowed mount stored", got.SandboxConfig.Mounts)
	}
	if len(got.SandboxConfig.Capabilities) != 1 || got.SandboxConfig.Capabilities[0] != "NET_BIND_SERVICE" {
		t.Errorf("capabilities = %v, want NET_BIND_SERVICE stored", got.SandboxConfig.Capabilities)
	}
}

// A language server with no image and no Dockerfile has nothing to run in a sandbox. This installed
// successfully before, and the sandbox then started a default image with no server in it.
func TestHandleInstall_RefusesALanguageServerWithNoImage(t *testing.T) {
	index := RegistryIndex{
		{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP, DisplayName: "Go Language Server"},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	body, _ := json.Marshal(map[string]any{"name": "gopls"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstall(w, req)

	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 || !events[len(events)-1].Done {
		t.Fatalf("expected a final done SSE event, got %d", len(events))
	}
	last := events[len(events)-1]
	if last.Error == "" {
		t.Fatal("install of a language server with no image reported success")
	}
	for _, want := range []string{"language server", "image", "Dockerfile"} {
		if !strings.Contains(last.Error, want) {
			t.Errorf("install error %q does not name %q", last.Error, want)
		}
	}
	if last.Result != nil {
		t.Errorf("a refused install returned a result: %+v", last.Result)
	}
	exts, err := h.store.List()
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	if len(exts) != 0 {
		t.Errorf("%d extensions stored after a refused install, want 0", len(exts))
	}
}

// A failed build installs nothing: no row, and no checked-out directory left behind for
// GET /api/extensions/local to offer as installable under the manifest it shipped with.
func TestHandleInstall_BuildFailureLeavesNoRowAndNoFiles(t *testing.T) {
	repo, commit := makeGitRepo(t, map[string]string{
		"lsp/gopls/manifest.json": `{"display_name":"Go","description":"d","author":"Go Team","version":"0.7.0",` +
			`"capabilities":{"lsp":{"command":"gopls","args":["serve"],"language":"go","file_types":[{"extension":"go","language_id":"go"}]}}}`,
		"lsp/gopls/Dockerfile": "FROM golang:1-alpine\nCOPY sandbox/sandbox-bridge /usr/local/bin/sandbox-bridge\n",
	})
	index := RegistryIndex{{
		Name: "gopls", Version: "0.7.0", Type: ExtensionTypeLSP, DisplayName: "Go Language Server",
		Author: "Go Team", Repo: repo, Commit: commit, Subdir: "lsp/gopls",
	}}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	fake := fakePodmanFails(t, 1, `Error: building at STEP "COPY": no items matching glob`)

	body, _ := json.Marshal(map[string]any{"name": "gopls"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstall(w, req)

	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 || !events[len(events)-1].Done {
		t.Fatalf("expected a final done SSE event, got %d", len(events))
	}
	if msg := events[len(events)-1].Error; !strings.Contains(msg, "failed to build") {
		t.Errorf("install error = %q, want it to report the failed build", msg)
	}
	if calls := fake.calls(t); len(calls) != 1 || calls[0][0] != "build" {
		t.Errorf("podman calls = %v, want one build", calls)
	}
	if exts, err := h.store.List(); err != nil || len(exts) != 0 {
		t.Errorf("stored extensions after a failed build = %v (%v), want none", exts, err)
	}
	if _, err := os.Stat(filepath.Join(extDir, "go-team", "gopls")); !os.IsNotExist(err) {
		t.Errorf("the checkout survived the failed install (stat err = %v) — %s", err, describeTree(t, extDir))
	}
	if _, err := os.Stat(filepath.Join(extDir, "go-team")); !os.IsNotExist(err) {
		t.Errorf("the emptied author folder was left behind (stat err = %v) — %s", err, describeTree(t, extDir))
	}
}

// describeTree renders the tree with mtimes. "A directory was left behind" has three different causes —
// never removed, half removed, or re-created after the removal — and the mtimes separate them: the original
// checkout's stamps mean nothing was deleted, stamps newer than the removal name a second writer.
func describeTree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			fmt.Fprintf(&b, "%s (stat failed: %v) ", strings.TrimPrefix(path, root+string(filepath.Separator)), infoErr)
			return nil
		}
		fmt.Fprintf(&b, "%s mtime=%s ", strings.TrimPrefix(path, root+string(filepath.Separator)), info.ModTime().Format("15:04:05.000"))
		return nil
	})
	if b.Len() == 0 {
		return "the tree is empty"
	}
	return strings.TrimSpace(b.String())
}

// TestHandleStorage_RefusesAnOversizedBodyBeforeReadingIt: the value cap is applied to bytes the store
// has already decoded, so on its own it lets a request make the server hold whatever it sent before
// being told no. The transport bound is what stops that, and the refused write leaves nothing behind.
func TestHandleStorage_RefusesAnOversizedBodyBeforeReadingIt(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeUI)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("create: %v", err)
	}

	body := strings.NewReader(`{"namespace":"ns","key":"huge","value":"` + strings.Repeat("a", storageBodyLimit*2) + `"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext/storage", body)
	w := httptest.NewRecorder()
	h.HandleStorage(w, req, "test-ext")
	if w.Code == http.StatusOK {
		t.Fatal("a body past the transport limit was accepted")
	}
	entry, err := h.store.StorageGet("test-ext", "ns", "huge")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if entry != nil {
		t.Error("the refused write left a value behind")
	}

	// A value inside the limit still round-trips — the bound is not a smaller cap in disguise.
	small := httptest.NewRequest(http.MethodPut, "/api/extensions/test-ext/storage",
		strings.NewReader(`{"namespace":"ns","key":"k","value":"kept"}`))
	smallW := httptest.NewRecorder()
	h.HandleStorage(smallW, small, "test-ext")
	if smallW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", smallW.Code, smallW.Body.String())
	}
	if entry, err := h.store.StorageGet("test-ext", "ns", "k"); err != nil || entry == nil {
		t.Fatalf("a normal value did not round-trip: entry=%v err=%v", entry, err)
	}
}
