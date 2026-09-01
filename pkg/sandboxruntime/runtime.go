// Package sandboxruntime discovers and drives the container runtime the sandboxes need:
// rootless podman with gVisor's runsc, where the sandbox-bridge binary is, and the startup
// sweep that removes containers a previous process left behind.
// Availability comes from one probe, so a task sandbox and an extension sandbox cannot
// disagree about whether this host can run gVisor.
package sandboxruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// imageRefPattern matches valid OCI/Docker image references.
// Requires the first character be alphanumeric (not a dash) and all
// subsequent characters be alphanumeric or one of / . _ - : @.
var imageRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._:@-]*$`)

// ValidateProjectName rejects project names that would break a container image name,
// a mount path, or a shell command: empty, a leading dash (flag injection), a control
// character, a ".." segment (the name is interpolated into the mount path
// /home/sandbox/<name>), or anything outside letters, digits, spaces and -_./.
//
// Names flow into ProjectImageName() and mount paths, so rejecting them here — the
// earliest point — is worth doing even though downstream users validate too.
func ValidateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name must not be empty")
	}
	if len(name) > 256 {
		return fmt.Errorf("project name must not exceed 256 characters")
	}
	for _, r := range name {
		if r < ' ' || r == '\x7f' {
			return fmt.Errorf("project name must not contain control characters")
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == ' ' || r == '-' || r == '_' || r == '.' || r == '/') {
			return fmt.Errorf("project name contains invalid character %q", string(r))
		}
	}
	if name[0] == '-' {
		return fmt.Errorf("project name must not start with a dash")
	}
	// The name is interpolated into the container mount path
	// (/home/sandbox/<name>), so a ".." segment would relocate the mount — and
	// with it the read-write worktree bind — elsewhere in the container. Dots
	// inside a segment ("my..project") stay fine; only whole segments count.
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return fmt.Errorf("project name must not contain '..' path segments")
		}
	}
	return nil
}

// ValidateImageRef rejects image references with characters that are invalid in an OCI
// image ref, so a reference like "-v /etc:/host" or "$(rm -rf /)" never reaches podman
// as a flag.
//
// Single source of truth: the sandbox Runtime method and the extensions RegistryClient
// both delegate here, so the two cannot drift into different rules.
func ValidateImageRef(image string) error {
	if image == "" {
		return fmt.Errorf("image reference must not be empty")
	}
	if !imageRefPattern.MatchString(image) {
		return fmt.Errorf("invalid image reference: %q", image)
	}
	return nil
}

// ValidateImageRef is the Runtime-bound form of the package-level ValidateImageRef
// — same rules, same single implementation.
func (r *Runtime) ValidateImageRef(image string) error {
	return ValidateImageRef(image)
}

// ValidateDockerfilePath rejects paths that start with a dash,
// preventing them from being interpreted as podman flags.
func ValidateDockerfilePath(path string) error {
	if path == "" {
		return fmt.Errorf("dockerfile path must not be empty")
	}
	if strings.HasPrefix(path, "-") {
		return fmt.Errorf("dockerfile path must not start with a dash: %q", path)
	}
	return nil
}

// Runtime provides low-level Podman and gVisor operations used by sandbox managers.
type Runtime struct{}

// NewRuntime creates a new Sandbox Runtime.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// PodmanExec runs a podman command and returns the combined output.
func (r *Runtime) PodmanExec(args ...string) ([]byte, error) {
	c := exec.Command("podman", args...)
	return c.CombinedOutput()
}

// PodmanExecContext is like PodmanExec but bounds execution to the context's
// deadline. Use it for best-effort calls (e.g. startup cleanup) where a wedged
// podman must not block the caller indefinitely.
func (r *Runtime) PodmanExecContext(ctx context.Context, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "podman", args...)
	return c.CombinedOutput()
}

// RemoveContainer force-removes a container, handling overlay filesystem issues
// that can block normal podman rm -f in rootless mode.
func (r *Runtime) RemoveContainer(name string) error {
	_, err := r.PodmanExec("rm", "-f", name)
	if err == nil {
		return nil
	}
	// Overlay merged dirs can have stale files that block rm -f.
	r.cleanOverlayMergedDirs()
	_, err = r.PodmanExec("rm", "-f", name)
	if err == nil {
		return nil
	}
	return fmt.Errorf("podman rm -f %s: %w", name, err)
}

// cleanOverlayMergedDirs removes stale files from podman overlay merged directories.
func (r *Runtime) cleanOverlayMergedDirs() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	overlayBase := filepath.Join(home, ".local", "share", "containers", "storage", "overlay")
	entries, err := os.ReadDir(overlayBase)
	if err != nil {
		return
	}
	for _, entry := range entries {
		mergedDir := filepath.Join(overlayBase, entry.Name(), "merged")
		files, _ := os.ReadDir(mergedDir)
		for _, f := range files {
			_ = os.RemoveAll(filepath.Join(mergedDir, f.Name()))
		}
	}
}

// staleCleanupTimeout bounds the startup sweep for containers left behind by a
// previous run. A wedged podman must never block server startup.
const staleCleanupTimeout = 15 * time.Second

// GVisorAvailable reports whether gVisor (runsc) and podman are both usable, and
// logs what it found under logTag.
//
// This is the only gVisor availability probe: the task sandbox manager and the
// extension sandbox manager both call here, so they cannot disagree about whether
// the host can run a gVisor container at all, and both fail closed the same way.
func GVisorAvailable(logTag string) bool {
	if _, err := exec.LookPath("runsc"); err != nil {
		log.Printf("[%s] runsc not found in PATH: %v", logTag, err)
		return false
	}
	out, err := exec.Command("runsc", "--version").CombinedOutput()
	if err != nil {
		log.Printf("[%s] runsc version check failed: %v (%s)", logTag, err, out)
		return false
	}
	log.Printf("[%s] runsc version: %s", logTag, strings.TrimSpace(string(out)))
	if _, err := exec.LookPath("podman"); err != nil {
		log.Printf("[%s] podman not found in PATH: %v", logTag, err)
		return false
	}
	return true
}

// SkipStartupCleanup reports whether the startup sweep over leftover sandbox containers
// should be skipped. MIKE_SANDBOX_SKIP_STARTUP_CLEANUP is set by test binaries (see the
// TestMain of the sandbox suites) so that running a unit test never reaches into the host
// that happens to be running a server: without it, `go test ./backend/...` swept every
// kanban-* container family on the machine, including the live server's language servers.
func SkipStartupCleanup() bool {
	return os.Getenv("MIKE_SANDBOX_SKIP_STARTUP_CLEANUP") != ""
}

// staleContainerNames keeps the containers a startup sweep may reclaim: everything not running.
//
// The sweep matches a whole name family by prefix, so removing a running container takes a working
// sandbox away from the server still talking to it; an unreadable state stays in the sweep, which
// is what a half-dead name-holder looks like.
func staleContainerNames(names []string, isRunning func(string) (bool, error)) []string {
	var stale []string
	for _, name := range names {
		if running, err := isRunning(name); err == nil && running {
			continue
		}
		stale = append(stale, name)
	}
	return stale
}

// CleanupStaleContainers force-removes every non-running container whose name matches podman's
// nameFilter (a regex, so a bare prefix matches the whole family), best-effort and time-bounded.
// Both sandbox managers need it on startup, so the loop exists once here; running containers stay,
// per staleContainerNames.
func (r *Runtime) CleanupStaleContainers(logTag, nameFilter string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), staleCleanupTimeout)
	defer cancel()

	out, err := r.PodmanExecContext(ctx, "ps", "-a", "--filter", "name="+nameFilter, "--format", "{{.Names}}")
	if err != nil {
		log.Printf("[%s] startup: failed to list stale containers: %v", logTag, err)
		return nil
	}
	var removed []string
	for _, name := range staleContainerNames(strings.Fields(strings.TrimSpace(string(out))), r.IsRunning) {
		if rmErr := r.RemoveContainer(name); rmErr != nil {
			log.Printf("[%s] startup: failed to remove stale container %s: %v", logTag, name, rmErr)
			continue
		}
		log.Printf("[%s] startup: removed stale container %s", logTag, name)
		removed = append(removed, name)
	}
	return removed
}

// ContainerExists checks if a container with the given name exists.
func (r *Runtime) ContainerExists(name string) (bool, error) {
	_, err := r.PodmanExec("inspect", "-t", "container", name)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// IsRunning checks if a container is actually running in podman.
func (r *Runtime) IsRunning(name string) (bool, error) {
	out, err := r.PodmanExec("inspect", "-f", "{{.State.Running}}", name)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// GetContainerMountSource returns the host source path for the bind mount whose
// destination (path inside the container) equals dest. It returns an empty string
// if no matching mount is found, and an error only if the container cannot be
// inspected. This is used to detect stale containers whose worktree bind mount no
// longer matches the current project path.
func (r *Runtime) GetContainerMountSource(name, dest string) (string, error) {
	out, err := r.PodmanExec("inspect", "-f", "{{json .Mounts}}", name)
	if err != nil {
		return "", err
	}
	var mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	}
	if err := json.Unmarshal(out, &mounts); err != nil {
		return "", fmt.Errorf("parse container mounts: %w", err)
	}
	cleanDest := filepath.Clean(dest)
	for _, m := range mounts {
		if m.Type == "bind" && filepath.Clean(m.Destination) == cleanDest {
			return m.Source, nil
		}
	}
	return "", nil
}

// GetAgentBinaryPath resolves the path to the sandbox-bridge binary.
func (r *Runtime) GetAgentBinaryPath() (string, error) {
	if p := os.Getenv("MIKE_AGENT_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	for _, name := range []string{"sandbox-bridge", "sandbox/sandbox-bridge"} {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	for _, name := range []string{"sandbox-bridge", "sandbox/sandbox-bridge"} {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("sandbox-bridge binary not found")
}

// SanitizeID ensures the ID is a valid podman container name.
func (r *Runtime) SanitizeID(id string) (string, error) {
	var result strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}
	s := result.String()
	s = strings.Trim(s, "-_.")
	for len(s) > 0 && !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= '0' && s[0] <= '9')) {
		s = s[1:]
	}
	if s == "" {
		return "", fmt.Errorf("ID %q contains no valid container name characters", id)
	}
	return s, nil
}

// cgroupRoot is the mount point of the cgroup v2 (unified) hierarchy.
const cgroupRoot = "/sys/fs/cgroup"

// parseUnifiedCgroupPath extracts the cgroup v2 (unified hierarchy) path from
// /proc/<pid>/cgroup content. On cgroup v2 hosts the file contains a line of
// the form "0::<path>" (e.g. "0::/user.slice/.../libpod-conmon-<id>.scope").
// Returns an error when no unified line is present (pure cgroup v1 host).
func parseUnifiedCgroupPath(contents string) (string, error) {
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			if path == "" {
				return "/", nil // process is in the cgroup root
			}
			return path, nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 (unified) entry found")
}

// ContainerCgroupDir returns the host cgroup v2 directory of the container's
// init process (e.g. /sys/fs/cgroup/user.slice/.../libpod-conmon-<id>.scope).
// The container must be running.
func (r *Runtime) ContainerCgroupDir(name string) (string, error) {
	out, err := r.PodmanExec("inspect", "-f", "{{.State.Pid}}", name)
	if err != nil {
		return "", fmt.Errorf("inspect container %s: %w", name, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("container %s is not running (pid %q)", name, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", fmt.Errorf("read cgroup for pid %d: %w", pid, err)
	}
	path, err := parseUnifiedCgroupPath(string(data))
	if err != nil {
		return "", fmt.Errorf("container %s: %w", name, err)
	}
	if !filepath.IsAbs(path) {
		path = "/" + path
	}
	return filepath.Join(cgroupRoot, filepath.Clean(path)), nil
}

// CgroupMemory holds memory statistics read from a cgroup v2 memory controller.
type CgroupMemory struct {
	CurrentBytes int64  // memory.current — current usage
	PeakBytes    int64  // memory.peak — high-water mark (0 when the file is absent)
	MaxBytes     int64  // memory.max — limit in bytes (0 when unlimited ("max") or absent)
	OOMKills     uint64 // oom_kill counter from memory.events
}

// ReadCgroupMemory reads the memory controller statistics from a cgroup v2
// directory. Optional files are tolerated: memory.peak does not exist on older
// kernels (reported as 0), and a missing memory.max/memory.events simply yields
// zero/absent values. A missing or malformed memory.current — the one file
// that must be present in a live cgroup — is an error.
func ReadCgroupMemory(dir string) (CgroupMemory, error) {
	var m CgroupMemory

	cur, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return m, fmt.Errorf("read memory.current: %w", err)
	}
	m.CurrentBytes, err = strconv.ParseInt(strings.TrimSpace(string(cur)), 10, 64)
	if err != nil {
		return m, fmt.Errorf("parse memory.current: %w", err)
	}

	if peak, err := os.ReadFile(filepath.Join(dir, "memory.peak")); err == nil {
		if p, perr := strconv.ParseInt(strings.TrimSpace(string(peak)), 10, 64); perr == nil {
			m.PeakBytes = p
		}
	}

	// A numeric memory.max is the enforced limit; "max" (unlimited) and a
	// missing file both leave MaxBytes at 0 — the only distinction downstream
	// needs is "limit known" vs. "no enforced limit".
	if maxData, err := os.ReadFile(filepath.Join(dir, "memory.max")); err == nil {
		if s := strings.TrimSpace(string(maxData)); s != "max" {
			if v, verr := strconv.ParseInt(s, 10, 64); verr == nil {
				m.MaxBytes = v
			}
		}
	}

	if events, err := os.ReadFile(filepath.Join(dir, "memory.events")); err == nil {
		for _, line := range strings.Split(string(events), "\n") {
			if count, ok := strings.CutPrefix(strings.TrimSpace(line), "oom_kill "); ok {
				if n, nerr := strconv.ParseUint(strings.TrimSpace(count), 10, 64); nerr == nil {
					m.OOMKills = n
				}
				break
			}
		}
	}

	return m, nil
}

// ApplyMemoryLimit enforces a hard memory limit on a running container by writing into its
// cgroup. Necessary because runsc - which needs --runtime-flag ignore-cgroups under rootless -
// ignores the OCI spec's linux.resources, so podman's --memory alone is never enforced.
//
// Writes memory.max = limitMB MiB and, best-effort, memory.swap.max = 0: with swap available the
// host reclaims the overshoot instead of OOM-killing, which makes the limit soft. On error the
// caller decides whether to fail open or closed.
func (r *Runtime) ApplyMemoryLimit(name string, limitMB int) error {
	if limitMB <= 0 {
		return fmt.Errorf("memory limit must be positive, got %d", limitMB)
	}
	dir, err := r.ContainerCgroupDir(name)
	if err != nil {
		return err
	}
	maxBytes := int64(limitMB) << 20
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(maxBytes, 10)), 0); err != nil {
		return fmt.Errorf("set memory.max in %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("0"), 0); err != nil {
		// Non-fatal: hosts without a swap controller still enforce memory.max
		// as a hard cap (there is no swap to absorb the overshoot).
		log.Printf("[sandboxruntime] %s: could not set memory.swap.max=0 (swap not isolated): %v", name, err)
	}
	return nil
}

// ClearMemoryLimit removes any memory limit from a running container by
// writing "max" into its cgroup.
//
// Returns an error if the limit cannot be cleared; the caller decides whether
// to fail open (sandbox keeps its old limit) or fail closed.
func (r *Runtime) ClearMemoryLimit(name string) error {
	dir, err := r.ContainerCgroupDir(name)
	if err != nil {
		return err
	}
	return clearCgroupMemoryLimit(dir)
}

// clearCgroupMemoryLimit writes "max" into the cgroup's memory.max and
// memory.swap.max files, removing any memory limit.
func clearCgroupMemoryLimit(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("max"), 0); err != nil {
		return fmt.Errorf("clear memory.max in %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("max"), 0); err != nil {
		log.Printf("[sandboxruntime] could not set memory.swap.max=max in %s: %v", dir, err)
	}
	return nil
}
