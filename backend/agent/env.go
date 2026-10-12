package agent

import "mike/pkg/subproc"

// Agent adapters spawn subprocesses; they receive an allowlist of environment
// variables to prevent leaking server credentials.
//
// agentEnvNames are variables required by agents and their children; all
// others are dropped.
var agentEnvNames = map[string]bool{
	"PATH":    true, // resolved via exec.LookPath
	"HOME":    true, // used for config and credentials
	"USER":    true,
	"LOGNAME": true,
	"SHELL":   true,
	"TERM":    true,
	"LANG":    true,
	"TZ":      true,
	"TMPDIR":  true,
	"TMP":     true,
	"TEMP":    true,
	// required for TLS verification
	"SSL_CERT_FILE":       true,
	"SSL_CERT_DIR":        true,
	"NODE_EXTRA_CA_CERTS": true,
}

// agentEnvPrefixes are safe variable prefixes (e.g. locale tuning).
var agentEnvPrefixes = []string{"LC_"}

// isAgentSafeEnv checks if a variable name is allowlisted.
var isAgentSafeEnv = subproc.Allows(agentEnvNames, agentEnvPrefixes)

// SafeAgentEnv returns allowlisted server environment plus extras (extras win).
// Provider credentials are passed via extras.
//
// used only for agent subprocesses; git, podman, and terminals use their own env providers.
func SafeAgentEnv(extras ...string) []string {
	// KeepEmpty: adapter may distinguish between cleared and unset variables.
	return append(subproc.Inherit(isAgentSafeEnv, subproc.KeepEmpty), extras...)
}
