package startup

import (
	"os"
	"strings"
	"testing"

	"kanban/backend/extensions"
	"kanban/backend/fsutil"
	"kanban/backend/sandbox"
	"kanban/pkg/logging"
)

// env is the test-only variable used by the ladder tests. Nothing in production reads it.
const probeEnv = "MIKE_RESOLVE_STRING_TEST"

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
		if got := IsLoopbackAddr(tt.listen); got != tt.want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", tt.listen, got, tt.want)
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
			c := Config{Listen: tt.listen, MasterPassword: tt.password}
			if got := c.RefusesWeakSecret(); got != tt.want {
				t.Errorf("RefusesWeakSecret(listen=%q, password=%q) = %v, want %v", tt.listen, tt.password, got, tt.want)
			}
		})
	}
}

func TestResolveStringPrecedence(t *testing.T) {
	t.Setenv(probeEnv, "")

	// Nothing set: the fallback.
	os.Unsetenv(probeEnv)
	if got := resolveString("", probeEnv, "fallback"); got != "fallback" {
		t.Errorf("unset env = %q, want fallback", got)
	}
	// A set-but-empty env var means unset, so an accidental MIKE_LISTEN= cannot
	// silently become "all interfaces".
	os.Setenv(probeEnv, "")
	if got := resolveString("", probeEnv, "fallback"); got != "fallback" {
		t.Errorf("empty env = %q, want fallback", got)
	}
	os.Setenv(probeEnv, "from-env")
	if got := resolveString("", probeEnv, "fallback"); got != "from-env" {
		t.Errorf("env = %q, want from-env", got)
	}
	if got := resolveString("from-flag", probeEnv, "fallback"); got != "from-flag" {
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
			got, err := resolvePort(tc.flagValue, "--port", env, 3000)
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
			t.Setenv(EnvLogLevel, "")
			if !tt.envSet {
				os.Unsetenv(EnvLogLevel)
			} else {
				t.Setenv(EnvLogLevel, tt.envValue)
			}
			got := resolveString(tt.flagValue, EnvLogLevel, "info")
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

// mustResolve resolves with the encryption key present, which every real boot requires; a test that
// wants the failure asks for it by name.
func mustResolve(t *testing.T, f Flags) Config {
	t.Helper()
	t.Setenv(EnvEncryptionKey, "a-long-enough-test-key")
	c, err := Resolve(f)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return c
}

// The opt-in switches are the exact value "1" and nothing else: a truthy-looking typo is not the
// opt-in it was meant to be, and the operator hears about it instead of being refused later.
func TestOptInSwitchesAreTheExactValueOne(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{value: "1", want: true},
		{value: "", want: false},  // set-but-empty means unset
		{value: "0", want: false}, // the explicit no
		{value: "true", want: false},
		{value: "yes", want: false},
		{value: "on", want: false},
		{value: " 1 ", want: false}, // not trimmed: the value is the literal, not a sentence
	}
	for _, tc := range cases {
		for _, name := range []string{EnvAllowRootFS, EnvDangerZone, EnvTrustProxy, EnvGCDryRun, EnvPProf} {
			t.Run(name+"="+tc.value, func(t *testing.T) {
				t.Setenv(name, tc.value)
				c := mustResolve(t, Flags{})
				var got bool
				switch name {
				case EnvAllowRootFS:
					got = c.AllowRootFS
				case EnvDangerZone:
					got = c.DangerZone
				case EnvTrustProxy:
					got = c.TrustProxy
				case EnvGCDryRun:
					got = c.GCDryRun
				case EnvPProf:
					got = c.PProf
				}
				if got != tc.want {
					t.Errorf("%s=%q resolved to %v, want %v", name, tc.value, got, tc.want)
				}
				// A value that is set but does nothing must say so; the ordinary no ("", "0") must not.
				warned := strings.Contains(strings.Join(c.Warnings, "\n"), name)
				if wantWarn := tc.value != "" && tc.value != "1"; warned != wantWarn {
					t.Errorf("%s=%q warned=%v, want %v (warnings: %v)", name, tc.value, warned, wantWarn, c.Warnings)
				}
			})
		}
	}
}

// The login budget cannot be turned off by a typo: 0, negative and unparsable values fall back to the
// default and report it.
func TestLoginRateLimitCannotBeDisabled(t *testing.T) {
	for _, raw := range []string{"0", "-3", "five", "5.5", ""} {
		t.Run("MIKE_LOGIN_RATE_LIMIT="+raw, func(t *testing.T) {
			if raw == "" {
				t.Setenv(EnvLoginRateLimit, "")
				os.Unsetenv(EnvLoginRateLimit)
			} else {
				t.Setenv(EnvLoginRateLimit, raw)
			}
			c := mustResolve(t, Flags{})
			if c.LoginRateLimit != DefaultLoginRateLimit {
				t.Errorf("limit = %d, want the default %d", c.LoginRateLimit, DefaultLoginRateLimit)
			}
			if raw != "" {
				if !strings.Contains(strings.Join(c.Warnings, "\n"), EnvLoginRateLimit) {
					t.Errorf("a value that fell back must be reported, got warnings %v", c.Warnings)
				}
			} else if len(c.Warnings) != 0 {
				t.Errorf("an unset budget is the ordinary case and warns about nothing, got %v", c.Warnings)
			}
		})
	}
	t.Run("a positive budget is honored", func(t *testing.T) {
		t.Setenv(EnvLoginRateLimit, "42")
		if got := mustResolve(t, Flags{}).LoginRateLimit; got != 42 {
			t.Errorf("limit = %d, want 42", got)
		}
	})
}

// The memory budget percentage decides how much of the machine MIKE may occupy, so a value that does
// not parse refuses the boot naming the variable rather than quietly taking the default share.
func TestMemoryBudgetPctRefusesAnythingButAPercentage(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", DefaultMemoryBudgetPct},
		{"42", 42},
		{"1", 1},
		{"100", 100},
	} {
		t.Run("MIKE_MEMORY_BUDGET_PCT="+tc.raw, func(t *testing.T) {
			t.Setenv(EnvMemoryBudgetPct, tc.raw)
			if got := mustResolve(t, Flags{}).MemoryBudgetPct; got != tc.want {
				t.Errorf("pct = %d, want %d", got, tc.want)
			}
		})
	}
	for _, raw := range []string{"0", "101", "-3", "ninety", "90.5"} {
		t.Run("refuses "+raw, func(t *testing.T) {
			if msg := mustFailResolve(t, raw); !strings.Contains(msg, EnvMemoryBudgetPct) {
				t.Errorf("error %q must name %s", msg, EnvMemoryBudgetPct)
			}
		})
	}
}

// mustFailResolve resolves with MIKE_MEMORY_BUDGET_PCT=raw and returns the error text, failing the
// test when the boot was accepted.
func mustFailResolve(t *testing.T, raw string) string {
	t.Helper()
	t.Setenv(EnvEncryptionKey, "a-long-enough-test-key")
	t.Setenv(EnvMemoryBudgetPct, raw)
	_, err := Resolve(Flags{})
	if err == nil {
		t.Fatalf("Resolve accepted %s=%q", EnvMemoryBudgetPct, raw)
	}
	return err.Error()
}

// Resolve is the only place the ladder runs, so the fields a consumer reads must come from it —
// including the ones whose default is not obvious (loopback, ./data, the login budget).
func TestResolveDefaults(t *testing.T) {
	c := mustResolve(t, Flags{})
	if c.Addr != "127.0.0.1:3000" {
		t.Errorf("Addr = %q, want 127.0.0.1:3000 (loopback by default: the API is RCE-complete)", c.Addr)
	}
	if c.DataDir != "./data" || !strings.HasSuffix(c.DBPath, "kanban.db") {
		t.Errorf("DataDir = %q, DBPath = %q", c.DataDir, c.DBPath)
	}
	if c.FrontendDist != DefaultFrontend {
		t.Errorf("FrontendDist = %q, want %q", c.FrontendDist, DefaultFrontend)
	}
	if c.LoginRateLimit != DefaultLoginRateLimit {
		t.Errorf("LoginRateLimit = %d, want %d", c.LoginRateLimit, DefaultLoginRateLimit)
	}
	if c.MemoryBudgetPct != DefaultMemoryBudgetPct {
		t.Errorf("MemoryBudgetPct = %d, want %d", c.MemoryBudgetPct, DefaultMemoryBudgetPct)
	}
	if c.DangerZone || c.AllowRootFS || c.TrustProxy || c.PProf || c.GCDryRun {
		t.Error("every widening switch must default to closed")
	}
	if len(c.Warnings) != 0 {
		t.Errorf("a clean environment produces no warnings, got %v", c.Warnings)
	}
}

// A missing encryption key is a refusal at resolution, before any side effect (data directory, trace
// file, TLS material) has happened, so a refused boot leaves nothing behind.
func TestResolveRefusesAMissingEncryptionKey(t *testing.T) {
	t.Setenv(EnvEncryptionKey, "")
	os.Unsetenv(EnvEncryptionKey)
	if _, err := Resolve(Flags{}); err == nil {
		t.Fatal("Resolve accepted a process with no encryption key")
	} else if !strings.Contains(err.Error(), EnvEncryptionKey) {
		t.Errorf("error %q must name the variable", err)
	}
}

// A listen address that resolves to nothing is refused with the variable named, rather than a
// net.Listen failure later that says only "too many colons".
func TestResolveRefusesAnUnparsableListenAddress(t *testing.T) {
	t.Setenv(EnvEncryptionKey, "a-long-enough-test-key")
	t.Setenv(EnvListen, ":nope:")
	c, err := Resolve(Flags{})
	if err == nil {
		t.Fatalf("Resolve accepted listen address %q and produced %q", ":nope:", c.Addr)
	}
	if !strings.Contains(err.Error(), EnvListen) {
		t.Errorf("error %q must name %s", err, EnvListen)
	}
}

// The registry index is a variable, not a stored column; MIKE_EXT_MOUNT_ROOTS is a path list.
func TestResolveListsAndURLs(t *testing.T) {
	t.Setenv(EnvRegistryURL, "file:///tmp/registry.json")
	t.Setenv(EnvExtMountRoots, "/srv/a"+string(os.PathListSeparator)+string(os.PathListSeparator)+" /srv/b ")
	c := mustResolve(t, Flags{})
	if c.RegistryURL != "file:///tmp/registry.json" {
		t.Errorf("RegistryURL = %q", c.RegistryURL)
	}
	want := []string{"/srv/a", "/srv/b"}
	if strings.Join(c.ExtMountRoots, ",") != strings.Join(want, ",") {
		t.Errorf("ExtMountRoots = %q, want %q (blanks and padding are not roots)", c.ExtMountRoots, want)
	}
}

// --allowed-hosts overrides MIKE_ALLOWED_HOSTS rather than extending it, and the entries arrive
// split: a middleware that re-split the raw string would be a second copy of the rule.
func TestResolveAllowedHosts(t *testing.T) {
	t.Setenv(EnvAllowedHosts, "a.example, b.example")
	c := mustResolve(t, Flags{})
	if strings.Join(c.AllowedHosts, ",") != "a.example,b.example" {
		t.Errorf("AllowedHosts = %q", c.AllowedHosts)
	}
	c = mustResolve(t, Flags{AllowedHosts: "proxy.example"})
	if strings.Join(c.AllowedHosts, ",") != "proxy.example" {
		t.Errorf("flag must replace the environment, got %q", c.AllowedHosts)
	}
}

// WeakSecrets is the boot's "this secret is shorter than we would like" line. It reports in a fixed
// order (the warning is one block the operator reads, and map order is not a thing to print in), it
// ignores an unset secret (resolution refuses the ones that must be present), and the threshold is
// the same MinSecretLength the exposed-listen refusal uses.
func TestWeakSecrets(t *testing.T) {
	tests := []struct {
		name          string
		encKey        string
		loginPassword string
		wantWeak      []string
	}{
		{name: "both strong", encKey: "strong-enough-key", loginPassword: "strong-password", wantWeak: nil},
		{name: "both weak", encKey: "short", loginPassword: "weak", wantWeak: []string{EnvEncryptionKey, EnvLoginPassword}},
		{name: "encryption key weak", encKey: "1234567", loginPassword: "strong-enough-password", wantWeak: []string{EnvEncryptionKey}},
		{name: "login password weak", encKey: "strong-enough-key-value", loginPassword: "abc", wantWeak: []string{EnvLoginPassword}},
		{name: "exactly 8 chars is ok", encKey: "12345678", loginPassword: "password", wantWeak: nil},
		{name: "exactly 7 chars is weak", encKey: "1234567", loginPassword: "passwor", wantWeak: []string{EnvEncryptionKey, EnvLoginPassword}},
		{name: "empty secrets are ignored", wantWeak: nil},
		{name: "one set one empty", encKey: "weak", wantWeak: []string{EnvEncryptionKey}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{EncryptionKey: tc.encKey, MasterPassword: tc.loginPassword}
			got := c.WeakSecrets()
			if strings.Join(got, ",") != strings.Join(tc.wantWeak, ",") {
				t.Errorf("WeakSecrets = %v, want %v", got, tc.wantWeak)
			}
		})
	}
}

// The sandbox sweep switch is not the exact-value-1 rule: MIKE_SANDBOX_SKIP_STARTUP_CLEANUP suppresses
// the sweep for ANY non-empty value, because it only ever turns the destructive half off. A truthy typo
// therefore costs a stale container rather than a live sandbox, and no boot warning is owed for it.
func TestSandboxSweepIsSuppressedByAnyNonEmptyValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		unset   bool
		wantNew bool
	}{
		{name: "(unset)", unset: true, wantNew: true}, // unset: the server sweeps, as it always has
		{name: "1", value: "1", wantNew: false},       // the documented suppression
		{name: "yes", value: "yes", wantNew: false},   // any non-empty value suppresses
		{name: "0", value: "0", wantNew: false},       // even a value that reads as "no" suppresses
		{name: "(empty)", value: "", wantNew: true},   // set-but-empty means unset, as everywhere here
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvEncryptionKey, "a-long-enough-test-key")
			if tc.unset {
				os.Unsetenv(EnvSkipSandboxSweep)
			} else {
				t.Setenv(EnvSkipSandboxSweep, tc.value)
			}
			c := mustResolve(t, Flags{})
			if c.SweepStaleContainersAtBoot != tc.wantNew {
				t.Errorf("SweepStaleContainersAtBoot = %v, want %v", c.SweepStaleContainersAtBoot, tc.wantNew)
			}
		})
	}
}

// Two spellings of one variable is how a documented flag ends up doing nothing. The consumer packages
// keep their own constants because the name appears in a refusal they print, so this is the seam that
// holds them together.
func TestEnvNamesMatchTheirOwners(t *testing.T) {
	if fsutil.AllowRootFSEnv != EnvAllowRootFS {
		t.Errorf("fsutil.AllowRootFSEnv = %q, startup says %q", fsutil.AllowRootFSEnv, EnvAllowRootFS)
	}
	if string(sandbox.DangerZoneEnv) != EnvDangerZone {
		t.Errorf("sandbox.DangerZoneEnv = %q, startup says %q", sandbox.DangerZoneEnv, EnvDangerZone)
	}
	if extensions.EnvMountRoots != EnvExtMountRoots {
		t.Errorf("extensions.EnvMountRoots = %q, startup says %q", extensions.EnvMountRoots, EnvExtMountRoots)
	}
}
