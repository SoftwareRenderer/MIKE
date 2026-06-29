package sandbox

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
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
	// PiConfigMount is where ~/.pi/agent/ is bind-mounted inside the container (for Pi agent tasks).
	// Mounted at /root/.pi/agent so Pi finds it at its default config location.
	PiConfigMount = "/root/.pi/agent"
	// KeepAliveTimeout is how long to wait for container cleanup.
	KeepAliveTimeout = 30 * time.Second
)

// SandboxManager manages gVisor sandbox containers for tasks.
type SandboxManager struct {
	mu         sync.Mutex
	active     map[string]string // taskID -> containerName
	mountPaths map[string]string // taskID -> mount path inside container
	available  bool
}

// NewSandboxManager creates a new SandboxManager and checks gVisor availability.
func NewSandboxManager() *SandboxManager {
	sm := &SandboxManager{
		active:     make(map[string]string),
		mountPaths: make(map[string]string),
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
// The worktree is bind-mounted read-write at /sandbox/<basename> inside the container.
// If baseImage is empty, the default BaseImage constant is used.
// If networkIsolation is true, the container is created with --network none.
// If memoryLimit is > 0, the container is created with --memory=<limit>M.
// If piAgentDir is non-empty, ~/.pi/agent/ is bind-mounted at PiConfigMount
// (only for Pi agent tasks). Pi finds its config at the default location via HOME=/root.
func (sm *SandboxManager) Create(taskID, worktreePath, baseImage string, networkIsolation bool, memoryLimit int, piAgentDir string) error {
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
	// Compute mount path: /sandbox/<basename-of-worktree>
	mountPath := "/sandbox/" + filepath.Base(absWorktree)

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
		"--workdir", mountPath,
		"-v", absWorktree + ":" + mountPath + ":rprivate,rw",
		"-e", "TERM=xterm-256color",
		"-e", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	if networkIsolation {
		createArgs = append(createArgs, "--network", "none")
	}
	if memoryLimit > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dM", memoryLimit))
	}
	// Pi agent config mount (only for Pi tasks)
	// Mounted at /root/.pi/agent so Pi finds it at its default config location
	if piAgentDir != "" {
		absPiDir, err := filepath.Abs(piAgentDir)
		if err != nil {
			return fmt.Errorf("resolve pi agent dir path: %w", err)
		}
		createArgs = append(createArgs,
			"-v", absPiDir+":"+PiConfigMount+":rprivate,rw",
			"-e", "HOME=/root",
		)
		log.Printf("[sandbox] task=%s mounting pi agent dir %s at %s", taskID, absPiDir, PiConfigMount)
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
	sm.mountPaths[taskID] = mountPath
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
	mountPath := sm.mountPaths[taskID]
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
		"-w", mountPath,
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

// ProcessHandle holds a running process inside the sandbox (no PTY).
// Used for JSONL-based processes like Pi Coding Agent.
type ProcessHandle struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
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
	return sm.startShell(taskID, false, "", "", false, 0, "")
}

// StartShellWithCreate starts an interactive shell, creating the sandbox container
// if it doesn't already exist. Used for project-level terminals where there's no
// task lifecycle to manage the container. The caller is responsible for calling
// Close() on the ShellProcess and Stop() on the container when done.
// If baseImage is empty, the default BaseImage constant is used.
// If networkIsolation is true, the container is created with --network none.
// If memoryLimit is > 0, the container is created with --memory=<limit>M.
// If piAgentDir is non-empty, the Pi config directory is bind-mounted.
func (sm *SandboxManager) StartShellWithCreate(taskID, worktreePath, baseImage string, networkIsolation bool, memoryLimit int, piAgentDir string) (*ShellProcess, error) {
	return sm.startShell(taskID, true, worktreePath, baseImage, networkIsolation, memoryLimit, piAgentDir)
}

func (sm *SandboxManager) startShell(taskID string, createIfMissing bool, worktreePath, baseImage string, networkIsolation bool, memoryLimit int, piAgentDir string) (*ShellProcess, error) {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	mountPath := sm.mountPaths[taskID]
	sm.mu.Unlock()

	if !ok {
		if !createIfMissing || worktreePath == "" {
			return nil, fmt.Errorf("no sandbox container for %s", taskID)
		}
		// Create container on demand (for project terminals)
		if err := sm.Create(taskID, worktreePath, baseImage, networkIsolation, memoryLimit, piAgentDir); err != nil {
			return nil, fmt.Errorf("create sandbox container: %w", err)
		}
		sm.mu.Lock()
		containerName = sm.active[taskID]
		mountPath = sm.mountPaths[taskID]
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
		"-w", mountPath,
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

// Close terminates the process handle and cleans up pipes.
func (ph *ProcessHandle) Close() error {
	if ph.Stdin != nil {
		_ = ph.Stdin.Close()
	}
	if ph.Stdout != nil {
		_ = ph.Stdout.Close()
	}
	if ph.Cmd != nil && ph.Cmd.Process != nil {
		_ = ph.Cmd.Process.Kill()
		_ = ph.Cmd.Wait()
	}
	return nil
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

// StartProcess starts a non-interactive process inside the task's sandbox container.
// Unlike StartShell, this does not allocate a PTY — it uses plain stdin/stdout pipes.
// This is suitable for JSONL-based protocols like the Pi Coding Agent RPC mode.
// The caller is responsible for calling Close() on the returned ProcessHandle when done.
func (sm *SandboxManager) StartProcess(taskID, command string, args []string) (*ProcessHandle, error) {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	mountPath := sm.mountPaths[taskID]
	sm.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("no sandbox container for task %s", taskID)
	}

	// Build exec arguments: -i keeps STDIN open, no -t (no PTY)
	execArgs := []string{
		"exec", "-i",
		"-w", mountPath,
		containerName,
		command,
	}
	execArgs = append(execArgs, args...)

	cmd := exec.Command("podman", execArgs...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	// Merge stderr into logs (goes to parent's stderr)
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start process in container: %w", err)
	}

	ph := &ProcessHandle{
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
	}

	log.Printf("[sandbox] task=%s started process %q in container %s (pid=%d)", taskID, command, containerName, cmd.Process.Pid)
	return ph, nil
}

// PiAvailable checks if the `pi` binary is available inside the task's sandbox container.
// Returns true if `which pi` succeeds inside the container.
func (sm *SandboxManager) PiAvailable(taskID string) bool {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	sm.mu.Unlock()

	if !ok {
		return false
	}

	_, err := sm.podmanExec("exec", containerName, "which", "pi")
	return err == nil
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

// ContainerRunning returns true if a sandbox container for the given task is
// tracked in the active map (i.e., was created in the current server session).
func (sm *SandboxManager) ContainerRunning(taskID string) bool {
	sm.mu.Lock()
	_, ok := sm.active[taskID]
	sm.mu.Unlock()
	return ok
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
	return sm.PullImageWithProgress(image, nil)
}

// PullImageWithProgress downloads a container image, streaming progress lines via onProgress.
// onProgress is called for each line of output (nil means no callback).
func (sm *SandboxManager) PullImageWithProgress(image string, onProgress func(line string)) error {
	if image == "" {
		image = BaseImage
	}
	log.Printf("[sandbox] pulling image %s", image)

	c := exec.Command("podman", "pull", image)
	// Merge stdout+stderr into a single pipe for streaming
	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	if err := c.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("start pull: %w", err)
	}

	// Stream output line by line
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			if onProgress != nil {
				onProgress(line)
			}
			log.Printf("[sandbox] pull: %s", line)
		}
	}()

	waitErr := c.Wait()
	pw.Close() // signal EOF to reader goroutine
	if waitErr != nil {
		return fmt.Errorf("pull image %s: %w", image, waitErr)
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
	return sm.BuildImageWithProgress(projectName, dockerfilePath, nil)
}

// BuildImageWithProgress builds a container image, streaming progress lines via onProgress.
// onProgress is called for each line of output (nil means no callback).
func (sm *SandboxManager) BuildImageWithProgress(projectName, dockerfilePath string, onProgress func(line string)) error {
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

	// Build the image with streaming output
	log.Printf("[sandbox] project=%s building image %s from %s", projectName, imageName, dockerfilePath)
	c := exec.Command("podman", "build", "-t", imageName, tmpDir)
	// Merge stdout+stderr into a single pipe for streaming
	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	if err := c.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("start build: %w", err)
	}

	// Stream output line by line
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			if onProgress != nil {
				onProgress(line)
			}
			log.Printf("[sandbox] build: %s", line)
		}
	}()

	waitErr := c.Wait()
	pw.Close() // signal EOF to reader goroutine
	if waitErr != nil {
		return fmt.Errorf("build image %s: %w", imageName, waitErr)
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
