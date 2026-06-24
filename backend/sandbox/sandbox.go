package sandbox

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const (
	// ContainerNamePrefix is the prefix for sandbox container names.
	ContainerNamePrefix = "kanban-sandbox"
	// ProjectImagePrefix is the prefix for per-project built images.
	ProjectImagePrefix = "sandbox"
	// BaseImage is the default container image.
	BaseImage = "docker.io/library/ubuntu:latest"
	// SandboxMountPath is where the worktree is bind-mounted inside the container.
	SandboxMountPath = "/worktree"
	// KeepAliveTimeout is how long to wait for container cleanup.
	KeepAliveTimeout = 30 * time.Second
)

// SandboxManager manages gVisor sandbox containers for tasks.
type SandboxManager struct {
	mu        sync.Mutex
	active    map[string]string // taskID -> containerName
	available bool
}

// NewSandboxManager creates a new SandboxManager and checks gVisor availability.
func NewSandboxManager() *SandboxManager {
	sm := &SandboxManager{
		active: make(map[string]string),
	}
	sm.available = sm.checkAvailable()
	if sm.available {
		log.Println("[sandbox] gVisor available, sandboxing enabled")
	} else {
		log.Println("[sandbox] gVisor not available (runsc not found), sandboxing disabled")
	}
	return sm
}

// IsAvailable returns true if gVisor (runsc) is installed and podman can use it.
func (sm *SandboxManager) IsAvailable() bool {
	return sm.available
}

// checkAvailable verifies that runsc is installed and podman can use it.
func (sm *SandboxManager) checkAvailable() bool {
	// Check runsc binary exists
	if _, err := exec.LookPath("runsc"); err != nil {
		log.Printf("[sandbox] runsc not found in PATH: %v", err)
		return false
	}
	// Check runsc version
	out, err := exec.Command("runsc", "--version").CombinedOutput()
	if err != nil {
		log.Printf("[sandbox] runsc version check failed: %v (%s)", err, out)
		return false
	}
	log.Printf("[sandbox] runsc version: %s", strings.TrimSpace(string(out)))
	// Check podman exists
	if _, err := exec.LookPath("podman"); err != nil {
		log.Printf("[sandbox] podman not found in PATH: %v", err)
		return false
	}
	return true
}

// podmanExec runs a podman command.
// When using gVisor (runsc) with rootless podman, --runtime-flag ignore-cgroups=true
// must be passed to both create and start commands.
func (sm *SandboxManager) podmanExec(args ...string) ([]byte, error) {
	c := exec.Command("podman", args...)
	out, err := c.CombinedOutput()
	return out, err
}

// Create creates a gVisor sandbox container for a task.
// The worktree is bind-mounted read-write at SandboxMountPath inside the container.
// If baseImage is empty, the default BaseImage constant is used.
// If networkIsolation is true, the container is created with --network none.
// If memoryLimit is > 0, the container is created with --memory=<limit>M.
func (sm *SandboxManager) Create(taskID, worktreePath, baseImage string, networkIsolation bool, memoryLimit int) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.available {
		return fmt.Errorf("gVisor not available (runsc not installed)")
	}

	// Sanitize ID: podman container names must match [a-zA-Z0-9][a-zA-Z0-9_.-]*
	safeID, err := sanitizeContainerID(taskID)
	if err != nil {
		return fmt.Errorf("invalid task ID for sandbox container: %w", err)
	}
	containerName := fmt.Sprintf("%s-%s", ContainerNamePrefix, safeID)

	// Check if container already exists
	exists, err := sm.containerExists(containerName)
	if err != nil {
		return fmt.Errorf("check container: %w", err)
	}
	if exists {
		// Clean up stale container from a previous server run
		log.Printf("[sandbox] task=%s removing stale container %s", taskID, containerName)
		rmOut, rmErr := sm.podmanExec("rm", "-f", containerName)
		if rmErr != nil {
			return fmt.Errorf("remove stale container %s: %w (%s)", containerName, rmErr, rmOut)
		}
	}

	// Resolve worktree to absolute path
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}

	// Create container with gVisor runtime
	// --runtime-flag overlay2=root:self: use kernel overlay2 for writable rootfs
	// --runtime-flag ignore-cgroups: required for rootless gVisor (no cgroup access)
	// --security-opt label=disable: required for gVisor (no SELinux support)
	// --cap-drop=ALL: drop all capabilities, re-add only what's needed (see below)
	// --security-opt no-new-privileges: prevent privilege escalation via setuid binaries
	image := baseImage
	if image == "" {
		image = BaseImage
	}

	// --runtime-flag is a global podman option (must come before subcommand)
	// Do not include leading -- in the flag value per podman docs
	createArgs := []string{
		"--runtime-flag=overlay2=root:self",
		"--runtime-flag=ignore-cgroups",
		"create",
		"--name", containerName,
		"--runtime", "runsc",
		"--security-opt", "label=disable",
		"--cap-drop", "ALL",
		"--cap-add", "CAP_CHOWN", // worktree files may have mismatched ownership
		"--cap-add", "CAP_DAC_OVERRIDE", // apt's _apt user must access root-owned temp files accessible in the container
		"--cap-add", "CAP_SETUID", // apt drops privileges via seteuid()
		"--cap-add", "CAP_SETGID", // apt drops privileges via setegid()
		"--cap-add", "CAP_FOWNER", // apt chmod on /var/lib/apt/lists/
		"--security-opt", "no-new-privileges",
		"--workdir", SandboxMountPath,
		"-v", absWorktree + ":" + SandboxMountPath + ":rprivate,rw",
		"-e", "TERM=xterm-256color",
		"-e", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	if networkIsolation {
		createArgs = append(createArgs, "--network", "none")
	}
	if memoryLimit > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dM", memoryLimit))
	}
	createArgs = append(createArgs, image, "sleep", "infinity")

	out, err := sm.podmanExec(createArgs...)
	if err != nil {
		return fmt.Errorf("create container: %w (%s)", err, out)
	}

	// Start the container (needed for exec sessions)
	// --runtime-flag ignore-cgroups=true is required for rootless gVisor
	startOut, err := sm.podmanExec("--runtime-flag", "ignore-cgroups=true", "start", containerName)
	if err != nil {
		// Clean up on start failure
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return fmt.Errorf("start container: %w (%s)", err, startOut)
	}

	sm.active[taskID] = containerName
	return nil
}

// Exec runs a command inside the task's sandbox container.
// Returns the combined stdout/stderr output.
func (sm *SandboxManager) Exec(taskID, command string, env []string) (string, error) {
	return sm.execInternal(taskID, command, nil, env)
}

// ExecWithInput runs a command inside the task's sandbox container with stdin input.
// Returns the combined stdout/stderr output.
func (sm *SandboxManager) ExecWithInput(taskID, command string, stdin []byte, env []string) (string, error) {
	return sm.execInternal(taskID, command, stdin, env)
}

// execInternal is the shared implementation for Exec and ExecWithInput.
// When stdin is non-nil, it is piped to the command's standard input.
func (sm *SandboxManager) execInternal(taskID, command string, stdin []byte, env []string) (string, error) {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	sm.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("no sandbox container for task %s", taskID)
	}

	// Build exec arguments
	execArgs := []string{
		"exec",
	}
	if stdin != nil {
		execArgs = append(execArgs, "-i") // keep STDIN open
	}
	execArgs = append(execArgs,
		"-w", SandboxMountPath,
		containerName,
	)

	// Add environment variables
	for _, e := range env {
		execArgs = append(execArgs, "-e", e)
	}

	// Run sh -c to handle shell features
	execArgs = append(execArgs, "sh", "-c", command)

	c := exec.Command("podman", execArgs...)

	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}

	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("exec failed: %w", err)
	}
	return string(out), nil
}

// ShellProcess holds a running shell process inside the sandbox.
type ShellProcess struct {
	Cmd  *exec.Cmd
	Ptmx *os.File
}

// StartShell starts an interactive shell inside the task's sandbox container.
// The container must already exist (created by Create). Returns a ShellProcess
// with the PTY master file descriptor for I/O relay.
// The caller is responsible for calling Close() when done.
func (sm *SandboxManager) StartShell(taskID string) (*ShellProcess, error) {
	return sm.startShell(taskID, false, "", "", false, 0)
}

// StartShellWithCreate starts an interactive shell, creating the sandbox container
// if it doesn't already exist. Used for project-level terminals where there's no
// task lifecycle to manage the container. The caller is responsible for calling
// Close() on the ShellProcess and Stop() on the container when done.
// If baseImage is empty, the default BaseImage constant is used.
// If networkIsolation is true, the container is created with --network none.
// If memoryLimit is > 0, the container is created with --memory=<limit>M.
func (sm *SandboxManager) StartShellWithCreate(taskID, worktreePath, baseImage string, networkIsolation bool, memoryLimit int) (*ShellProcess, error) {
	return sm.startShell(taskID, true, worktreePath, baseImage, networkIsolation, memoryLimit)
}

func (sm *SandboxManager) startShell(taskID string, createIfMissing bool, worktreePath, baseImage string, networkIsolation bool, memoryLimit int) (*ShellProcess, error) {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	sm.mu.Unlock()

	if !ok {
		if !createIfMissing || worktreePath == "" {
			return nil, fmt.Errorf("no sandbox container for %s", taskID)
		}
		// Create container on demand (for project terminals)
		if err := sm.Create(taskID, worktreePath, baseImage, networkIsolation, memoryLimit); err != nil {
			return nil, fmt.Errorf("create sandbox container: %w", err)
		}
		sm.mu.Lock()
		containerName = sm.active[taskID]
		sm.mu.Unlock()
	}

	// Prefer bash if available in the image, fall back to sh
	shell := "sh"
	if _, err := sm.podmanExec("exec", containerName, "test", "-e", "/bin/bash"); err == nil {
		shell = "bash"
	}

	// Start podman exec with PTY
	// -i: keep STDIN open
	// -t: allocate a pseudo-TTY
	cmd := exec.Command(
		"podman", "exec", "-it",
		"-w", SandboxMountPath,
		containerName,
		shell,
	)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Start with PTY
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, fmt.Errorf("start shell: %w", err)
	}

	// Set non-blocking mode on the PTY
	syscall.SetNonblock(int(ptmx.Fd()), true)

	sp := &ShellProcess{
		Cmd:  cmd,
		Ptmx: ptmx,
	}

	log.Printf("[sandbox] task=%s started shell in container %s", taskID, containerName)
	return sp, nil
}

// Close terminates the shell process and cleans up the PTY.
func (sp *ShellProcess) Close() error {
	if sp.Ptmx != nil {
		_ = sp.Ptmx.Close()
	}
	if sp.Cmd != nil && sp.Cmd.Process != nil {
		_ = sp.Cmd.Process.Kill()
		_ = sp.Cmd.Wait()
	}
	return nil
}

// Stop removes the sandbox container for a task.
func (sm *SandboxManager) Stop(taskID string) error {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	if ok {
		delete(sm.active, taskID)
	}
	sm.mu.Unlock()

	if !ok {
		return nil // already stopped
	}

	// Remove container (force stop if running)
	out, err := sm.podmanExec("rm", "-f", containerName)
	if err != nil {
		log.Printf("[sandbox] task=%s warning: failed to remove container: %s", taskID, out)
		return fmt.Errorf("remove container: %w", err)
	}

	log.Printf("[sandbox] task=%s removed container %s", taskID, containerName)
	return nil
}

// ImageReady checks if a container image is available locally.
func (sm *SandboxManager) ImageReady(image string) bool {
	if image == "" {
		image = BaseImage
	}
	_, err := sm.podmanExec("image", "exists", image)
	return err == nil
}

// PullImage downloads a container image. Blocks until the pull completes.
func (sm *SandboxManager) PullImage(image string) error {
	if image == "" {
		image = BaseImage
	}
	log.Printf("[sandbox] pulling image %s", image)
	out, err := sm.podmanExec("pull", image)
	if err != nil {
		return fmt.Errorf("pull image %s: %w (%s)", image, err, out)
	}
	log.Printf("[sandbox] image %s pulled", image)
	return nil
}

// ProjectImageName returns the image name for a project's custom-built Dockerfile image.
// Uses a sanitized version of the project name for readability.
func ProjectImageName(projectName string) string {
	safeName := strings.ReplaceAll(projectName, " ", "-")
	safeName = strings.ReplaceAll(safeName, "/", "-")
	if ProjectImagePrefix == "" {
		return safeName
	}
	return fmt.Sprintf("%s-%s", ProjectImagePrefix, safeName)
}

// BuildImage builds a container image from a Dockerfile and tags it with the
// project-specific name. The Dockerfile is copied to a temp directory for the build.
// The temp directory is cleaned up after the build completes.
func (sm *SandboxManager) BuildImage(projectName, dockerfilePath string) error {
	if !sm.available {
		return fmt.Errorf("gVisor not available (runsc not installed)")
	}

	// Read the Dockerfile
	content, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return fmt.Errorf("read dockerfile: %w", err)
	}

	// Create a temp directory and write the Dockerfile
	tmpDir, err := os.MkdirTemp("", "kanban-sandbox-build")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, content, 0o644); err != nil {
		return fmt.Errorf("write dockerfile to temp dir: %w", err)
	}

	imageName := ProjectImageName(projectName)

	// Remove any existing image with the same name
	_, _ = sm.podmanExec("rmi", imageName) // best-effort

	// Build the image
	log.Printf("[sandbox] project=%s building image %s from %s", projectName, imageName, dockerfilePath)
	out, err := sm.podmanExec("build", "-t", imageName, tmpDir)
	if err != nil {
		return fmt.Errorf("build image %s: %w (%s)", imageName, err, out)
	}
	log.Printf("[sandbox] project=%s image %s built successfully", projectName, imageName)
	return nil
}

// ImageReadyForProject checks if a project's custom-built image exists locally.
func (sm *SandboxManager) ImageReadyForProject(projectName string) bool {
	imageName := ProjectImageName(projectName)
	_, err := sm.podmanExec("image", "exists", imageName)
	return err == nil
}

// containerExists checks if a container with the given name exists.
func (sm *SandboxManager) containerExists(name string) (bool, error) {
	// Use "exists" subcommand which is more reliable than ps filtering
	_, err := sm.podmanExec("inspect", "-t", "container", name)
	if err != nil {
		// inspect returns non-zero if container doesn't exist
		return false, nil
	}
	return true, nil
}

// Cleanup removes all active sandbox containers.
func (sm *SandboxManager) Cleanup() {
	sm.mu.Lock()
	tasks := make([]string, 0, len(sm.active))
	for taskID := range sm.active {
		tasks = append(tasks, taskID)
	}
	sm.mu.Unlock()

	for _, taskID := range tasks {
		_ = sm.Stop(taskID)
	}
}

// sanitizeContainerID ensures the ID is a valid podman container name.
// Podman names must match [a-zA-Z0-9][a-zA-Z0-9_.-]*
func sanitizeContainerID(id string) (string, error) {
	var result strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			result.WriteRune(r)
		} else {
			result.WriteRune('-')
		}
	}
	s := result.String()
	// Ensure first character is alphanumeric
	for len(s) > 0 && !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= '0' && s[0] <= '9')) {
		s = s[1:]
	}
	if s == "" {
		return "", fmt.Errorf("ID %q contains no valid container name characters", id)
	}
	return s, nil
}
