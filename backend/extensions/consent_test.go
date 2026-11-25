package extensions

// The two ends of one rule: this predicate decides whether a development load is refused, and
// requiresInstallReview() in frontend/src/lib/utils/extension-permissions.ts decides whether the install
// dialog opens. The fixtures below are CONSENT_FIXTURES in
// frontend/src/lib/utils/__tests__/extension-consent-gate.unit.ts, spelled the same way — change them in
// one file, change them in the other.

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

var consentFixtures = []struct {
	name     string
	manifest string
	want     bool
}{
	{"a plain card",
		`{"capabilities":{"ui":{"injection_points":["taskCard"],"permissions":["ui.toast"]}}}`, false},
	{"a stylesheet",
		`{"capabilities":{"css":{"css_url":"theme.css"}}}`, false},
	{"server-side egress",
		`{"capabilities":{"network":{"allowed_domains":["api.example.com"]}}}`, true},
	{"egress with no domain named",
		`{"capabilities":{"network":{}}}`, true},
	{"a proxy call from the frame",
		`{"capabilities":{"ui":{"permissions":["editor.getContent","network.proxy"]}}}`, true},
	{"a page served with network",
		`{"capabilities":{"ui":{"permissions":["preview.serveNetwork"]}}}`, true},
	{"a page served with scripts",
		`{"capabilities":{"ui":{"permissions":["preview.serveScripts"]}}}`, true},
	{"a byte read of the opened file",
		`{"capabilities":{"ui":{"injection_points":["binaryPreview"],"file_suffixes":["stl"],"permissions":["fs.getBytes"]}}}`, false},
	{"a declared dependency directory",
		`{"capabilities":{"lsp":{"sandbox":{"dependency_cache":{"container_path":"/go/pkg/mod","env":"GOMODCACHE"}}}}}`, true},
	{"a suggested agent folder",
		`{"capabilities":{"agent":{"sandbox":{"mount":[{"host_path":"~/.pi/agent","container_path":"/home/sandbox/.pi/agent","mode":"ro"}]}}}}`, true},
	{"an agent folder with nothing in it",
		`{"capabilities":{"agent":{"sandbox":{"mount":[]}}}}`, false},
	{"nothing at all",
		`{}`, false},
}

func decodeCaps(t *testing.T, manifest string) *Capabilities {
	t.Helper()
	return decodeManifest(t, manifest).Capabilities
}

func TestConsentConditionsMatchTheInstallDialog(t *testing.T) {
	for _, tc := range consentFixtures {
		t.Run(tc.name, func(t *testing.T) {
			if got := consentRequired(decodeCaps(t, tc.manifest)); got != tc.want {
				t.Errorf("consentRequired = %v, want %v", got, tc.want)
			}
		})
	}
}

// The dangerous names are a shared list with the browser; a fourth on one side only is the drift.
func TestDangerousPermissionNamesAreTheSharedThree(t *testing.T) {
	want := []string{"network.proxy", "preview.serveScripts", "preview.serveNetwork"}
	if len(dangerousUIPermissions) != len(want) {
		t.Fatalf("the list holds %d names, want %d", len(dangerousUIPermissions), len(want))
	}
	for _, name := range want {
		if !dangerousUIPermissions[name] {
			t.Errorf("%s is missing from dangerousUIPermissions", name)
		}
	}
}

// An acknowledgement is tied to what the person was shown, so its fingerprint must move exactly when the
// declaration they saw did — and not when anything else in the manifest did.
func TestConsentFingerprintMovesOnlyWithTheDeclaration(t *testing.T) {
	const twoDomains = `{"display_name":"A","version":"0.1.0","capabilities":{"network":{"allowed_domains":["b.example","a.example"]}}}`
	const sameDomainsOtherOrder = `{"display_name":"A","version":"0.1.0","capabilities":{"network":{"allowed_domains":["a.example","b.example"]}}}`
	const renamed = `{"display_name":"B","version":"0.9.0","capabilities":{"network":{"allowed_domains":["a.example","b.example"]}}}`
	const oneDomain = `{"display_name":"A","version":"0.1.0","capabilities":{"network":{"allowed_domains":["a.example"]}}}`
	const headerAdded = `{"display_name":"A","version":"0.1.0","capabilities":{"network":{"allowed_domains":["b.example","a.example"],"headers":{"Authorization":"Bearer {{token}}"}}}}`

	base := consentFingerprint(decodeCaps(t, twoDomains))
	if got := consentFingerprint(decodeCaps(t, sameDomainsOtherOrder)); got != base {
		t.Errorf("listing the same hosts in another order moved the fingerprint: %s vs %s", got, base)
	}
	if got := consentFingerprint(decodeCaps(t, renamed)); got != base {
		t.Errorf("wording changes moved the fingerprint: %s vs %s", got, base)
	}
	if got := consentFingerprint(decodeCaps(t, oneDomain)); got == base {
		t.Error("dropping a host left the fingerprint alone")
	}
	if got := consentFingerprint(decodeCaps(t, headerAdded)); got == base {
		t.Error("a new header template left the fingerprint alone, so a secret could ride the old yes")
	}
	if got := consentFingerprint(nil); got == base || got == "" {
		t.Error("an extension with no capabilities has no fingerprint of its own")
	}
}

// A refusal has to be readable by a client that cannot render the dialog.
func TestARefusalNamesWhatItIsAskingFor(t *testing.T) {
	refused := &ErrConsentRequired{Manifest: decodeManifest(t, `{"display_name":"A","capabilities":{`+
		`"network":{"allowed_domains":["z.example","a.example"]},`+
		`"ui":{"permissions":["ui.toast","preview.serveNetwork"]}}}`)}
	got := strings.Join(refused.Reasons(), " | ")
	for _, want := range []string{"z.example", "a.example", "preview.serveNetwork"} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal never names %s: %q", want, got)
		}
	}
	if strings.Contains(got, "ui.toast") {
		t.Errorf("the refusal listed a permission nobody is being asked about: %q", got)
	}
	if !strings.Contains(refused.Error(), "z.example") {
		t.Errorf("the sentence a log line carries omits the host: %s", refused.Error())
	}
}

func decodeManifest(t *testing.T, manifest string) *ExtensionManifest {
	t.Helper()
	var m ExtensionManifest
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatalf("the fixture is not a manifest: %v", err)
	}
	return &m
}
