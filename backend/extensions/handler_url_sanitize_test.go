package extensions

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleCreate_SanitizesManifestURLs(t *testing.T) {
	h := testHandler(t)

	// Create extension with javascript: URLs
	reqBody := CreateExtensionRequest{
		Name:  "test-ext-sanitize",
		Type:  ExtensionTypeUI,
		Version: "1.0.0",
		Manifest: ExtensionManifest{
			DisplayName: "Test Extension",
			Homepage:    "javascript:alert(1)",
			LicenseURL:  "data:text/html,<script>alert(1)</script>",
		},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	var resp ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Manifest.Homepage != "" {
		t.Errorf("Homepage = %q, want empty (javascript: should be sanitized)", resp.Manifest.Homepage)
	}
	if resp.Manifest.LicenseURL != "" {
		t.Errorf("LicenseURL = %q, want empty (data: should be sanitized)", resp.Manifest.LicenseURL)
	}
}

func TestHandleCreate_PreservesValidURLs(t *testing.T) {
	h := testHandler(t)

	reqBody := CreateExtensionRequest{
		Name:  "test-ext-valid",
		Type:  ExtensionTypeUI,
		Version: "1.0.0",
		Manifest: ExtensionManifest{
			DisplayName: "Test Extension",
			Homepage:    "https://example.com",
			LicenseURL:  "https://example.com/license",
		},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/api/extensions", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	var resp ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Manifest.Homepage != "https://example.com" {
		t.Errorf("Homepage = %q, want https://example.com", resp.Manifest.Homepage)
	}
	if resp.Manifest.LicenseURL != "https://example.com/license" {
		t.Errorf("LicenseURL = %q, want https://example.com/license", resp.Manifest.LicenseURL)
	}
}

func TestHandleUpdate_SanitizesManifestURLs(t *testing.T) {
	h := testHandler(t)

	// Create a valid extension first
	ext := makeTestExt("ext-sanitize", "test-sanitize", ExtensionTypeUI)
	ext.Manifest.Homepage = "https://safe.com"
	ext.Manifest.LicenseURL = "https://safe.com/license"
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update with malicious URLs
	updateReq := UpdateExtensionRequest{
		Manifest: ExtensionManifest{
			Homepage:   "javascript:alert(document.cookie)",
			LicenseURL: "data:text/html,<img onerror=alert(1) src=x>",
		},
	}
	body, _ := json.Marshal(updateReq)
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/ext-sanitize", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleUpdate(w, req, "ext-sanitize")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp ExtensionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Manifest.Homepage != "" {
		t.Errorf("Homepage = %q, want empty (javascript: should be sanitized)", resp.Manifest.Homepage)
	}
	if resp.Manifest.LicenseURL != "" {
		t.Errorf("LicenseURL = %q, want empty (data: should be sanitized)", resp.Manifest.LicenseURL)
	}
}
