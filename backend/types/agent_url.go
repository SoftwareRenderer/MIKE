package types

import "strings"

// AgentURLPrefix is the scheme an agent provider's URL carries: agent://<extension-name>. An
// enabled agent extension *is* a provider, so this string is the only link between a task's
// provider setting and the extension that runs it. The browser mirrors it as AGENT_URL_PREFIX
// (frontend/src/lib/types.ts); the server is the side that decides the shape.
const AgentURLPrefix = "agent://"

// AgentProviderURL is the URL an enabled agent extension is reachable as, and the inverse of
// ParseAgentURL: every URL emitted here must parse back to the extension it came from, which is
// what the completeness test in backend/agent/registry_test.go checks in both directions.
func AgentProviderURL(name string) string { return AgentURLPrefix + name }

// ParseAgentURL names the extension an agent provider URL points at. ok is false when url is not
// an agent URL, names nothing ("agent://"), or names more than one segment — an extension's name
// is a single directory name, so such a URL cannot address anything and is reported as an invalid
// URL rather than looked up and missed. A trailing slash is tolerated: a hand-typed URL carries one.
func ParseAgentURL(url string) (name string, ok bool) {
	rest, found := strings.CutPrefix(url, AgentURLPrefix)
	if !found {
		return "", false
	}
	name = strings.TrimSuffix(rest, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// IsAgentURL reports whether a URL carries the agent scheme, whether or not it names an
// extension. The provider-persistence checks ask this rather than ParseAgentURL: a row whose URL
// starts with the scheme is registry-derived and must never reach the database, even when its
// name is unusable. Ask ParseAgentURL whenever the name itself is needed.
func IsAgentURL(url string) bool { return strings.HasPrefix(url, AgentURLPrefix) }
