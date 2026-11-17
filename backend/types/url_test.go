package types

import (
	"strings"
	"testing"
)

func TestURLHostNamesTheHostOrNothing(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://Search.Example/path", "Search.Example"},
		{"http://127.0.0.1:8080", "127.0.0.1"},
		{"http://[::1]:8080/search", "::1"},
		{"  https://api.example.com  ", "api.example.com"},
		{"", ""},
		{"not a url at all", ""},
		{"://", ""},
	} {
		if got := URLHost(tc.raw); got != tc.want {
			t.Errorf("URLHost(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// RedactURL is what a log line quotes when it quotes an address: the password goes, everything a
// person would recognise stays. An address carrying no password is handed back untouched — rewriting
// one that parses is how a remote name stops matching the one the person typed.
func TestRedactURLRemovesOnlyThePassword(t *testing.T) {
	const secret = "ghp_SUPERSECRET"
	for _, tc := range []struct{ raw, want string }{
		// The credential forms a person can paste, and the address that must survive the cut.
		{"https://oauth2:" + secret + "@github.com/acme/private.git", "github.com/acme/private.git"},
		{"git+ssh://oauth2:" + secret + "@git.example.com/acme/private.git", "git.example.com/acme/private.git"},
		// A path with a space in it is the address a person pastes, and the one url.Parse refuses.
		{"https://oauth2:" + secret + "@git example/acme.git", "git example/acme.git"},
		// No password to take: the address comes back exactly as it went in.
		{"https://github.com/acme/public.git", "https://github.com/acme/public.git"},
		{"git@github.com:acme/public.git", "git@github.com:acme/public.git"},
		{"https://user@github.com/acme/public.git", "https://user@github.com/acme/public.git"},
		{"", ""},
	} {
		got := RedactURL(tc.raw)
		if strings.Contains(got, secret) {
			t.Errorf("RedactURL(%q) = %q, which still carries the credential", tc.raw, got)
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("RedactURL(%q) = %q, want %q kept", tc.raw, got, tc.want)
		}
		if !strings.Contains(tc.raw, secret) && got != tc.raw {
			t.Errorf("RedactURL(%q) = %q, want it unchanged", tc.raw, got)
		}
	}
}
