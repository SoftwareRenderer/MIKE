package extensions

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testHandler(t *testing.T) *ExtensionHandler {
	t.Helper()
	store := testStore(t)
	reg := NewRegistryClient()
	cfg := &ExtensionConfig{
		RegistryURL:  "",
		WorkspaceDir: "/tmp/workspace",
	}
	sandbox := NewExtensionSandboxManager(reg, store)
	return NewExtensionHandler(store, sandbox, reg, cfg)
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

func TestHandleCreate(t *testing.T) {
	h := testHandler(t)
	body := map[string]any{
		"name":    "my-lsp",
		"type":    "lsp",
		"version": "1.0.0",
		"manifest": map[string]any{
			"display_name": "My LSP",
			"description":  "A test LSP",
			"author":       "Test",
		},
		"sandbox_config": map[string]any{
			"network_isolation": true,
			"memory_limit":      512,
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}

	var result ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Name != "my-lsp" {
		t.Errorf("Name = %q, want my-lsp", result.Name)
	}
	if result.Type != ExtensionTypeLSP {
		t.Errorf("Type = %q, want lsp", result.Type)
	}
	if result.InstallSource != "manual" {
		t.Errorf("InstallSource = %q, want manual", result.InstallSource)
	}
}

func TestHandleCreate_MissingName(t *testing.T) {
	h := testHandler(t)
	body := map[string]any{
		"type": "lsp",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCreate_InvalidType(t *testing.T) {
	h := testHandler(t)
	body := map[string]any{
		"name": "test",
		"type": "invalid",
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
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
	ext.SandboxConfig.MemoryLimit = 256 // default
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
	ext.Manifest.LSP = &LSPConfig{
		Command:    "typescript-language-server",
		Args:       []string{"--stdio"},
		Extensions: []string{"ts", "tsx", "js", "jsx"},
		Language:   "typescript",
	}
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := map[string]any{
		"manifest": map[string]any{
			"lsp": map[string]any{
				"initParamsOverride": map[string]any{
					"tsserver.path": "/usr/local/lib/node_modules/typescript",
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
	if updated.Manifest.LSP == nil {
		t.Fatal("LSP config is nil after update")
	}
	if updated.Manifest.LSP.InitParamsOverride == nil {
		t.Fatal("InitParamsOverride is nil after update")
	}
	tsserverPath, ok := updated.Manifest.LSP.InitParamsOverride["tsserver.path"]
	if !ok {
		t.Fatal("tsserver.path not found in InitParamsOverride")
	}
	if tsserverPath != "/usr/local/lib/node_modules/typescript" {
		t.Errorf("tsserver.path = %v, want /usr/local/lib/node_modules/typescript", tsserverPath)
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
			"lsp": map[string]any{
				"command":  "my-lsp",
				"language": "mylang",
				"initParamsOverride": map[string]any{
					"customOption": true,
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
	if updated.Manifest.LSP == nil {
		t.Fatal("LSP config is nil after update")
	}
	if updated.Manifest.LSP.Command != "my-lsp" {
		t.Errorf("Command = %q, want my-lsp", updated.Manifest.LSP.Command)
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

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}

	var result ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Name != "gopls" {
		t.Errorf("Name = %q, want gopls", result.Name)
	}
	if result.InstallSource != "registry" {
		t.Errorf("InstallSource = %q, want registry", result.InstallSource)
	}
}

func TestHandleInstall_WithoutManifestURL_CopiesLicenseURL(t *testing.T) {
	// Registry entry without ManifestURL — fallback path should copy LicenseURL
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

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	var result ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
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
