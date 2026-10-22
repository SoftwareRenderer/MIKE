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

	// These are the two checks a probe makes about a saved credential.
	if SameHost(URLHost("https://search.example/mcp"), URLHost("http://127.0.0.1:9/mcp")) {
		t.Error("a stored credential would be spent across hosts")
	}
	if !SameHost(URLHost("https://localhost:8080"), URLHost("http://127.0.0.1:9000")) {
		t.Error("a credential was withheld from the same machine under another loopback name")
	}
}

func TestRedactURLHidesAPastedCredential(t *testing.T) {
	const secret = "ghp_SUPERSECRET"
	for _, tc := range []struct{ raw, kept string }{
		{"https://oauth2:" + secret + "@github.com/acme/private.git", "github.com/acme/private.git"},
		{"git+ssh://oauth2:" + secret + "@git.example.com/acme/private.git", "git.example.com/acme/private.git"},
		// A path with a space in it is the address a person pastes, and the one url.Parse refuses.
		{"https://oauth2:" + secret + "@git example/acme.git", "git example/acme.git"},
	} {
		got := RedactURL(tc.raw)
		if strings.Contains(got, secret) || !strings.Contains(got, tc.kept) {
			t.Errorf("RedactURL(%q) = %q, want the password gone and %q kept", tc.raw, got, tc.kept)
		}
	}
}

func TestRedactURLLeavesAnAddressWithNoPasswordAlone(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/acme/public.git",
		"git@github.com:acme/public.git",
		"https://user@github.com/acme/public.git",
		"",
	} {
		if got := RedactURL(raw); got != raw {
			t.Errorf("RedactURL(%q) = %q, want it unchanged", raw, got)
		}
	}
}
