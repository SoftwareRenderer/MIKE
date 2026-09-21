// Package startup resolves every process-level setting once, flag then environment then default.
package startup

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environment variable names. Every read of a variable happens here; a consumer keeps a constant of
// its own only where the name is part of a refusal it prints (fsutil.AllowRootFSEnv,
// updates.EnvDisableUpdates), and TestEnvNamesMatchTheirOwners holds this copy and those together,
// because two spellings of one variable drift in exactly the way that ends with a documented flag
// doing nothing.
const (
	EnvPort           = "MIKE_PORT"
	EnvListen         = "MIKE_LISTEN"
	EnvDataDir        = "MIKE_DATA_DIR"
	EnvWorktreesDir   = "MIKE_WORKTREES_DIR"
	EnvFrontendDist   = "MIKE_FRONTEND_DIST"
	EnvTLS            = "MIKE_TLS"
	EnvAllowedHosts   = "MIKE_ALLOWED_HOSTS"
	EnvLogLevel       = "MIKE_LOG_LEVEL"
	EnvLoginPassword  = "MIKE_LOGIN_PASSWORD"
	EnvEncryptionKey  = "MIKE_ENCRYPTION_KEY"
	EnvRegistryURL    = "MIKE_REGISTRY_URL"
	EnvUpdateURL      = "MIKE_UPDATE_URL"
	EnvDisableUpdates = "MIKE_DISABLE_UPDATES"
	// EnvUpdateCheckDeprecated is read by nothing: it is the tri-state this variable replaced, named
	// only so an operator whose .env still sets it is told at boot.
	EnvUpdateCheckDeprecated = "MIKE_UPDATE_CHECK"
	EnvExtMountRoots         = "MIKE_EXT_MOUNT_ROOTS"
	EnvAgentPath             = "MIKE_AGENT_PATH"
	EnvSkipSandboxSweep      = "MIKE_SANDBOX_SKIP_STARTUP_CLEANUP"
	EnvTrustProxy            = "MIKE_TRUST_PROXY"
	EnvLoginRateLimit        = "MIKE_LOGIN_RATE_LIMIT"
	EnvAllowRootFS           = "MIKE_ALLOW_ROOT_FS"
	EnvDangerZone            = "DANGER_ZONE"
	EnvGCDryRun              = "MIKE_GC_DRY_RUN"
	EnvPProf                 = "MIKE_PPROF"
	EnvMemoryBudgetPct       = "MIKE_MEMORY_BUDGET_PCT"
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

	// DefaultMemoryBudgetPct is the share of installed RAM the memory budget admits against. The
	// numerator is what MIKE's own containers and processes cost, so the percentage is MIKE's share
	// of the machine rather than a margin kept over everyone else's usage.
	DefaultMemoryBudgetPct = 90
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
	FrontendDist string
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
	// FrontendDist is the directory a development build serves the SPA from. Empty means serve the
	// copy compiled into this binary, which is what every `-tags release` build resolves to.
	FrontendDist string

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

	// UpdateManifestURL is where the release manifest is read from. Empty means the hosted default.
	UpdateManifestURL string
	// DisableUpdates is the organization-level kill switch for the server's own release updates: no
	// check, no download, no install, no restart, and no prompt about any of them. It outranks the
	// stored preference and cannot be answered from a browser.
	DisableUpdates bool

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

	// MemoryBudgetPct is the percentage of installed RAM the memory budget may be spent out of.
	// Always 1-100; the gate multiplies it by MemTotal once, at startup.
	MemoryBudgetPct int

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

	// A `-tags release` build serves the copy it carries, so it refuses both spellings rather than
	// accepting and ignoring them.
	if !frontendDirAllowed {
		if f.FrontendDist != "" || os.Getenv(EnvFrontendDist) != "" {
			return Config{}, fmt.Errorf("--frontend-dist and %s are not available in this build: it serves the frontend it was compiled with", EnvFrontendDist)
		}
	} else {
		c.FrontendDist = resolveString(f.FrontendDist, EnvFrontendDist, DefaultFrontend)
	}

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
	// The manifest is the channel a newer binary is announced over, so its scheme is checked at boot.
	c.UpdateManifestURL, err = resolveUpdateURL(os.Getenv(EnvUpdateURL))
	if err != nil {
		return Config{}, err
	}
	c.DisableUpdates = resolveDisable(EnvDisableUpdates, &c.Warnings)
	if stale := os.Getenv(EnvUpdateCheckDeprecated); stale != "" {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%s=%q is no longer read: release updates are turned off with %s=1, and are otherwise decided in Settings → About",
			EnvUpdateCheckDeprecated, stale, EnvDisableUpdates))
	}
	c.ExtMountRoots = splitPathList(os.Getenv(EnvExtMountRoots))
	c.AgentPath = resolveString("", EnvAgentPath, "")
	c.SweepStaleContainersAtBoot = os.Getenv(EnvSkipSandboxSweep) == ""

	c.TrustProxy = isOne(EnvTrustProxy)
	c.DangerZone = isOne(EnvDangerZone)
	c.AllowRootFS = isOne(EnvAllowRootFS)
	c.GCDryRun = isOne(EnvGCDryRun)
	c.PProf = isOne(EnvPProf)

	c.LoginRateLimit = resolveRateLimit(os.Getenv(EnvLoginRateLimit), &c.Warnings)

	// A budget percentage that does not parse would silently decide how much of the machine MIKE
	// takes, so it is refused outright rather than warned into the default.
	c.MemoryBudgetPct, err = resolvePercent(os.Getenv(EnvMemoryBudgetPct))
	if err != nil {
		return Config{}, err
	}

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

// resolvePercent resolves a 1-100 percentage of installed RAM. An unset or empty variable is the
// default; anything else that does not parse, or parses outside the range, refuses the boot: the
// number decides how much of the machine MIKE may occupy, and a typo would decide it silently.
func resolvePercent(raw string) (int, error) {
	if raw == "" {
		return DefaultMemoryBudgetPct, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a percentage", EnvMemoryBudgetPct, raw)
	}
	if n < 1 || n > 100 {
		return 0, fmt.Errorf("%s: %d is outside 1-100", EnvMemoryBudgetPct, n)
	}
	return n, nil
}

// isOne is the shape of an opt-in switch: only the exact value "1" turns it on. A truthy-looking
// typo (true, yes, on) is not the opt-in it was meant to be, and a set-but-empty value keeps the
// safe behaviour.
func isOne(envName string) bool { return os.Getenv(envName) == "1" }

// resolveDisable reads a switch whose only job is to turn a capability off. It follows the opt-in shape —
// only the exact value "1" disables, a set-but-empty value means unset — and says out loud when a
// truthy-looking value disabled nothing, because that is the failure this switch exists to prevent. "0" is
// the switch's own off spelling, so it disables nothing quietly.
func resolveDisable(envName string, warnings *[]string) bool {
	raw := os.Getenv(envName)
	switch raw {
	case "", "0":
		return false
	case "1":
		return true
	}
	*warnings = append(*warnings, fmt.Sprintf("%s=%q is not the exact value 1, so it disables nothing; set it to 1 to disable, or unset it to leave the decision to Settings", envName, raw))
	return false
}

// resolveUpdateURL accepts the release manifest's location. HTTPS is required because the manifest is the
// channel a newer binary is announced over; plain HTTP only on loopback.
func resolveUpdateURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%s=%q is not a URL", EnvUpdateURL, raw)
	}
	host := u.Hostname()
	loopback := host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
	switch {
	case u.Scheme == "https":
		return raw, nil
	case u.Scheme == "http" && loopback:
		return raw, nil
	default:
		return "", fmt.Errorf("%s=%q must be https (http is accepted only on loopback)", EnvUpdateURL, raw)
	}
}

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
