package agent

import (
	"os"
	"strings"
)

// Agent adapters spawn a subprocess (e.g. the `pi` binary) from an extension, and
// that process can spawn further children on the host. Handing it os.Environ()
// leaked the whole server environment through /proc/self/environ — above all
// MIKE_LOGIN_PASSWORD (log in as the operator) and MIKE_ENCRYPTION_KEY (decrypt
// every stored provider key). Agent processes get an allowlist instead.
//
// agentEnvNames are the variables an agent, and the tooling it shells out to,
// legitimately need. Everything else — credentials, tokens, MIKE_* configuration,
// DANGER_ZONE, feature flags — is dropped.
var agentEnvNames = map[string]bool{
	"PATH":    true, // adapters default to a bare binary name (e.g. "pi") resolved via exec.LookPath
	"HOME":    true, // tooling finds its config directory, and its own credentials, through $HOME
	"USER":    true,
	"LOGNAME": true,
	"SHELL":   true,
	"TERM":    true,
	"LANG":    true,
	"TZ":      true,
	"TMPDIR":  true,
	"TMP":     true,
	"TEMP":    true,
	// Without these an agent cannot verify TLS on the host.
	"SSL_CERT_FILE":       true,
	"SSL_CERT_DIR":        true,
	"NODE_EXTRA_CA_CERTS": true,
	// An agent reaching its own LLM endpoint still needs the proxy settings.
	"HTTP_PROXY":  true,
	"HTTPS_PROXY": true,
	"NO_PROXY":    true,
	"http_proxy":  true,
	"https_proxy": true,
	"no_proxy":    true,
}

// agentEnvPrefixes are families that are safe to allow in bulk (locale tuning,
// e.g. LC_ALL/LC_CTYPE).
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

// SafeAgentEnv returns the environment for an agent subprocess: the allowlisted part of the
// server's environment, then extras - adapter-supplied values win because os/exec keeps the
// LAST occurrence of a key.
//
// extras are passed through as given (from the extension's SubprocessConfig()); that is how an
// agent gets provider credentials, which are deliberately not inherited. An allowlisted
// variable the server lacks is omitted, not emitted empty: shells and Node treat `VAR=` and
// unset differently.
//
// Do not use this elsewhere. These os.Environ() sites are intentional:
//   - backend/sandbox: rootless podman needs XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS
//   - backend/term: the user's own interactive shell
//   - backend/git: git needs its environment and credential configuration
//   - pkg/bridge/agent/shell.go: already inside the sandbox container
//
// Everything else that spawns a process the LLM can steer - including the host bash tool -
// gets SafeAgentEnv.
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
