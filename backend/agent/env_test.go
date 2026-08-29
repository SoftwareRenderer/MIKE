package agent

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// --- helpers ---

// hasKV reports whether env contains the exact KEY=VALUE entry.
func hasKV(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// hasKey reports whether env contains any entry for the given key.
func hasKey(env []string, key string) bool {
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && name == key {
			return true
		}
	}
	return false
}

// lastValueFor returns the value of the LAST entry for key (os/exec keeps the last
// duplicate, so that is the one a child process sees) and how many entries matched.
func lastValueFor(env []string, key string) (string, int) {
	var val string
	count := 0
	for _, kv := range env {
		name, v, ok := strings.Cut(kv, "=")
		if ok && name == key {
			val = v
			count++
		}
	}
	return val, count
}

// unsetAllowlisted drops an allowlisted variable for the duration of the test; the
// preceding t.Setenv registers the cleanup that restores the original value.
func unsetAllowlisted(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "original")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

// --- tests ---

// TestSafeAgentEnv_StripsServerSecrets is the regression test for the credential
// exposure: the agent subprocess must never inherit the master password or the key
// that decrypts every provider API key in the database.
func TestSafeAgentEnv_StripsServerSecrets(t *testing.T) {
	secrets := map[string]string{
		"MIKE_LOGIN_PASSWORD":   "hunter2",
		"MIKE_ENCRYPTION_KEY":   "super-secret-key",
		"MIKE_REGISTRY_URL":     "https://example.invalid/registry.json",
		"DANGER_ZONE":           "1",
		"MIKE_TLS_KEY":          "pem-blob",
		"MIKE_LISTEN":           "127.0.0.1",
		"ANTHROPIC_API_KEY":     "sk-ant-nope",
		"OPENAI_API_KEY":        "sk-nope",
		"AWS_SECRET_ACCESS_KEY": "aws-nope",
		"CUSTOM_DB_URL":         "postgres://user:pw@host/db",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}

	env := SafeAgentEnv()
	for k, v := range secrets {
		if hasKV(env, k+"="+v) {
			t.Errorf("SafeAgentEnv leaked %s", k)
		}
		if hasKey(env, k) {
			t.Errorf("SafeAgentEnv emitted key %s at all", k)
		}
	}
}

func TestSafeAgentEnv_PreservesLoadBearingVars(t *testing.T) {
	// PATH is what resolves the adapter's default bare binary name ("pi"); HOME is what
	// the agent resolves its own config dir from. Both must survive verbatim.
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("HOME", "/home/tester")
	t.Setenv("LC_ALL", "C.UTF-8") // prefix match
	t.Setenv("NO_PROXY", "localhost")
	t.Setenv("no_proxy", "localhost")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/etc/ssl/my-ca.pem")

	env := SafeAgentEnv()
	for _, want := range []string{
		"PATH=/usr/local/bin:/usr/bin",
		"HOME=/home/tester",
		"LC_ALL=C.UTF-8",
		"NO_PROXY=localhost",
		"no_proxy=localhost",
		"NODE_EXTRA_CA_CERTS=/etc/ssl/my-ca.pem",
	} {
		if !hasKV(env, want) {
			t.Errorf("SafeAgentEnv dropped %q", want)
		}
	}
}

func TestSafeAgentEnv_OmitsUnsetVars(t *testing.T) {
	// No "VAR=" noise for allowlisted variables the server doesn't have set — an empty
	// and an unset variable are not the same thing to a shell or to Node.
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "TZ", "LANG", "SHELL", "HTTP_PROXY", "SSL_CERT_FILE"} {
		unsetAllowlisted(t, key)
	}

	env := SafeAgentEnv()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "TZ", "LANG", "SHELL", "HTTP_PROXY", "SSL_CERT_FILE"} {
		if hasKey(env, key) {
			t.Errorf("SafeAgentEnv emitted unset variable %q", key)
		}
	}
}

func TestSafeAgentEnv_ExtrasWin(t *testing.T) {
	t.Setenv("HOME", "/host/home")
	t.Setenv("PATH", "/host/bin")

	env := SafeAgentEnv("HOME=/root", "PI_CONFIG_DIR=/srv/pi")

	home, homeCount := lastValueFor(env, "HOME")
	if home != "/root" {
		t.Errorf("HOME = %q, want the extras value /root (os/exec keeps the last duplicate)", home)
	}
	if homeCount != 2 {
		t.Errorf("HOME appeared %d times, want 2 (inherited + extra)", homeCount)
	}
	// The extra is appended AFTER the allowlist, so it is the last occurrence.
	if env[len(env)-1] != "PI_CONFIG_DIR=/srv/pi" {
		t.Errorf("extras must be appended last, tail of env = %v", env[max(0, len(env)-2):])
	}
	if v, _ := lastValueFor(env, "PI_CONFIG_DIR"); v != "/srv/pi" {
		t.Errorf("adapter-supplied var lost: PI_CONFIG_DIR = %q", v)
	}
	// Non-overridden allowlisted vars are still there.
	if v, _ := lastValueFor(env, "PATH"); v != "/host/bin" {
		t.Errorf("PATH = %q, want /host/bin", v)
	}
}

func TestSafeAgentEnv_NoEnvAtAllForUnknownNames(t *testing.T) {
	// Only the allowlist is emitted — a name that merely *looks* allowlisted is not.
	for _, key := range []string{"PATHX", "HOME_DIR", "LCO", "LC", "TMPDIRS", "HTTPS_PROXY_FILE", "PATH_HOME"} {
		t.Setenv(key, "noise")
	}
	env := SafeAgentEnv()
	for _, key := range []string{"PATHX", "HOME_DIR", "LCO", "LC", "TMPDIRS", "HTTPS_PROXY_FILE", "PATH_HOME"} {
		if hasKey(env, key) {
			t.Errorf("SafeAgentEnv leaked %q", key)
		}
	}
}

// TestSafeAgentEnv_ChildSeesOnlyAllowlist runs a real child process and asserts what it
// actually sees, including the "extras win" deduplication os/exec performs.
func TestSafeAgentEnv_ChildSeesOnlyAllowlist(t *testing.T) {
	envPath, err := exec.LookPath("env")
	if err != nil {
		t.Skipf("env binary not available: %v", err)
	}
	t.Setenv("MIKE_LOGIN_PASSWORD", "hunter2")
	t.Setenv("MIKE_ENCRYPTION_KEY", "super-secret-key")
	t.Setenv("HOME", "/host/home")

	cmd := exec.Command(envPath)
	// Absolute path: the child's PATH comes from SafeAgentEnv, so don't depend on it.
	cmd.Env = SafeAgentEnv("MIKE_TEST_MARKER=from-extras")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run %s: %v", envPath, err)
	}
	child := strings.Split(strings.TrimSpace(string(out)), "\n")

	if hasKV(child, "MIKE_LOGIN_PASSWORD=hunter2") {
		t.Error("child process inherited MIKE_LOGIN_PASSWORD")
	}
	if hasKV(child, "MIKE_ENCRYPTION_KEY=super-secret-key") {
		t.Error("child process inherited MIKE_ENCRYPTION_KEY")
	}
	if v, count := lastValueFor(child, "MIKE_TEST_MARKER"); v != "from-extras" || count != 1 {
		t.Errorf("extras did not reach the child: %q (x%d)", v, count)
	}
	if v, count := lastValueFor(child, "HOME"); v != "/host/home" || count != 1 {
		t.Errorf("HOME in child = %q (x%d), want /host/home once", v, count)
	}
	if v, _ := lastValueFor(child, "PATH"); v == "" {
		t.Error("child has no PATH — the agent binary would not resolve")
	}
}
