package types

import "testing"

// One grammar, both questions. ParseAgentURL names the extension a provider row addresses; IsAgentURL
// only asks whether the row carries the agent scheme. They differ on purpose: a URL with the scheme and
// no usable name is still a registry-derived row that must never reach the database, while
// ParseAgentURL refuses to invent an extension for it. A trailing slash is tolerated because a
// hand-typed URL carries one; more than one segment addresses nothing, an extension name being one
// directory name.
func TestAgentURLGrammar(t *testing.T) {
	for _, tc := range []struct {
		url       string
		wantName  string
		wantOK    bool
		wantAgent bool
	}{
		{"agent://pi", "pi", true, true},
		{"agent://claude-code", "claude-code", true, true},
		{"agent://pi/", "pi", true, true},
		// Carries the scheme, names nothing usable.
		{"agent://", "", false, true},
		{"agent:///", "", false, true},
		{"agent://org/pi", "", false, true},
		{"agent://pi//", "", false, true},
		// Not an agent URL at all.
		{"http://localhost:8080/v1", "", false, false},
		{"", "", false, false},
		// A bare name is not a provider URL.
		{"pi", "", false, false},
		{"pi://agent", "", false, false},
		// The scheme is compared, not folded: a different spelling is a different scheme.
		{"AGENT://pi", "", false, false},
	} {
		t.Run(func() string {
			if tc.url == "" {
				return "empty"
			}
			return tc.url
		}(), func(t *testing.T) {
			name, ok := ParseAgentURL(tc.url)
			if ok != tc.wantOK || name != tc.wantName {
				t.Errorf("ParseAgentURL(%q) = (%q, %v), want (%q, %v)", tc.url, name, ok, tc.wantName, tc.wantOK)
			}
			if got := IsAgentURL(tc.url); got != tc.wantAgent {
				t.Errorf("IsAgentURL(%q) = %v, want %v", tc.url, got, tc.wantAgent)
			}
		})
	}
}

// TestAgentProviderURLRoundTrip pins the writer to the reader: every URL the server hands out for an
// enabled extension parses back to the extension it came from, and reads as an agent row.
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
