package sandboxruntime

// The environment handed to the podman CLI. Rootless podman really does read it: storage is keyed off
// the user, temporary volumes live under an XDG directory, and the systemd cgroup manager is reached
// over D-Bus. That need used to be met by leaving cmd.Env nil, which hands over the whole server
// environment including MIKE_LOGIN_PASSWORD and MIKE_ENCRYPTION_KEY.
//
// Two things to know before editing this list:
//  1. This is the podman CLIENT's environment. Nothing here reaches a container - that is
//     containerEnvArgs in backend/sandbox, with its own rule and its own container-level test.
//  2. Deleting an entry can break sandboxing in a way that reads like a podman bug. Each entry says why
//     it is needed and TestPodmanEnvKeepsWhatRootlessPodmanNeeds pins them.

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// podmanBinary is the CLI started. A variable so a test can point a subprocess at a stub, the way
// backend/git does with its own binary.
var podmanBinary = "podman"

// podmanEnvNames is the part of the server's environment the podman CLI is given.
var podmanEnvNames = map[string]bool{
	"PATH":    true, // conmon, runsc and the storage drivers are resolved through it
	"HOME":    true, // rootless storage is keyed off the user (~/.local/share/containers)
	"USER":    true,
	"LOGNAME": true,
	"TERM":    true,
	"LANG":    true,
	"TZ":      true,
	"TMPDIR":  true, // podman builds image-build contexts and overlay mounts under it
	"TMP":     true,
	"TEMP":    true,

	// The rootless trio. XDG_RUNTIME_DIR holds the per-user tmpfs that podman's temporary storage and its
	// D-Bus socket depend on; DBUS_SESSION_BUS_ADDRESS reaches the systemd user manager, the default cgroup
	// manager. Without them podman fails to start a container or silently uses a different store.
	"XDG_RUNTIME_DIR":          true,
	"XDG_CACHE_HOME":           true,
	"XDG_CONFIG_HOME":          true,
	"XDG_DATA_HOME":            true,
	"DBUS_SESSION_BUS_ADDRESS": true,
	"DBUS_SYSTEM_BUS_ADDRESS":  true,
	"XDG_SESSION_TYPE":         true,
	"NOTIFY_SOCKET":            true, // sd_notify, used when podman is socket-activated

	// Registry auth and podman's own config discovery. A pull that cannot find REGISTRY_AUTH_FILE fails on
	// a private image with "authentication required", which reads as user misconfiguration.
	"REGISTRY_AUTH_FILE":       true,
	"CONTAINERS_GRAPHROOTLESS": true,
	"DOCKER_HOST":              true,
	"CONTAINER_HOST":           true,
	"CONTAINER_SSH_URL":        true,

	// TLS trust, so an internal registry signed by a private CA still verifies.
	"SSL_CERT_FILE":        true,
	"SSL_CERT_DIR":         true,
	"NODE_EXTRA_CA_CERTS":  true,
	"SSL_CERT_FILE_PODMAN": true,

	"IN_container": true, // podman's own "am I inside a container" marker, read on nested runs
}

// proxyEnvNames are needed for the same reason SafeAgentEnv passes them: a registry reached through
// an egress proxy stops resolving without them.
var proxyEnvNames = map[string]bool{
	"HTTP_PROXY":     true,
	"HTTPS_PROXY":    true,
	"NO_PROXY":       true,
	"NO_PROXY_HOSTS": true,
	"http_proxy":     true,
	"https_proxy":    true,
	"no_proxy":       true,
}

// podmanEnvPrefixes are families allowed in bulk: podman reads its behaviour out of CONTAINERS_*,
// PODMAN_* and DOCKER_*, and enumerating its variables exactly would be wrong. Nothing secret in this
// deployment is named by them.
var podmanEnvPrefixes = []string{"CONTAINERS_", "PODMAN_", "DOCKER_", "LC_"}

// isPodmanSafeEnv reports whether an environment variable name may reach the podman CLI.
func isPodmanSafeEnv(name string) bool {
	if podmanEnvNames[name] || proxyEnvNames[name] {
		return true
	}
	for _, p := range podmanEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// PodmanEnv returns the environment for a podman subprocess: the allowlisted part of the server's
// environment, then extras (extras last, because os/exec keeps the LAST occurrence of a key).
//
// Empty values are dropped rather than passed through: podman treats an empty XDG_RUNTIME_DIR as a
// fatal "cannot find runtime directory" rather than falling back, where an absent one is handled.
func PodmanEnv(extra ...string) []string {
	env := make([]string, 0, 32+len(extra))
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || value == "" || !isPodmanSafeEnv(name) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// PodmanCommand builds the *exec.Cmd for one podman invocation, with the environment allowlist and
// PodmanWaitDelay already set. It is the podman counterpart of git.Command, and the reason is the
// same: there were five separate podman spawn points, one of which
// (RegistryClient.PullImage → `podman pull`) set no WaitDelay at all, so "every podman subprocess
// carries WaitDelay" was a convention that one call site had already broken.
//
// Callers that need a long-lived process with pipes (the sandbox bridge's `podman exec -i`) get the
// *exec.Cmd and own the process; the environment is not one of the things they may need to change.
func PodmanCommand(args ...string) *exec.Cmd {
	return configurePodman(exec.Command(podmanBinary, args...))
}

// PodmanCommandContext is PodmanCommand bounded to a context.
func PodmanCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	return configurePodman(exec.CommandContext(ctx, podmanBinary, args...))
}

// configurePodman applies the two invariants every podman subprocess must carry, from both
// constructors. It takes a built *exec.Cmd rather than a context so the context-free form can share
// it without passing a nil context.
func configurePodman(c *exec.Cmd) *exec.Cmd {
	c.Env = PodmanEnv()
	c.WaitDelay = PodmanWaitDelay
	return c
}
