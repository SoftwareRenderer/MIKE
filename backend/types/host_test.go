package types

import "testing"

// What breaks if these slip: an allowlist entry that never matches its own host, or a credential
// withheld from the machine that owns it.

func TestCanonicalHostFoldsTheSpellingsOfOneName(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"API.Example.COM", "api.example.com"},
		{"  api.example.com  ", "api.example.com"},
		{"api.example.com.", "api.example.com"},
		{"api.example.com:8443", "api.example.com"},
		{"::1", "::1"},
		{"[::1]", "::1"},
		{"[::1]:8443", "::1"},
		{"FE80::1", "fe80::1"},
		{"127.0.0.1:3000", "127.0.0.1"},
		{"", ""},
	} {
		if got := CanonicalHost(tc.raw); got != tc.want {
			t.Errorf("CanonicalHost(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSameHostFoldsNothingButLoopback(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		// Case, ports and trailing dots are already folded.
		{"API.Example.com", "api.example.com", true},
		{"api.example.com:8443", "api.example.com", true},
		{"api.example.com.", "api.example.com", true},
		// Any loopback name is one machine.
		{"localhost", "127.0.0.1", true},
		{"localhost:8080", "127.0.0.1:9000", true},
		{"[::1]", "localhost", true},
		{"127.0.0.7", "localhost", true},
		// These pairs are not one machine.
		{"mcp.example.com", "sub.mcp.example.com", false},
		{"mcp.example.com", "mcp.example.com.evil.test", false},
		{"mcp.example.com", "mc.p.example.com", false},
		{"127.0.0.1", "10.0.0.5", false},
		{"10.0.0.1", "10.0.0.2", false},
		{"", "localhost", false},
		{"", "", false},
	} {
		if got := SameHost(tc.a, tc.b); got != tc.want {
			t.Errorf("SameHost(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// Host pinning and the egress allowlist must fold a host the same way, so both read CanonicalHost.
func TestCanonicalHostIsTheOneRuleForHostPinToo(t *testing.T) {
	for _, spelled := range []string{"MIKE.LAN.", "mike.lan:3000", "  mike.lan  "} {
		if CanonicalHost(spelled) != "mike.lan" {
			t.Errorf("CanonicalHost(%q) = %q, want mike.lan", spelled, CanonicalHost(spelled))
		}
	}
}
