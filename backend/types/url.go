package types

import (
	"net/url"
	"strings"
)

// URLHost is the host a URL names, or an empty string when it names none. A host that does not parse
// matches nothing, including another host that does not parse.
func URLHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// redactedCredential stands where an address's password was.
const redactedCredential = "(redacted)"

// RedactURL is an address without the password it may carry, for the log lines and messages that quote
// an address back: a pasted remote address carries its secret in it, and the written line must not.
func RedactURL(raw string) string {
	address := strings.TrimSpace(raw)
	if !strings.Contains(address, "@") {
		return address
	}
	if u, err := url.Parse(address); err == nil {
		if _, typed := u.User.Password(); !typed {
			return address
		}
		u.User = url.UserPassword(u.User.Username(), redactedCredential)
		return u.String()
	}
	// An address that will not parse still gets the textual pass, because what matters here is the
	// line it is about to be written into.
	start := strings.Index(address, "://")
	if start < 0 {
		return address
	}
	rest := address[start+3:]
	if at := strings.IndexAny(rest, "@/?#"); at >= 0 && rest[at] == '@' {
		return address[:start+3] + redactedCredential + rest[at:]
	}
	return address
}
