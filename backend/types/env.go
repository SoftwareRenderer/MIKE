package types

import (
	"fmt"
	"strings"
)

// One rule, with the argument shape making it unforgeable:
//
//	The container's environment is the image's plus what the project declared. Never the host's.
//
// podman's `-e NAME` with no `=value` copies NAME out of the podman process's environment — the
// server's — rather than setting it empty. These functions can only produce KEY=VALUE.

// ValidateSandboxEnv reports whether one declared variable can be turned into a podman `-e`
// assignment.
//
// An empty key is skipped by the caller rather than refused: a project's saved configuration may
// hold a blank row from the settings form, and refusing to start the sandbox over it would be a
// worse outcome than ignoring it. Every other case is a refusal, because a silently-dropped
// variable is a container that starts with the wrong environment and no explanation.
func ValidateSandboxEnv(e SandboxEnv) error {
	if e.Key == "" {
		return fmt.Errorf("sandbox env: empty variable name")
	}
	// `=` is the delimiter: `A=B=C` would be handed to podman as an assignment whose NAME contains
	// `=`, which podman rejects, taking the whole container create down with a message about
	// arguments rather than about the configuration row that caused it.
	if strings.ContainsAny(e.Key, "=\n\r\t ,") {
		return fmt.Errorf("sandbox env name %q must not contain '=', whitespace or ','", e.Key)
	}
	if strings.HasPrefix(e.Key, "-") {
		return fmt.Errorf("sandbox env name %q must not start with '-'", e.Key)
	}
	// A newline in the VALUE cannot be escaped through the argument vector (it is one argv string,
	// not a shell word), but podman's own parsing of the assignment treats it as a separator, so it
	// is refused rather than passed and hoped over.
	if strings.ContainsAny(e.Value, "\r\n") {
		return fmt.Errorf("sandbox env %q: value must not contain a newline", e.Key)
	}
	return nil
}

// SandboxEnvArgs builds the `-e` flags for a set of declared variables, in order, skipping the
// blank rows a saved form leaves behind and refusing anything else that is not a valid assignment.
//
// Its return type is deliberately []string of PAIRS ("‑e", "KEY=VALUE") rather than a joined
// string: the caller appends it straight to an argument vector, and every element is one argv entry,
// so no quoting or word-splitting is possible at the call site.
func SandboxEnvArgs(envs []SandboxEnv) ([]string, error) {
	var args []string
	for _, e := range envs {
		if e.Key == "" {
			continue // a blank row in the settings form, not a configuration error
		}
		if err := ValidateSandboxEnv(e); err != nil {
			return nil, err
		}
		args = append(args, "-e", e.Key+"="+e.Value)
	}
	return args, nil
}
