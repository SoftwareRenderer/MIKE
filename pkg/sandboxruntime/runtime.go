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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mike/pkg/logging"

	"mike/pkg/subproc"
)

// imageRefPattern matches valid OCI/Docker image references.
// Requires the first character be alphanumeric (not a dash) and all
// subsequent characters be alphanumeric or one of / . _ - : @.
var imageRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._:@-]*$`)

// ValidateProjectName rejects project names that would break a container image name,
// a mount path, or a shell command: empty, a leading dash (flag injection), a control
// character, a ".." segment or a name that names no directory (both move the mount,
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
	// MountPath is Join(mountRoot, name), so a name that cleans to nothing makes this project's
	// mount the shared root instead of a directory below it. A "." segment inside a longer name is
	// collapsed by Join and stays allowed.
	switch filepath.Clean(name) {
	case ".", "/":
		return fmt.Errorf("project name %q names no directory of its own", name)
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

// Boot is how the process started, as far as the container runtime is concerned. Both fields are
// decided once at startup (backend/startup) and passed down, because each one controls something a
// per-call re-read gets wrong: whether this process may delete the host's leftover sandbox containers,
// and where the bridge binary is found.
type Boot struct {
	// SweepStaleContainersAtBoot claims the host's container namespace: it lets the boot sweep delete
	// every stale kanban-* container family. That is a host-wide delete, so the zero value **declines**
	// it. A test binary or an ad-hoc caller that swept would take a live server's language servers with
	// it — the failure MIKE_SANDBOX_SKIP_STARTUP_CLEANUP existed to prevent, now prevented by default
	// rather than by every suite remembering to set a variable.
	SweepStaleContainersAtBoot bool
	// AgentPath overrides where the sandbox-bridge binary is looked for. Empty searches beside the
	// executable, which is where `make bridge` puts it.
	AgentPath string
}

// Runtime provides low-level Podman and gVisor operations used by sandbox managers. It is built with a
// Boot, so the decisions about the host are made once, where the process started.
type Runtime struct {
	boot Boot
}

// NewRuntimeWithBoot creates a Runtime with the boot decisions this process was started with.
func NewRuntimeWithBoot(boot Boot) *Runtime {
	return &Runtime{boot: boot}
}

// Boot returns the decisions this Runtime was built with.
func (r *Runtime) Boot() Boot { return r.boot }

// PodmanWaitDelay bounds how long a podman subprocess may hold its output pipes after it exits or
// its context is cancelled. cmd.Wait waits for the pipes, not just the process, so a container that
// stops answering hangs it forever - and a cancelled context does not help, because Wait keeps
// waiting on the pipes after the kill. Ten seconds is well past any healthy drain and well inside
// the 15 s shutdown budget.
const PodmanWaitDelay = 10 * time.Second

// PodmanExec runs a podman command and returns the combined output.
//
// The watch is the only bound on the calls here: most of them have no caller worth cancelling (stop a
// container, remove one, inspect one - they must finish whatever the browser did), so a context would
// bound the wrong thing. What a wedged rootless podman needs is a line naming what is still running.
func (r *Runtime) PodmanExec(args ...string) ([]byte, error) {
	cmd := PodmanCommand(args...)
	stop := subproc.Watch(podmanLabel(args), subproc.SlowWait, podmanSlowLog)
	out, err := cmd.CombinedOutput()
	stop()
	return out, err
}

// PodmanExecContext is like PodmanExec but bounds execution to the context's
// deadline. Use it for best-effort calls (e.g. startup cleanup) where a wedged
// podman must not block the caller indefinitely.
func (r *Runtime) PodmanExecContext(ctx context.Context, args ...string) ([]byte, error) {
	cmd := PodmanCommandContext(ctx, args...)
	stop := subproc.Watch(podmanLabel(args), subproc.SlowWait, podmanSlowLog)
	out, err := cmd.CombinedOutput()
	stop()
	return out, err
}

// podmanSlowLog is the logger a slow podman child reports through. Warn, not Error: the run is still
// going, and nothing has failed yet — the point is that an operator reading "podman inspect has been
// running for 30s" knows where to look before concluding the server is hung.
func podmanSlowLog(format string, args ...any) {
	logging.For(logging.Sandbox).Warnf(format, args...)
}

// podmanLabel names a podman operation for the log: the verb and the first operand, which is the
// container or image an operator needs to see ("podman inspect kanban-sandbox-frost-amber"). Flags are
// skipped, so `rm -f <name>` keeps its name; the cost is that a flag **value** can occupy the second
// slot (`create --runtime runsc` labels as "podman create runsc"), which is accepted rather than
// resolved by a table of which podman flags take values — a wrong table would hide the operand.
func podmanLabel(args []string) string {
	verbs := make([]string, 0, 2)
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		verbs = append(verbs, a)
		if len(verbs) == 2 {
			break
		}
	}
	return strings.TrimSpace("podman " + strings.Join(verbs, " "))
}

// RemoveContainer force-removes a container, clearing the stale overlay files that can block `rm -f`.
func (r *Runtime) RemoveContainer(name string) error {
	_, err := r.PodmanExec("rm", "-f", name)
	if err == nil {
		return nil
	}
	r.cleanOverlayMergedDir(name)
	_, err = r.PodmanExec("rm", "-f", name)
	if err == nil {
		return nil
	}
	return fmt.Errorf("podman rm -f %s: %w", name, err)
}

// containerMergedDir returns the merged directory podman reports for one container. The caller
// deletes what it returns, so an answer that is not one is an error.
func (r *Runtime) containerMergedDir(name string) (string, error) {
	if running, err := r.IsRunning(name); err == nil && running {
		return "", fmt.Errorf("container %s is running", name)
	}
	out, err := r.PodmanExec("inspect", "-f", "{{.GraphDriver.Data.MergedDir}}", name)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	return mergedDirFromInspect(name, string(out))
}

// mergedDirFromInspect judges the inspect answer before anything is deleted from it. A field podman
// does not have prints "<no value>" rather than failing, so the shape is what does the checking.
func mergedDirFromInspect(name, inspectOut string) (string, error) {
	merged := strings.TrimSpace(inspectOut)
	if !filepath.IsAbs(merged) || merged != filepath.Clean(merged) || filepath.Base(merged) != "merged" {
		return "", fmt.Errorf("container %s has no overlay merged directory (inspect said %q)", name, merged)
	}
	return merged, nil
}

// cleanOverlayMergedDir empties the one merged directory that can keep `rm -f` from finishing. It
// comes from inspect, never from a walk of the storage root: the other merged directories there belong
// to containers this server does not own, and are live mounts.
func (r *Runtime) cleanOverlayMergedDir(name string) {
	merged, err := r.containerMergedDir(name)
	if err != nil {
		logging.For(logging.Runtime).Infof("overlay cleanup skipped for %s: %v", name, err)
		return
	}
	files, err := os.ReadDir(merged)
	if err != nil {
		return
	}
	for _, f := range files {
		_ = os.RemoveAll(filepath.Join(merged, f.Name()))
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
		logging.For(logging.Runtime).Infof("runsc not found in PATH (probe %s): %v", logTag, err)
		return false
	}
	runscCmd := exec.Command("runsc", "--version")
	// Not PodmanCommand: that constructor pins the binary to podman, and this probe must run runsc
	// (a host with podman but no runsc is the exact case being detected). The environment is the same
	// allowlist — it is the container-runtime CLI environment, and runsc needs none of it — and it
	// carries the WaitDelay through the same helper, so the pairing is not hand-copied here.
	subproc.Bind(runscCmd, PodmanEnv(), PodmanWaitDelay)
	out, err := runscCmd.CombinedOutput()
	if err != nil {
		logging.For(logging.Runtime).Warnf("runsc version check failed (probe %s): %v (%s)", logTag, err, out)
		return false
	}
	logging.For(logging.Runtime).Infof("runsc version (probe %s): %s", logTag, strings.TrimSpace(string(out)))
	if _, err := exec.LookPath("podman"); err != nil {
		logging.For(logging.Runtime).Infof("podman not found in PATH (probe %s): %v", logTag, err)
		return false
	}
	return true
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
		logging.For(logging.Runtime).Warnf("startup: failed to list stale containers (probe %s): %v", logTag, err)
		return nil
	}
	var removed []string
	for _, name := range staleContainerNames(strings.Fields(strings.TrimSpace(string(out))), r.IsRunning) {
		if rmErr := r.RemoveContainer(name); rmErr != nil {
			logging.For(logging.Runtime).Warnf("startup: failed to remove stale container %s (probe %s): %v", name, logTag, rmErr)
			continue
		}
		logging.For(logging.Runtime).Infof("startup: removed stale container %s (probe %s)", name, logTag)
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

// GetAgentBinaryPath resolves the path to the sandbox-bridge binary: the path this Runtime was built
// with (startup.Config.AgentPath, for a host where the bridge is not beside the executable), then the
// locations relative to the executable.
func (r *Runtime) GetAgentBinaryPath() (string, error) {
	if p := r.boot.AgentPath; p != "" {
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
	AnonBytes  int64  // memory.stat "anon": host-side heaps of the processes in the cgroup
	ShmemBytes int64  // memory.stat "shmem": tmpfs, shared memory, and the RAM behind a gVisor guest
	MaxBytes   int64  // memory.max, 0 when unlimited or absent
	OOMKills   uint64 // memory.events oom_kill counter
}

// HeldBytes is the memory the sandbox is holding. gVisor backs the guest's RAM with a file, so the
// memory a task allocates is charged to the cgroup as shmem rather than anon. The host's page cache
// is not held memory, because the kernel can reclaim it.
func (m CgroupMemory) HeldBytes() int64 { return m.AnonBytes + m.ShmemBytes }

// ReadCgroupMemory reads one container's memory controller files. A missing anon line is an error,
// because every caller's number depends on it; every other missing or unparsable value reads as zero.
func ReadCgroupMemory(dir string) (CgroupMemory, error) {
	var m CgroupMemory

	stat, err := os.ReadFile(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return m, fmt.Errorf("read memory.stat: %w", err)
	}
	statContents := string(stat)
	if m.AnonBytes, err = cgroupStatValue(statContents, "anon"); err != nil {
		return m, err
	}
	if shmem, serr := cgroupStatValue(statContents, "shmem"); serr == nil {
		m.ShmemBytes = shmem
	}

	// An unlimited "max" and a missing file both read as 0, because callers only ask whether the
	// container is limited at all.
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

// cgroupStatValue reads one "<key> <bytes>" line from memory.stat. The key is matched with a
// trailing space so that "anon" cannot match the "anon_thp" line.
func cgroupStatValue(contents, key string) (int64, error) {
	for _, line := range strings.Split(contents, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), key+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse memory.stat %s: %w", key, err)
		}
		return v, nil
	}
	return 0, fmt.Errorf("memory.stat has no %s line", key)
}

// ApplyMemoryLimit enforces a hard memory limit on a running container by writing into its
// cgroup, because runsc — which needs --runtime-flag ignore-cgroups under rootless — ignores the
// OCI spec's linux.resources, so podman's --memory alone is never enforced. It writes
// memory.max = limitMB MiB and, best-effort, memory.swap.max = 0: with swap available the host
// reclaims the overshoot instead of OOM-killing, which would make the limit soft. On error the
// caller decides whether to fail open or closed.
func (r *Runtime) ApplyMemoryLimit(name string, limitMB int) error {
	dir, err := r.ContainerCgroupDir(name)
	if err != nil {
		return err
	}
	return applyMemoryLimitAt(dir, name, limitMB)
}

// applyMemoryLimitAt writes the cap into an already-resolved cgroup directory. It takes the directory
// so that the writes can be tested without a container.
func applyMemoryLimitAt(dir, name string, limitMB int) error {
	if limitMB <= 0 {
		return fmt.Errorf("memory limit must be positive, got %d", limitMB)
	}
	maxBytes := int64(limitMB) << 20
	// memory.high is set to "max", which disables it. A throttle cannot work here: the guest's memory
	// is unreclaimable while swap is off, so writing it stalls the guest and then kills it anyway. This
	// write also clears a value left by an older build.
	if err := os.WriteFile(filepath.Join(dir, "memory.high"), []byte("max"), 0); err != nil {
		logging.For(logging.Runtime).Warnf("%s: could not clear memory.high: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(maxBytes, 10)), 0); err != nil {
		return fmt.Errorf("set memory.max in %s: %w", dir, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("0"), 0); err != nil {
		// Non-fatal: hosts without a swap controller still enforce memory.max
		// as a hard cap (there is no swap to absorb the overshoot).
		logging.For(logging.Runtime).Warnf("%s: could not set memory.swap.max=0 (swap not isolated): %v", name, err)
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

// clearCgroupMemoryLimit writes "max" into the cgroup's memory.max, memory.high and
// memory.swap.max files, removing any memory limit.
func clearCgroupMemoryLimit(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "memory.high"), []byte("max"), 0); err != nil {
		logging.For(logging.Runtime).Warnf("could not clear memory.high in %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("max"), 0); err != nil {
		return fmt.Errorf("clear memory.max in %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte("max"), 0); err != nil {
		logging.For(logging.Runtime).Warnf("could not set memory.swap.max=max in %s: %v", dir, err)
	}
	return nil
}
