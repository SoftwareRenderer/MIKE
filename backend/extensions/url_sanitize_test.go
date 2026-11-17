package extensions

// The manifest's homepage and license URL reach the browser as the `href` of a link in Settings →
// Extensions, and both arrive as text somebody else wrote — a PUT body, or the manifest.json of a
// checkout. One rule covers them: a URL that is not http(s) is stored as empty, never as the bytes it
// arrived with, because a cleared field is a missing link and any other answer is a click that runs
// script from MIKE's own origin.

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Valid URLs — preserved
		{"https url", "https://example.com", "https://example.com"},
		{"http url", "http://example.com", "http://example.com"},
		{"https with path", "https://example.com/path/to/page", "https://example.com/path/to/page"},
		{"https with query", "https://example.com/page?q=1", "https://example.com/page?q=1"},
		{"https with fragment", "https://example.com/page#section", "https://example.com/page#section"},
		{"empty string", "", ""},

		// Dangerous schemes — cleared
		{"javascript scheme", "javascript:alert(1)", ""},
		{"data html", "data:text/html,<script>alert(1)</script>", ""},
		{"data text", "data:text/plain,hello", ""},
		{"vbscript scheme", "vbscript:MsgBox(1)", ""},
		{"file scheme", "file:///etc/passwd", ""},
		{"ftp scheme", "ftp://example.com/file", ""},
		{"mailto scheme", "mailto:test@example.com", ""},
		{"blob scheme", "blob:some-uuid", ""},
		{"about blank", "about:blank", ""},

		// Malformed — cleared
		{"mixed case javascript", "JavaScript:alert(1)", ""},
		{"whitespace url", "  ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeURL(tt.in); got != tt.want {
				t.Errorf("sanitizeURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The PUT path patches the two fields independently, so each has to pass through the rule: a check
// written for one and not the other stores the second verbatim.
func TestHandleUpdate_SanitizesManifestURLs(t *testing.T) {
	h := testHandler(t)

	ext := makeTestExt("ext-sanitize", "test-sanitize", ExtensionTypeUI)
	ext.Manifest.Homepage = "https://safe.com"
	ext.Manifest.LicenseURL = "https://safe.com/license"
	if err := h.store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

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
	if err := json.UnmarshalRead(w.Body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Manifest.Homepage != "" {
		t.Errorf("Homepage = %q, want empty (javascript: should be sanitized)", resp.Manifest.Homepage)
	}
	if resp.Manifest.LicenseURL != "" {
		t.Errorf("LicenseURL = %q, want empty (data: should be sanitized)", resp.Manifest.LicenseURL)
	}
}

// The install path sanitizes the manifest it read out of the checkout — the same two fields, written
// by whoever published the extension rather than by the person editing the row.
func TestInstall_SanitizesTheCheckedOutManifestURLs(t *testing.T) {
	repo, commit := makeGitRepo(t, map[string]string{
		"ui/x/manifest.json": `{"display_name":"X","description":"d","author":"Acme","version":"1.0.0",` +
			`"homepage":"javascript:alert(document.cookie)","license_url":"data:text/html,<h1>hi</h1>",` +
			`"capabilities":{"ui":{"injection_points":["task-card"]}}}`,
	})
	index := RegistryIndex{{
		Name: "x", Version: "1.0.0", Type: ExtensionTypeUI, DisplayName: "X",
		Author: "Acme", Repo: repo, Commit: commit, Subdir: "ui/x",
	}}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer mockServer.Close()

	h := testHandler(t)
	h.config.RegistryURL = mockServer.URL
	h.config.ExtensionsDir = t.TempDir()

	body, _ := json.Marshal(map[string]any{"name": "x"})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstall(w, req)

	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 || !events[len(events)-1].Done {
		t.Fatalf("expected a final done SSE event, got %d", len(events))
	}
	last := events[len(events)-1]
	if last.Error != "" {
		t.Fatalf("install failed: %s", last.Error)
	}
	if last.Result.Manifest.Homepage != "" || last.Result.Manifest.LicenseURL != "" {
		t.Errorf("the installed row kept a manifest URL: homepage=%q license=%q",
			last.Result.Manifest.Homepage, last.Result.Manifest.LicenseURL)
	}
	stored, err := h.store.GetByID(last.Result.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Manifest.Homepage != "" || stored.Manifest.LicenseURL != "" {
		t.Errorf("the stored row kept a manifest URL: homepage=%q license=%q",
			stored.Manifest.Homepage, stored.Manifest.LicenseURL)
	}
}
