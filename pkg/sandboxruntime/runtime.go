package sandboxruntime

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// imageRefPattern matches valid OCI/Docker image references.
// Requires the first character be alphanumeric (not a dash) and all
// subsequent characters be alphanumeric or one of / . _ - : @.
var imageRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/._:@-]*$`)

// ValidateImageRef rejects image references that are invalid OCI image characters.
// This prevents references like "-v /etc:/host" or "$(rm -rf /)" from being
// passed to podman as if they were flags.
func ValidateImageRef(image string) error {
	if image == "" {
		return fmt.Errorf("image reference must not be empty")
	}
	if !imageRefPattern.MatchString(image) {
		return fmt.Errorf("image reference contains invalid characters or starts with a dash: %q", image)
	}
	return nil
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
	if err := ValidateImageRef(image); err != nil {
		return false
	}
	_, err := r.PodmanExec("image", "exists", image)
	return err == nil
}

// PullImage downloads a container image.
func (r *Runtime) PullImage(image string, onProgress func(line string)) error {
	if err := ValidateImageRef(image); err != nil {
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
	if err := ValidateImageRef(imageName); err != nil {
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
