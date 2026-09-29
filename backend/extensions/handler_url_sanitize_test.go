package extensions

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleUpdate_SanitizesManifestURLs(t *testing.T) {
	t.Parallel()
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
