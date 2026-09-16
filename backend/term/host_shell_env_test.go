package term

import (
	"strings"
	"testing"

	"mike/backend/sandbox"
)

// TestHostShellEnvIsGatedOnDangerZone pins both sides of the host terminal's
// environment, because the two failure directions are different problems:
//
//   - without DANGER_ZONE, the shell must not carry the server's secrets
//     (MIKE_LOGIN_PASSWORD logs in as the operator; MIKE_ENCRYPTION_KEY decrypts every
//     stored provider key) while still being a usable shell;
//   - with the grant, the terminal is the operator's own shell and keeps the environment
//     their own shell would have. Scrubbing that silently breaks the tooling they opted in for.
//
// The grant is an argument (sandbox.DangerZone), not an environment variable: the test says which
// process it is pretending to have booted.
func TestHostShellEnvIsGatedOnDangerZone(t *testing.T) {
	t.Setenv("MIKE_LOGIN_PASSWORD", "operator-password")
	t.Setenv("MIKE_ENCRYPTION_KEY", "encryption-key")
	t.Setenv("MIKE_LISTEN", "0.0.0.0")

	has := func(env []string, prefix string) bool {
		for _, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				return true
			}
		}
		return false
	}
	last := func(env []string, prefix string) string {
		got := ""
		for _, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				got = kv
			}
		}
		return got
	}

	// No opt-in: no server secrets, but a working shell.
	env := hostShellEnv(sandbox.DangerZone(false))
	for _, secret := range []string{"MIKE_LOGIN_PASSWORD=", "MIKE_ENCRYPTION_KEY=", "MIKE_LISTEN="} {
		if has(env, secret) {
			t.Errorf("without DANGER_ZONE the host shell still receives %s", secret)
		}
	}
	for _, needed := range []string{"PATH=", "HOME="} {
		if !has(env, needed) {
			t.Errorf("the scrubbed shell cannot work: %s was dropped", needed)
		}
	}
	// extras win, because os/exec keeps the last occurrence of a key.
	if got := last(env, "TERM="); got != "TERM=xterm-256color" {
		t.Errorf("TERM = %q, want the terminal's own value last", got)
	}

	// Opt-in: the operator's environment, as documented.
	env = hostShellEnv(sandbox.DangerZone(true))
	if !has(env, "MIKE_LOGIN_PASSWORD=") {
		t.Error("DANGER_ZONE=1 no longer gives the operator's own shell their environment")
	}
}
