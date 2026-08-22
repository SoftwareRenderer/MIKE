package sandboxruntime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// imageRefPattern matches valid OCI/Docker image references.
// Requires the first character be alphanumeric (not a dash) and all
// subsequent characters be alphanumeric or one of / . _ - : @.
var imageRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._:@-]*$`)

// ValidateProjectName rejects project names that could cause issues when used
// as part of container image names, mount paths, or shell commands.
//
// Rules:
//   - Must not be empty
//   - Must not start with a dash (prevents flag injection in podman commands)
//   - Must not contain control characters (prevents newline/tab injection)
//   - Only allows: letters, digits, spaces, hyphens, underscores, dots, and slashes
//
// This is defense-in-depth: project names flow into ProjectImageName() which
// produces image refs for podman, and into mount paths. Even though downstream
// functions validate their inputs, validating at creation/update is the
// earliest point to reject bad data.
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
	return nil
}

// ValidateImageRef rejects image references that are invalid OCI image characters.
// This prevents references like "-v /etc:/host" or "$(rm -rf /)" from being
// passed to podman as if they were flags.
//
// This is the single source of truth for image-reference validation across the
// entire codebase. Both the sandbox Runtime method (below) and the extensions
// RegistryClient delegate to this standalone function, eliminating duplicate
// validation logic that could drift and create security gaps.
func ValidateImageRef(image string) error {
	if image == "" {
		return fmt.Errorf("image reference must not be empty")
	}
	if !imageRefPattern.MatchString(image) {
		return fmt.Errorf("invalid image reference: %q", image)
	}
	return nil
}

// ValidateImageRef rejects image references that are invalid OCI image characters.
// This prevents references like "-v /etc:/host" or "$(rm -rf /)" from being
// passed to podman as if they were flags.
//
// This method is retained for backward compatibility and delegates to the package
// -level ValidateImageRef function.
func (r *Runtime) ValidateImageRef(image string) error {
	return ValidateImageRef(image)
}

// validateDockerfilePath rejects paths that start with a dash,
// preventing them from being interpreted as podman flags.
func validateDockerfilePath(path string) error {
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

// ImageExists checks if a container image is available locally.
func (r *Runtime) ImageExists(image string) bool {
	if err := r.ValidateImageRef(image); err != nil {
		return false
	}
	_, err := r.PodmanExec("image", "exists", image)
	return err == nil
}

// PullImage downloads a container image.
func (r *Runtime) PullImage(image string, onProgress func(line string)) error {
	if err := r.ValidateImageRef(image); err != nil {
		return err
	}
	log.Printf("[sandboxruntime] pulling image %s", image)
	c := exec.Command("podman", "pull", image)
	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	if err := c.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("start pull: %w", err)
	}

	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			if onProgress != nil {
				onProgress(line)
			}
		}
	}()

	waitErr := c.Wait()
	pw.Close()
	if waitErr != nil {
		return fmt.Errorf("pull image %s: %w", image, waitErr)
	}
	return nil
}

// BuildImage builds a container image from a Dockerfile.
func (r *Runtime) BuildImage(imageName, dockerfilePath string, onProgress func(line string)) error {
	if err := r.ValidateImageRef(imageName); err != nil {
		return err
	}
	if err := validateDockerfilePath(dockerfilePath); err != nil {
		return err
	}
	content, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return fmt.Errorf("read dockerfile: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "kanban-sandbox-build")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, content, 0o644); err != nil {
		return fmt.Errorf("write dockerfile to temp dir: %w", err)
	}

	c := exec.Command("podman", "build", "-t", imageName, tmpDir)
	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	if err := c.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("start build: %w", err)
	}

	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			if onProgress != nil {
				onProgress(line)
			}
		}
	}()

	waitErr := c.Wait()
	pw.Close()
	if waitErr != nil {
		return fmt.Errorf("build image %s: %w", imageName, waitErr)
	}
	return nil
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

// ApplyMemoryLimit enforces a hard memory limit on a running container by
// writing directly into its cgroup. This is required because runsc (which
// needs --runtime-flag ignore-cgroups under rootless) does not apply the OCI
// spec's linux.resources, so podman's --memory flag alone is never enforced.
//
// Writes memory.max = limitMB MiB. It also writes memory.swap.max = 0 on a
// best-effort basis: without it, a host with swap reclaims overshoot into
// swap instead of OOM-killing, turning the hard limit into a soft one.
// Returns an error if the limit cannot be applied; the caller decides whether
// to fail open (sandbox runs unlimited) or fail closed.
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
