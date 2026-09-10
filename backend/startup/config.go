// Package startup resolves every process-level setting once, flag then environment then default.
package startup

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variable names. The consumer packages keep their own constants where the name is part
// of a user-facing refusal (fsutil.AllowRootFSEnv, sandbox.DangerZoneEnv, extensions.EnvMountRoots);
// TestEnvNamesMatchTheirOwners holds this copy and those together, because two spellings of one
// variable drift in exactly the way that ends with a documented flag doing nothing.
const (
	EnvPort             = "MIKE_PORT"
	EnvListen           = "MIKE_LISTEN"
	EnvDataDir          = "MIKE_DATA_DIR"
	EnvWorktreesDir     = "MIKE_WORKTREES_DIR"
	EnvFrontendDist     = "MIKE_FRONTEND_DIST"
	EnvTLS              = "MIKE_TLS"
	EnvAllowedHosts     = "MIKE_ALLOWED_HOSTS"
	EnvLogLevel         = "MIKE_LOG_LEVEL"
	EnvLoginPassword    = "MIKE_LOGIN_PASSWORD"
	EnvEncryptionKey    = "MIKE_ENCRYPTION_KEY"
	EnvRegistryURL      = "MIKE_REGISTRY_URL"
	EnvExtMountRoots    = "MIKE_EXT_MOUNT_ROOTS"
	EnvAgentPath        = "MIKE_AGENT_PATH"
	EnvSkipSandboxSweep = "MIKE_SANDBOX_SKIP_STARTUP_CLEANUP"
	EnvTrustProxy       = "MIKE_TRUST_PROXY"
	EnvLoginRateLimit   = "MIKE_LOGIN_RATE_LIMIT"
	EnvAllowRootFS      = "MIKE_ALLOW_ROOT_FS"
	EnvDangerZone       = "DANGER_ZONE"
	EnvGCDryRun         = "MIKE_GC_DRY_RUN"
	EnvPProf            = "MIKE_PPROF"
)

// DefaultPort and DefaultListenAddr are the built-in defaults of the ladder. Loopback by default
// because the API is RCE-complete (terminal WebSocket, git operations, file writes) and must never
// land on a public interface by accident; an explicit 0.0.0.0 / :: is the deliberate escape hatch.
const (
	DefaultPort       = 3000
	DefaultListenAddr = "127.0.0.1"
	DefaultDataDir    = "./data"
	DefaultFrontend   = "./frontend/dist"
	defaultLogLevel   = "info"

	// DefaultLoginRateLimit is the login budget per client IP per minute. The limiter cannot be
	// turned off: 0, negative and unparsable values fall back to this with a warning, so a typo
	// cannot silently drop brute-force protection.
	DefaultLoginRateLimit = 5

	// MinSecretLength is both the minimum master password length accepted on an exposed
	// (non-loopback) listen address and the length below which a secret is reported as weak at boot.
	MinSecretLength = 8
)

// weakPasswords are master passwords that must never guard a non-loopback listen address.
var weakPasswords = map[string]struct{}{
	"test": {}, "dev": {}, "admin": {}, "password": {}, "pass": {}, "changeme": {},
}

// Flags carries what the command line provided. The zero value means "not given", which is what makes
// flag > environment > default expressible: an empty string and a 0 int are the absences, not values.
// A flag value that is present but invalid is still an error, never a silent fall through to the
// environment.
type Flags struct {
	Port         int
	Listen       string
	DataDir      string
	WorktreesDir string
	NoTLS        bool
	TLSCertPath  string
	TLSKeyPath   string
	AllowedHosts string
	LogLevel     string
}

// Config is written once at resolution and never again: the answer a request gets does not depend on
// when that request arrived.
type Config struct {
	Port    int
	Listen  string // address or host, as given
	Addr    string // host:port, ready to dial or listen
	DataDir string
	DBPath  string
	// WorktreesOverride is a runtime override for the git worktrees directory and is deliberately
	// never persisted to the DB workspace_dir.
	WorktreesOverride string
	FrontendDist      string

	TLSDisabled bool
	TLSCertPath string
	TLSKeyPath  string

	LogLevel string

	// AllowedHosts are the *extra* Host values accepted beyond localhost and the listen host; the
	// rest of the pinning rule lives in the middleware that reads this.
	AllowedHosts []string

	MasterPassword string
	EncryptionKey  string
	RegistryURL    string

	// ExtMountRoots are the extra host roots an extension sandbox may bind-mount, beyond the
	// implicit ones. Still an allowlist.
	ExtMountRoots []string
	// AgentPath overrides where the sandbox-bridge binary is found, for a host where it does not sit
	// beside the server executable.
	AgentPath string
	// SweepStaleContainersAtBoot is the server's claim on the host's container namespace: the boot
	// sweep deletes stale kanban-* container families, which is a host-wide delete.
	// MIKE_SANDBOX_SKIP_STARTUP_CLEANUP suppresses it for **any** non-empty value, because a test
	// binary must never sweep the host it runs on. That is deliberately not the exact-value-1 rule the
	// widening switches use: this variable only ever turns the destructive half OFF, so a truthy typo
	// costs one stale container rather than a live sandbox.
	SweepStaleContainersAtBoot bool

	TrustProxy bool
	// LoginRateLimit is requests per minute per client IP. Always > 0.
	LoginRateLimit int

	// DangerZone is the operator's permission to turn sandboxing off. It is not a permission to
	// execute anywhere: a project's sandbox_enabled column answers that.
	DangerZone bool
	// AllowRootFS widens the host-directory jail of the no-project file endpoints.
	AllowRootFS bool
	// GCDryRun reports the branch GC's decisions without performing them.
	GCDryRun bool
	// PProf serves net/http/pprof on localhost:6060. A profile can contain API keys and passwords,
	// so it is off unless asked for by name.
	PProf bool

	// Warnings are the operator-facing notes resolution produced: values that were refused *softly*
	// (a switch set to something that is not its opt-in value, a budget that fell back to the
	// default). They are collected here so the boot says them once, in one place, instead of a
	// handler noticing the typo on the request that happened to look.
	Warnings []string
}

// Resolve runs the ladder: flag, then environment, then default. Every failure names the variable or
// flag it came from, because the alternative is a server configured differently from what the
// operator typed. It performs no side effect, so a refusal leaves nothing behind.
func Resolve(f Flags) (Config, error) {
	c := Config{}
	var err error

	c.Port, err = resolvePort(f.Port, "--port", EnvPort, DefaultPort)
	if err != nil {
		return Config{}, err
	}
	c.Listen = resolveString(f.Listen, EnvListen, DefaultListenAddr)
	c.Addr = listenAddr(c.Listen, c.Port)
	if _, err := net.ResolveTCPAddr("tcp", c.Addr); err != nil {
		return Config{}, fmt.Errorf("invalid listen address %q (from --listen / %s): %w", c.Addr, EnvListen, err)
	}

	c.DataDir = resolveString(f.DataDir, EnvDataDir, DefaultDataDir)
	c.DBPath = c.DataDir + string(os.PathSeparator) + "kanban.db"
	c.WorktreesOverride = resolveString(f.WorktreesDir, EnvWorktreesDir, "")
	c.FrontendDist = resolveString("", EnvFrontendDist, DefaultFrontend)

	// --no-tls and MIKE_TLS=0 both disable TLS; MIKE_TLS is the env spelling of the same switch, so
	// only the exact "0" means off (an unset or garbage value keeps TLS on).
	c.TLSDisabled = f.NoTLS || os.Getenv(EnvTLS) == "0"
	c.TLSCertPath = f.TLSCertPath
	c.TLSKeyPath = f.TLSKeyPath

	c.LogLevel = resolveString(f.LogLevel, EnvLogLevel, defaultLogLevel)

	// The flag replaces the environment rather than extending it: a stale variable must not widen the
	// allowlist of a deployment that switched to the flag.
	c.AllowedHosts = splitCSV(resolveString(f.AllowedHosts, EnvAllowedHosts, ""))

	c.MasterPassword = os.Getenv(EnvLoginPassword)
	c.EncryptionKey = os.Getenv(EnvEncryptionKey)
	if c.EncryptionKey == "" {
		return Config{}, fmt.Errorf("%s environment variable is not set", EnvEncryptionKey)
	}
	// Empty means "not given": the hosted index is the config package's default (config.DefaultRegistryURL),
	// and this package does not duplicate the URL.
	c.RegistryURL = resolveString("", EnvRegistryURL, "")
	c.ExtMountRoots = splitPathList(os.Getenv(EnvExtMountRoots))
	c.AgentPath = resolveString("", EnvAgentPath, "")
	c.SweepStaleContainersAtBoot = os.Getenv(EnvSkipSandboxSweep) == ""

	c.TrustProxy = isOne(EnvTrustProxy)
	c.DangerZone = isOne(EnvDangerZone)
	c.AllowRootFS = isOne(EnvAllowRootFS)
	c.GCDryRun = isOne(EnvGCDryRun)
	c.PProf = isOne(EnvPProf)

	c.LoginRateLimit = resolveRateLimit(os.Getenv(EnvLoginRateLimit), &c.Warnings)

	// A switch set to anything but "1" stays closed, and says so: an operator who typed
	// MIKE_ALLOW_ROOT_FS=true and got the jail would otherwise discover it by being refused.
	for _, name := range []string{EnvAllowRootFS, EnvDangerZone, EnvTrustProxy, EnvGCDryRun, EnvPProf} {
		if v := os.Getenv(name); v != "" && v != "1" {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s=%q is not the exact value 1, so it does nothing; set %s=1 to enable it or unset it to keep the current behaviour", name, v, name))
		}
	}
	sortNames(c.Warnings)
	return c, nil
}

// RefusesWeakSecret reports whether startup must abort: a weak MIKE_LOGIN_PASSWORD
// (shorter than MinSecretLength, or a well-known default) is fatal unless the
// server listens on loopback only. Loopback + weak stays allowed so the dev flow works.
func (c Config) RefusesWeakSecret() bool {
	if IsLoopbackAddr(c.Listen) {
		return false
	}
	pw := strings.TrimSpace(c.MasterPassword)
	if len(pw) < MinSecretLength {
		return true
	}
	return isKnownWeak(strings.ToLower(pw))
}

// WeakSecrets lists the secret variables that are set but shorter than the recommended length, so the
// operator hears about it at boot rather than in an incident. An unset secret is not "weak" — the boot
// already refused the ones that must be present. The result is sorted because it is printed as one
// block, and map order is not a thing to print in.
func (c Config) WeakSecrets() []string {
	var weak []string
	for _, s := range []struct {
		name  string
		value string
	}{{EnvEncryptionKey, c.EncryptionKey}, {EnvLoginPassword, c.MasterPassword}} {
		if s.value != "" && len(s.value) < MinSecretLength {
			weak = append(weak, s.name)
		}
	}
	sortNames(weak)
	return weak
}

// resolveString returns the first non-empty of the flag value, the named environment variable, and
// the fallback. A set-but-empty variable counts as unset: for MIKE_LISTEN the two readings of an
// empty value are "loopback" and "every interface", so the safe one wins.
func resolveString(flagValue, envName, fallback string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return fallback
}

// resolvePort resolves a TCP port the same way. flagName is only used to name the source in an
// error; a flag value is range-checked too, since --port 99999 parses and binds nothing.
func resolvePort(flagValue int, flagName, envName string, fallback int) (int, error) {
	if flagValue != 0 {
		if err := validatePort(flagValue, flagName); err != nil {
			return 0, err
		}
		return flagValue, nil
	}
	raw := os.Getenv(envName)
	if raw == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a port number", envName, raw)
	}
	if err := validatePort(port, envName); err != nil {
		return 0, err
	}
	return port, nil
}

func validatePort(port int, what string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: port %d is outside 1-65535", what, port)
	}
	return nil
}

// resolveRateLimit accepts a positive integer budget and refuses everything else *softly*: the
// protection must survive a typo, so a bad value warns into *warnings and uses the default.
func resolveRateLimit(raw string, warnings *[]string) int {
	if raw == "" {
		return DefaultLoginRateLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		*warnings = append(*warnings, fmt.Sprintf(
			"%s=%q is not a positive integer; using the default of %d requests/minute (login rate limiting cannot be disabled)",
			EnvLoginRateLimit, raw, DefaultLoginRateLimit))
		return DefaultLoginRateLimit
	}
	return n
}

// isOne is the shape of an opt-in switch: only the exact value "1" turns it on. A truthy-looking
// typo (true, yes, on) is not the opt-in it was meant to be, and a set-but-empty value keeps the
// safe behaviour.
func isOne(envName string) bool { return os.Getenv(envName) == "1" }

// listenAddr builds the server listen address from a listen address and port.
// An empty address means loopback; "0.0.0.0"/"::" are the explicit
// all-interfaces escape hatch (a bare ":port" covers both families).
func listenAddr(listen string, port int) string {
	switch listen {
	case "":
		return net.JoinHostPort(DefaultListenAddr, strconv.Itoa(port))
	case "0.0.0.0", "::":
		return fmt.Sprintf(":%d", port)
	default:
		return net.JoinHostPort(listen, strconv.Itoa(port))
	}
}

// IsLoopbackAddr reports whether a listen address accepts connections from the
// loopback interface only. "" is the default (loopback); an explicit
// "0.0.0.0"/"::" or any routable address is not.
func IsLoopbackAddr(listen string) bool {
	b := strings.TrimSpace(listen)
	if b == "" || strings.EqualFold(b, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(b, "[]"))
	return ip != nil && ip.IsLoopback()
}

func isKnownWeak(password string) bool {
	_, known := weakPasswords[password]
	return known
}

// splitCSV parses a comma-separated list, dropping blanks and padding. The splitting lives here so a
// consumer receives entries, not a string each of them re-splits its own way.
func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitPathList splits an os.PathList-separated value, dropping blanks and padding.
func splitPathList(raw string) []string {
	var out []string
	for _, part := range filepath.SplitList(raw) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sortNames is an insertion sort over the two-name list WeakSecrets can return. Sorting keeps the boot
// warning's order stable without importing "sort" for two elements.
func sortNames(names []string) {
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}
