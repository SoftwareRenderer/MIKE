package agent

import (
	"os"
	"strings"
)

// Agent adapters spawn a subprocess (e.g. the `pi` binary) from an *extension*, and that
// process is free to spawn further children on the host. Handing it `os.Environ()` leaked
// the whole server environment through /proc/self/environ — above all MIKE_LOGIN_PASSWORD
// (re-authenticate as the operator) and MIKE_ENCRYPTION_KEY (decrypt every provider API key
// stored in the database). Agent processes get an allowlist instead.
//
// agentEnvNames are the variables an agent (and the tooling it shells out to) legitimately
// needs from the ambient environment. Everything else — credentials, tokens, MIKE_* server
// configuration, DANGER_ZONE, feature flags — is dropped.
var agentEnvNames = map[string]bool{
	"PATH":    true, // load-bearing, not optional: adapters default to a bare binary name (e.g. "pi") resolved via exec.LookPath
	"HOME":    true, // load-bearing: tooling resolves its config directory (and thus its own credentials) from $HOME
	"USER":    true,
	"LOGNAME": true,
	"SHELL":   true,
	"TERM":    true,
	"LANG":    true,
	"TZ":      true,
	"TMPDIR":  true,
	"TMP":     true,
	"TEMP":    true,
	// Trust-store overrides: without these an agent cannot verify TLS on the host.
	"SSL_CERT_FILE":       true,
	"SSL_CERT_DIR":        true,
	"NODE_EXTRA_CA_CERTS": true,
	// Egress routing: an agent that reaches its own LLM endpoint still needs the proxy config.
	"HTTP_PROXY":  true,
	"HTTPS_PROXY": true,
	"NO_PROXY":    true,
	"http_proxy":  true,
	"https_proxy": true,
	"no_proxy":    true,
}

// agentEnvPrefixes are families that are safe in bulk (locale tuning, e.g. LC_ALL/LC_CTYPE).
var agentEnvPrefixes = []string{"LC_"}

// isAgentSafeEnv reports whether an environment variable name is on the allowlist.
func isAgentSafeEnv(name string) bool {
	if agentEnvNames[name] {
		return true
	}
	for _, p := range agentEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// SafeAgentEnv returns the environment for an agent subprocess: a minimal allowlist of the
// server's own environment followed by extras, so adapter-supplied values win — os/exec
// deduplicates keeping the LAST occurrence of a key.
//
// extras are passed through verbatim (they come from the extension's SubprocessConfig(),
// i.e. its config plus the manifest's sandbox env), which is how an agent receives anything
// app-specific — including provider credentials, which are deliberately NOT inherited from
// the server environment.
//
// Allowlisted variables that aren't set in the server environment are omitted entirely
// rather than emitted empty, so the child never sees `VAR=` noise (an empty and an unset
// variable mean different things to shells and to Node).
//
// Deliberately NOT using this anywhere else — leave these os.Environ() call sites alone:
//   - backend/sandbox/sandbox.go: rootless podman needs XDG_RUNTIME_DIR /
//     DBUS_SESSION_BUS_ADDRESS from the ambient environment to reach the user's session.
//   - backend/term/terminal.go: that is intentionally the user's own interactive shell.
//   - backend/git/*: git needs its env/credential configuration to work.
//   - pkg/bridge/agent/shell.go: already runs inside the sandbox container, which is the
//     isolation boundary.
func SafeAgentEnv(extras ...string) []string {
	env := make([]string, 0, len(agentEnvNames)+len(extras)+2)
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" || !isAgentSafeEnv(name) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extras...)
}
