package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHandler(t *testing.T) *ExtensionHandler {
	t.Helper()
	store := testStore(t)
	reg := NewRegistryClient()
	cfg := &ExtensionConfig{
		RegistryURL: "",
	}
	sandbox := NewExtensionSandboxManager(reg, store)
	return NewExtensionHandler(store, sandbox, reg, cfg)
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

func TestHandleList_Empty(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/extensions", nil)
	w := httptest.NewRecorder()

	h.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result []ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("result count = %d, want 0", len(result))
	}
}

func TestHandleList_WithExtensions(t *testing.T) {
	h := testHandler(t)
	// Create test extensions
	for _, name := range []string{"gopls", "typescript"} {
		ext := makeTestExt(name, name, ExtensionTypeLSP)
		if err := h.store.Create(ext); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/extensions", nil)
	w := httptest.NewRecorder()

	h.HandleList(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result []ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("result count = %d, want 2", len(result))
	}
	// Verify fields
	if result[0].Name == "" {
		t.Error("result[0].Name is empty")
	}
	if result[0].Type != ExtensionTypeLSP {
		t.Errorf("result[0].Type = %q, want lsp", result[0].Type)
	}
	if !result[0].Enabled {
		t.Error("result[0].Enabled = false, want true")
	}
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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
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

func TestHandleList_WrongMethod(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions", nil)
	w := httptest.NewRecorder()

	h.HandleList(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Name != "test-extension" {
		t.Errorf("Name = %q, want test-extension", result.Name)
	}
}

func TestHandleGet_NotFound(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/extensions/nonexistent", nil)
	w := httptest.NewRecorder()

	h.HandleGet(w, req, "nonexistent")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
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
		Command:    "typescript-language-server",
		Args:       []string{"--stdio"},
		Extensions: []string{"ts", "tsx", "js", "jsx"},
		Language:   "typescript",
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
		Command:    "gopls",
		Args:       []string{"serve"},
		Extensions: []string{"go"},
		Language:   "go",
	}}
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update editor_enabled and tool_calls_enabled
	body := map[string]any{
		"manifest": map[string]any{
			"capabilities": map[string]any{
				"lsp": map[string]any{
					"editor_enabled":     false,
					"tool_calls_enabled": true,
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
}

func TestHandleUpdate_LSPConfig_BoolFlags_PartialUpdate(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	falseVal := false
	ext.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{
		Command:          "gopls",
		Args:             []string{"serve"},
		Extensions:       []string{"go"},
		Language:         "go",
		EditorEnabled:    &falseVal,
		ToolCallsEnabled: &falseVal,
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

func TestHandleDelete(t *testing.T) {
	h := testHandler(t)
	ext := makeTestExt("test-ext", "test-extension", ExtensionTypeLSP)
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/extensions/test-ext", nil)
	w := httptest.NewRecorder()

	h.HandleDelete(w, req, "test-ext")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	// Verify deleted
	_, err := h.store.GetByID("test-ext")
	if err == nil {
		t.Error("extension still exists after delete")
	}
}

func TestHandleDelete_NotFound(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/extensions/nonexistent", nil)
	w := httptest.NewRecorder()

	h.HandleDelete(w, req, "nonexistent")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
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
		"capabilities": {"lsp": {"command": "svelte-language-server", "args": ["--stdio"], "extensions": ["svelte"], "language": "svelte"}}
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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
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
		json.NewEncoder(w).Encode(index)
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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
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
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
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
}

func TestHandleInstallLocal(t *testing.T) {
	h := testHandler(t)
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir

	// A local extension with a manifest but no Dockerfile, so the test does not
	// attempt an actual container image build.
	extPath := filepath.Join(extDir, "sveltejs", "svelte-language-server")
	if err := os.MkdirAll(extPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifest := `{
		"display_name": "Svelte Language Server",
		"description": "Svelte language server",
		"author": "Svelte",
		"version": "1.0.0",
		"capabilities": {"lsp": {"command": "svelte-language-server", "args": ["--stdio"], "extensions": ["svelte"], "language": "svelte"}}
	}`
	if err := os.WriteFile(filepath.Join(extPath, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}

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
	stored, err := h.store.GetByName("svelte-language-server")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if stored.InstallSource != "local" {
		t.Errorf("stored InstallSource = %q, want local", stored.InstallSource)
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
		json.NewEncoder(w).Encode(index)
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
		json.NewEncoder(w).Encode(index)
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

func TestHandleCheckUpdates_CommitChange(t *testing.T) {
	// Curated index pins a newer commit than what is installed.
	index := RegistryIndex{
		{
			Name:    "gopls",
			Version: "1.0.0",
			Type:    ExtensionTypeLSP,
			Commit:  strings.Repeat("b", 64),
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	ext := makeTestExt("gopls", "gopls", ExtensionTypeLSP)
	ext.Commit = strings.Repeat("a", 64)
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result count = %d, want 1", len(result))
	}
	if result[0].CurrentCommit != strings.Repeat("a", 64) || result[0].AvailableCommit != strings.Repeat("b", 64) {
		t.Errorf("commit update not reported: %+v", result[0])
	}
}

func TestHandleCheckUpdates_NoCommitChange(t *testing.T) {
	index := RegistryIndex{
		{
			Name:    "gopls",
			Version: "1.0.0",
			Type:    ExtensionTypeLSP,
			Commit:  strings.Repeat("a", 64),
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	ext := makeTestExt("gopls", "gopls", ExtensionTypeLSP)
	ext.Commit = strings.Repeat("a", 64)
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("result count = %d, want 0 (same commit)", len(result))
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

func TestHandleCheckUpdates_Changelog(t *testing.T) {
	commitA := strings.Repeat("a", 64) // installed
	commitB := strings.Repeat("b", 64) // available
	index := RegistryIndex{{
		Name:    "changelog-ext",
		Version: "1.0.0",
		Type:    ExtensionTypeUI,
		Author:  "Software Renderer",
		Commit:  commitB,
	}}
	changelog := `{"hash":"` + commitA + `","version":"1.0.0","description":"Initial release"}` + "\n" +
		`{"hash":"` + commitB + `","version":"1.0.0","description":"Fix hover offsets"}` + "\n"
	registryURL := setupChangelogRegistry(t, index, changelog)

	h := testHandler(t)
	h.config.RegistryURL = registryURL

	ext := makeTestExt("changelog-ext", "changelog-ext", ExtensionTypeUI)
	ext.Commit = commitA
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result count = %d, want 1", len(result))
	}
	// Only the change newer than the installed hash is reported.
	if len(result[0].Changelog) != 1 {
		t.Fatalf("changelog count = %d, want 1 (%+v)", len(result[0].Changelog), result[0].Changelog)
	}
	if result[0].Changelog[0].Hash != commitB || result[0].Changelog[0].Description != "Fix hover offsets" {
		t.Errorf("changelog[0] = %+v, want the commitB entry", result[0].Changelog[0])
	}
}

func TestHandleCheckUpdates_ChangelogNoMatchingHash(t *testing.T) {
	// Changelog exists but does not contain the installed hash → no changes shown.
	index := RegistryIndex{{
		Name:    "changelog-ext",
		Version: "1.0.0",
		Type:    ExtensionTypeUI,
		Author:  "Software Renderer",
		Commit:  strings.Repeat("b", 64),
	}}
	changelog := `{"hash":"` + strings.Repeat("c", 64) + `","version":"1.0.0","description":"x"}` + "\n" +
		`{"hash":"` + strings.Repeat("b", 64) + `","version":"1.0.0","description":"y"}` + "\n"
	registryURL := setupChangelogRegistry(t, index, changelog)

	h := testHandler(t)
	h.config.RegistryURL = registryURL

	ext := makeTestExt("changelog-ext", "changelog-ext", ExtensionTypeUI)
	ext.Commit = strings.Repeat("a", 64) // absent from the changelog
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result count = %d, want 1", len(result))
	}
	if len(result[0].Changelog) != 0 {
		t.Errorf("changelog = %+v, want empty (no matching hash)", result[0].Changelog)
	}
}

func TestHandleCheckUpdates_ChangelogFetchFails(t *testing.T) {
	// No changelog published → the update is still reported, non-fatal.
	index := RegistryIndex{{
		Name:    "changelog-ext",
		Version: "1.0.0",
		Type:    ExtensionTypeUI,
		Author:  "Software Renderer",
		Commit:  strings.Repeat("b", 64),
	}}
	registryURL := setupChangelogRegistry(t, index, "") // no changelog file

	h := testHandler(t)
	h.config.RegistryURL = registryURL

	ext := makeTestExt("changelog-ext", "changelog-ext", ExtensionTypeUI)
	ext.Commit = strings.Repeat("a", 64)
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result count = %d, want 1 (fetch failure must not block update detection)", len(result))
	}
	if len(result[0].Changelog) != 0 {
		t.Errorf("changelog = %+v, want empty", result[0].Changelog)
	}
}

func TestHandleCheckUpdates_NoChangelogForVersionUpdate(t *testing.T) {
	// Non-git-pinned (version-only) updates have no commit to key on — no
	// changelog is fetched or shown, even if one is published.
	index := RegistryIndex{{
		Name:    "ver-ext",
		Version: "2.0.0",
		Type:    ExtensionTypeUI,
		Author:  "Software Renderer",
	}}
	changelog := `{"hash":"` + strings.Repeat("b", 64) + `","version":"2.0.0","description":"should not appear"}` + "\n"
	registryURL := setupChangelogRegistry(t, index, changelog)

	h := testHandler(t)
	h.config.RegistryURL = registryURL

	ext := makeTestExt("ver-ext", "ver-ext", ExtensionTypeUI)
	ext.Version = "1.0.0" // no Commit — legacy version-pinned install
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()
	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("result count = %d, want 1", len(result))
	}
	if len(result[0].Changelog) != 0 {
		t.Errorf("changelog = %+v, want empty for version-only update", result[0].Changelog)
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
		json.NewEncoder(w).Encode(index)
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
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
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
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
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

func TestHandleInstall_AlreadyInstalled(t *testing.T) {
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
		json.NewEncoder(w).Encode(index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	// Pre-install
	ext := makeTestExt("gopls", "gopls", ExtensionTypeLSP)
	_ = h.store.Create(ext)

	body := map[string]any{
		"name": "gopls",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleInstall(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestHandleInstall_AlreadyInstalled_TimestampedID(t *testing.T) {
	// Reinstall must be rejected even though records use timestamped IDs.
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
		json.NewEncoder(w).Encode(index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	// Pre-install with a timestamped ID (like a real install).
	ext := makeTestExt("gopls-9999999999", "gopls", ExtensionTypeLSP)
	_ = h.store.Create(ext)

	body, _ := json.Marshal(map[string]any{"name": "gopls"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstall(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (duplicate install with timestamped ID)", w.Code)
	}
	exts, _ := h.store.List()
	if len(exts) != 1 {
		t.Errorf("expected exactly one installed extension, got %d", len(exts))
	}
}

func TestHandleInstall_DuplicateInFlight(t *testing.T) {
	index := RegistryIndex{
		{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP, DisplayName: "Go Language Server"},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
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

func TestHandleInstall_CompletesAfterClientDisconnect(t *testing.T) {
	index := RegistryIndex{
		{Name: "gopls", Version: "1.0.0", Type: ExtensionTypeLSP, DisplayName: "Go Language Server"},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	body, _ := json.Marshal(map[string]any{"name": "gopls"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.HandleInstall(w, req)
		close(done)
	}()

	// Cancel the request context to simulate the page closing mid-install.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// handler returned on disconnect
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnect")
	}

	// The install must still complete in the background and write the record.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		exts, _ := h.store.List()
		for _, e := range exts {
			if e.Name == "gopls" {
				return // installed despite client disconnect
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	exts, _ := h.store.List()
	t.Fatalf("extension not installed after client disconnect; store has %d extensions", len(exts))
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
		json.NewEncoder(w).Encode(index)
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
	// The in-flight guard must be released once the background update finishes.
	if h.inFlightOps.busy["github-issue-importer"] {
		t.Error("in-flight guard not released after update completed")
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

func TestHandleCheckUpdates_NoUpdates(t *testing.T) {
	// Create mock registry server with same version
	index := RegistryIndex{
		{
			Name:    "gopls",
			Version: "1.0.0",
			Type:    ExtensionTypeLSP,
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	// Install extension at same version
	ext := makeTestExt("gopls", "gopls", ExtensionTypeLSP)
	ext.Version = "1.0.0"
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()

	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("result count = %d, want 0 (no updates)", len(result))
	}
}

func TestHandleCheckUpdates_UpdateAvailable(t *testing.T) {
	// Create mock registry server with newer version
	index := RegistryIndex{
		{
			Name:    "gopls",
			Version: "2.0.0",
			Type:    ExtensionTypeLSP,
		},
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL

	// Install extension at older version
	ext := makeTestExt("gopls", "gopls", ExtensionTypeLSP)
	ext.Version = "1.0.0"
	_ = h.store.Create(ext)

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/check-updates", nil)
	w := httptest.NewRecorder()

	h.HandleCheckUpdates(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var result []ExtensionUpdate
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("result count = %d, want 1", len(result))
	}
	if result[0].CurrentVersion != "1.0.0" {
		t.Errorf("CurrentVersion = %q, want 1.0.0", result[0].CurrentVersion)
	}
	if result[0].AvailableVersion != "2.0.0" {
		t.Errorf("AvailableVersion = %q, want 2.0.0", result[0].AvailableVersion)
	}
}

func TestHandleSandboxLogs_NotRunning(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/extensions/test-ext/logs", nil)
	w := httptest.NewRecorder()

	h.HandleSandboxLogs(w, req, "test-ext")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestExtensionResponse_MarshalJSON(t *testing.T) {
	resp := ExtensionResponse{
		ID:            "test-ext",
		Name:          "Test Extension",
		Version:       "1.0.0",
		Type:          ExtensionTypeLSP,
		Enabled:       true,
		Running:       false,
		InstallSource: "registry",
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got ExtensionResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Name != "Test Extension" {
		t.Errorf("Name = %q, want Test Extension", got.Name)
	}
	if got.Type != ExtensionTypeLSP {
		t.Errorf("Type = %q, want lsp", got.Type)
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
	h.store.StorageSet("test-ext", "ns", "abc", json.RawMessage(`1`))
	h.store.StorageSet("test-ext", "ns", "abd", json.RawMessage(`2`))
	h.store.StorageSet("test-ext", "ns", "x", json.RawMessage(`3`))

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
	h.store.StorageSet("test-ext", "p:proj1", "tags", json.RawMessage(`["a"]`))
	h.store.StorageSet("test-ext", "global", "prefs", json.RawMessage(`{"x":1}`))

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
