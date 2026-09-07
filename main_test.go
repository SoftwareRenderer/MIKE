package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/pkg/logging"
)

func TestListenAddr(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		port   int
		want   string
	}{
		// Empty is the default, and the default is loopback — never all interfaces.
		{name: "empty falls back to loopback", listen: "", port: 3000, want: "127.0.0.1:3000"},
		// Explicit any-address stays the all-interfaces escape hatch.
		{name: "explicit IPv4 any", listen: "0.0.0.0", port: 3000, want: ":3000"},
		{name: "explicit IPv6 any", listen: "::", port: 3000, want: ":3000"},
		{name: "localhost IPv4", listen: "127.0.0.1", port: 8080, want: "127.0.0.1:8080"},
		{name: "localhost IPv6 brackets", listen: "::1", port: 3000, want: "[::1]:3000"},
		{name: "hostname", listen: "localhost", port: 9090, want: "localhost:9090"},
		{name: "LAN IP", listen: "192.168.1.50", port: 3000, want: "192.168.1.50:3000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listenAddr(tt.listen, tt.port); got != tt.want {
				t.Errorf("listenAddr(%q, %d) = %q, want %q", tt.listen, tt.port, got, tt.want)
			}
		})
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		listen string
		want   bool
	}{
		{listen: "", want: true},              // default = loopback
		{listen: "127.0.0.1", want: true},     //
		{listen: "127.0.0.53", want: true},    // whole 127/8 is loopback
		{listen: "::1", want: true},           //
		{listen: "[::1]", want: true},         // bracketed form
		{listen: "localhost", want: true},     //
		{listen: "0.0.0.0", want: false},      // explicit all interfaces
		{listen: "::", want: false},           //
		{listen: "192.168.1.50", want: false}, // LAN
		{listen: "10.0.0.5", want: false},     //
		{listen: "0.1.2.3", want: false},      // routable → treated as exposed
		{listen: "example.com", want: false},  //
	}
	for _, tt := range tests {
		if got := isLoopbackAddr(tt.listen); got != tt.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.listen, got, tt.want)
		}
	}
}

func TestRefusesWeakSecret(t *testing.T) {
	tests := []struct {
		name     string
		listen   string
		password string
		want     bool
	}{
		// Loopback keeps the dev flow working, weak secret or not.
		{name: "loopback default + weak", listen: "", password: "test", want: false},
		{name: "loopback explicit + weak", listen: "127.0.0.1", password: "dev", want: false},
		{name: "loopback + empty", listen: "localhost", password: "", want: false},
		// Exposed listen addresses refuse weak secrets.
		{name: "all interfaces + short", listen: "0.0.0.0", password: "abc", want: true},
		{name: "all interfaces + empty", listen: "0.0.0.0", password: "", want: true},
		{name: "all interfaces + known default", listen: "0.0.0.0", password: "test", want: true},
		{name: "IPv6 any + known default", listen: "::", password: "Password", want: true},
		{name: "LAN ip + known default", listen: "192.168.1.50", password: "changeme", want: true},
		{name: "LAN ip + known default uppercase", listen: "192.168.1.50", password: "ADMIN", want: true},
		{name: "LAN ip + known default padded", listen: "192.168.1.50", password: "  pass  ", want: true},
		// Long enough and not a known default → accepted.
		{name: "all interfaces + strong", listen: "0.0.0.0", password: "correct horse battery", want: false},
		{name: "all interfaces + long default-ish", listen: "0.0.0.0", password: "test-password-1234", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := refusesWeakSecret(tt.listen, tt.password); got != tt.want {
				t.Errorf("refusesWeakSecret(%q, %q) = %v, want %v", tt.listen, tt.password, got, tt.want)
			}
		})
	}
}

// TestOpenTraceFileIsOwnerOnly covers L8: the flight recorder is always-on and
// holds ~30s of runtime internals, so both the initial file and the rotated
// replacement must be created 0600.
func TestOpenTraceFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.trace")

	f, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile: %v", err)
	}
	// Rotate exactly like the 30s ticker does: truncate the same path again.
	f.Close()
	f2, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile (rotate): %v", err)
	}
	defer f2.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("trace file mode = %#o, want 0600", perm)
	}
}

func TestResolveStringPrecedence(t *testing.T) {
	const env = "MIKE_RESOLVE_STRING_TEST"
	t.Cleanup(func() { os.Unsetenv(env) })

	// Nothing set: the fallback.
	os.Unsetenv(env)
	if got := resolveString("", env, "fallback"); got != "fallback" {
		t.Errorf("unset env = %q, want fallback", got)
	}
	// A set-but-empty env var means unset, so an accidental MIKE_LISTEN= cannot
	// silently become "all interfaces".
	os.Setenv(env, "")
	if got := resolveString("", env, "fallback"); got != "fallback" {
		t.Errorf("empty env = %q, want fallback", got)
	}
	os.Setenv(env, "from-env")
	if got := resolveString("", env, "fallback"); got != "from-env" {
		t.Errorf("env = %q, want from-env", got)
	}
	if got := resolveString("from-flag", env, "fallback"); got != "from-flag" {
		t.Errorf("flag = %q, want from-flag (flag must beat env)", got)
	}
}

func TestResolvePort(t *testing.T) {
	const env = "MIKE_RESOLVE_PORT_TEST"
	t.Cleanup(func() { os.Unsetenv(env) })

	tests := []struct {
		name      string
		flagValue int
		envValue  string
		want      string // "" means the resolved number, otherwise a substring of the error
		port      int
	}{
		{name: "nothing set falls back", envValue: "", port: 3000},
		{name: "flag wins", flagValue: 8080, envValue: "9090", port: 8080},
		{name: "env is parsed", envValue: "9090", port: 9090},
		// The bug this exists for: fmt.Sscanf accepted this and left the port at 3000.
		{name: "non-numeric env is refused", envValue: "tcp3000", port: 0},
		{name: "zero env is refused", envValue: "0", port: 0},
		{name: "negative env is refused", envValue: "-1", port: 0},
		{name: "above range env is refused", envValue: "70000", port: 0},
		{name: "above range flag is refused", flagValue: 70000, port: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envValue == "" {
				os.Unsetenv(env)
			} else {
				os.Setenv(env, tc.envValue)
			}
			got, err := resolvePort(tc.flagValue, "port", env, 3000)
			if tc.port == 0 {
				if err == nil {
					t.Fatalf("resolvePort(%d, %q) = %d, want an error", tc.flagValue, tc.envValue, got)
				}
				if !strings.Contains(err.Error(), env) && !strings.Contains(err.Error(), "--port") {
					t.Errorf("error %q must name the source it came from", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePort(%d, %q): %v", tc.flagValue, tc.envValue, err)
			}
			if got != tc.port {
				t.Errorf("resolvePort(%d, %q) = %d, want %d", tc.flagValue, tc.envValue, got, tc.port)
			}
		})
	}
}

// TestLogLevelResolution pins the --log-level / MIKE_LOG_LEVEL ladder and that a non-level value is
// REFUSED rather than guessed at - a typo must not silently cost the operator the log.
func TestLogLevelResolution(t *testing.T) {
	tests := []struct {
		name      string
		flagValue string
		envValue  string
		envSet    bool
		want      string // effective name after the ladder
		wantErr   bool
	}{
		{name: "nothing set defaults to info", want: "info"},
		{name: "env wins over default", envValue: "debug", envSet: true, want: "debug"},
		{name: "flag wins over env", flagValue: "warn", envValue: "debug", envSet: true, want: "warn"},
		{name: "set-but-empty env means unset", envValue: "", envSet: true, want: "info"},
		{name: "typo is refused, not defaulted", envValue: "verbose", envSet: true, want: "verbose", wantErr: true},
		{name: "numeric level is refused", envValue: "0", envSet: true, want: "0", wantErr: true},
		{name: "flag typo is refused too", flagValue: "notice", want: "notice", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MIKE_LOG_LEVEL", "")
			if !tt.envSet {
				os.Unsetenv("MIKE_LOG_LEVEL")
			} else {
				t.Setenv("MIKE_LOG_LEVEL", tt.envValue)
			}
			got := resolveString(tt.flagValue, "MIKE_LOG_LEVEL", "info")
			if got != tt.want {
				t.Fatalf("resolveString = %q, want %q", got, tt.want)
			}
			_, err := logging.ParseLevel(got)
			if tt.wantErr && err == nil {
				t.Errorf("ParseLevel(%q) accepted a value that is not a level; startup must refuse", got)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ParseLevel(%q) rejected a valid level: %v", got, err)
			}
		})
	}
}
