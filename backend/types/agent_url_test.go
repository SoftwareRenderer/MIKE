package types

import "testing"

func TestParseAgentURL(t *testing.T) {
	tests := []struct {
		url      string
		wantName string
		wantOK   bool
	}{
		{"agent://pi", "pi", true},
		{"agent://claude-code", "claude-code", true},
		// A hand-typed URL carries the trailing slash; Lookup has always accepted it.
		{"agent://pi/", "pi", true},
		// Names nothing.
		{"agent://", "", false},
		{"agent:///", "", false},
		// Not an agent URL.
		{"http://localhost:8080/v1", "", false},
		{"", "", false},
		// No scheme at all — a bare name is not a provider URL.
		{"pi", "", false},
		// More than one segment: an extension name is a single directory name, so this
		// addresses nothing and must read as an invalid URL, not as a miss.
		{"agent://org/pi", "", false},
		{"agent://pi//", "", false},
	}

	for _, tc := range tests {
		name, ok := ParseAgentURL(tc.url)
		if ok != tc.wantOK || name != tc.wantName {
			t.Errorf("ParseAgentURL(%q) = (%q, %v), want (%q, %v)", tc.url, name, ok, tc.wantName, tc.wantOK)
		}
	}
}

func TestAgentProviderURLRoundTrip(t *testing.T) {
	for _, name := range []string{"pi", "claude-code", "agent2", "x"} {
		url := AgentProviderURL(name)
		got, ok := ParseAgentURL(url)
		if !ok || got != name {
			t.Errorf("round trip of %q: AgentProviderURL→ParseAgentURL = (%q, %v)", name, got, ok)
		}
		if !IsAgentURL(url) {
			t.Errorf("IsAgentURL(%q) = false for an emitted provider URL", url)
		}
	}
}

// TestIsAgentURLWithoutName pins the difference between the two questions: a URL that carries the
// scheme but names no extension is still an agent provider row for persistence purposes (it must
// never be stored), while ParseAgentURL refuses to invent a name for it.
func TestIsAgentURLWithoutName(t *testing.T) {
	if !IsAgentURL("agent://") {
		t.Error(`IsAgentURL("agent://") = false, want true: the scheme is present even with no name`)
	}
	if _, ok := ParseAgentURL("agent://"); ok {
		t.Error(`ParseAgentURL("agent://") accepted a URL that names no extension`)
	}
	for _, url := range []string{"", "http://x", "pi://agent", "AGENT://pi"} {
		if IsAgentURL(url) {
			t.Errorf("IsAgentURL(%q) = true, want false", url)
		}
	}
}
