package extensions

import (
	"testing"
)

func TestSanitizeURL(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			got := sanitizeURL(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeManifestURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		homepage       string
		licenseURL     string
		wantHomepage   string
		wantLicenseURL string
	}{
		{"both valid", "https://example.com", "https://license.com", "https://example.com", "https://license.com"},
		{"homepage javascript", "javascript:alert(1)", "https://license.com", "", "https://license.com"},
		{"license data url", "https://example.com", "data:text/html,<h1>hi</h1>", "https://example.com", ""},
		{"both malicious", "javascript:alert(1)", "data:text/html,evil", "", ""},
		{"both empty", "", "", "", ""},
		{"homepage empty license valid", "", "https://license.com", "", "https://license.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &ExtensionManifest{
				DisplayName: "Test",
				Homepage:    tt.homepage,
				LicenseURL:  tt.licenseURL,
			}
			sanitizeManifestURLs(m)
			if m.Homepage != tt.wantHomepage {
				t.Errorf("Homepage = %q, want %q", m.Homepage, tt.wantHomepage)
			}
			if m.LicenseURL != tt.wantLicenseURL {
				t.Errorf("LicenseURL = %q, want %q", m.LicenseURL, tt.wantLicenseURL)
			}
		})
	}
}

func TestSanitizeManifestURLs_Nil(t *testing.T) {
	t.Parallel()
	// Should not panic on nil
	sanitizeManifestURLs(nil)
}
