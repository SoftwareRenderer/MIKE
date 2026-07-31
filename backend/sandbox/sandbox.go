package sandbox

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"kanban/backend/types"
	sandbox "kanban/pkg/bridge"
	"kanban/pkg/sandboxruntime"
)

const (
	// ContainerNamePrefix is the prefix for sandbox container names.
	ContainerNamePrefix = "kanban-sandbox"
	// ProjectImagePrefix is the prefix for per-project built images.
	ProjectImagePrefix = "sandbox"
	// BaseImage is the default container image.
	BaseImage = "docker.io/library/ubuntu:latest"
	// SkillsMount is where the prompts/skills/ directory is bind-mounted read-only.
	SkillsMount = "/mnt/skills"
	// KeepAliveTimeout is how long to wait for container cleanup.
	KeepAliveTimeout = 30 * time.Second
	// livenessTTL is how long to cache the result of a container liveness check.
	// Avoids expensive podman inspect calls on every FS operation.
	livenessTTL = 5 * time.Minute
)

// SandboxOpts holds the configuration for sandbox container creation and execution.
type SandboxOpts struct {
	BaseImage        string
	NetworkIsolation bool
	MemoryLimit      int
	ForwardPorts     []types.PortForward
	AgentSandbox     *types.AgentSandbox     // mounts and envs for agent sandbox
	SkillsDir        string
	SandboxMounts    []types.SandboxMount
	SandboxEnvs      []types.SandboxEnv
	Capabilities     []string
	PlanMode         bool
}

// agentConn holds the pipes to a running agent process.
type agentConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

// SandboxManager manages gVisor sandbox containers for tasks.
type SandboxManager struct {
	mu            sync.Mutex
	rt            *sandboxruntime.Runtime
	active        map[string]string          // taskID -> containerName
	mountPaths    map[string]string          // taskID -> mount path inside container
	agents        map[string]*agentConn      // taskID -> agent pipes + process
	clients       map[string]*sandbox.Client // taskID -> agent client (cached)
	livenessCache map[string]time.Time       // taskID -> last time running was confirmed
	available     bool
}

// NewSandboxManager creates a new SandboxManager and checks gVisor availability.
func NewSandboxManager() *SandboxManager {
	rt := sandboxruntime.NewRuntime()
	sm := &SandboxManager{
		rt:            rt,
		active:        make(map[string]string),
		mountPaths:    make(map[string]string),
		agents:        make(map[string]*agentConn),
		clients:       make(map[string]*sandbox.Client),
		livenessCache: make(map[string]time.Time),
	}
	sm.available = sm.checkAvailable()
	if sm.available {
		log.Println("[sandbox] gVisor available, sandboxing enabled")
		// Skip cleanup if MIKE_SANDBOX_SKIP_STARTUP_CLEANUP is set (e.g. during tests
		// where TestMain handles cleanup once to avoid races between parallel tests).
		if os.Getenv("MIKE_SANDBOX_SKIP_STARTUP_CLEANUP") == "" {
			sm.cleanupStaleContainers()
		}
	} else {
		log.Println("[sandbox] gVisor not available (runsc not found), sandboxing disabled")
	}
	return sm
}

// cleanupStaleContainers removes any kanban-sandbox-* containers left behind
// by a previous server session (e.g. crash, SIGKILL). This runs at startup
// so stale containers don't accumulate across restarts.
func (sm *SandboxManager) cleanupStaleContainers() {
	out, err := sm.rt.PodmanExec("ps", "-a", "--filter", "name=kanban-sandbox-", "--format", "{{.Names}}")
	if err != nil {
		log.Printf("[sandbox] startup: failed to list stale containers: %v", err)
		return
	}
	names := strings.Fields(strings.TrimSpace(string(out)))
	if len(names) == 0 {
		return
	}
	for _, name := range names {
		if rmErr := sm.rt.RemoveContainer(name); rmErr != nil {
			log.Printf("[sandbox] startup: failed to remove stale container %s: %v", name, rmErr)
		} else {
			log.Printf("[sandbox] startup: removed stale container %s", name)
		}
	}
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
	return sm.rt.PodmanExec(args...)
}

// GetClient returns the agent client for a task.
// The client communicates directly with the container agent via podman exec pipes.
func (sm *SandboxManager) GetClient(taskID string) (*sandbox.Client, error) {
	sm.mu.Lock()
	if c, ok := sm.clients[taskID]; ok {
		if c.IsAlive() {
			sm.mu.Unlock()
			return c, nil
		}
		delete(sm.clients, taskID)
	}
	ac, ok := sm.agents[taskID]
	sm.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("no agent process for task %s", taskID)
	}

	client := sandbox.NewClient(ac.stdin, ac.stdout)

	sm.mu.Lock()
	sm.clients[taskID] = client
	sm.mu.Unlock()
	return client, nil
}

// Create creates a gVisor sandbox container for a task.
//
// Filesystem Mounts:
//   - Worktree: Bind-mounted at /home/sandbox/<projectName>. Mode is read-only (ro) if planMode is true,
//     otherwise read-write (rw).
//   - Git Metadata: If projectPath is non-empty and .git/ exists, it is mounted read-only at the
//     same absolute path to allow git to resolve refs, objects, and worktree metadata.
//   - Agent Sandbox: If opts.AgentSandbox is provided, its mounts and envs are applied
//     (default: /root/.config/agent) and HOME is set to /root.
//   - Skills: If skillsDir is non-empty, it is bind-mounted read-only at SkillsMount.
//   - Custom: Any additional mounts in opts.SandboxMounts are added after the standard mounts.
//
// Container Configuration:
//   - Image: Uses opts.BaseImage if provided, otherwise defaults to BaseImage constant.
//   - Network: Created with --network none if networkIsolation is true.
//   - Memory: Created with --memory=<limit>M if memoryLimit > 0.
//   - Environment: User-defined environment variables in opts.SandboxEnvs are set via -e flags.
//
// Networking:
//   - Port Forwarding: For each entry in opts.ForwardPorts, an ephemeral host port is auto-assigned
//     via net.Listen(":0") and mapped to the container port.
//
// Returns:
//   - (assignedPorts, nil) on success, where assignedPorts lists the host→container mappings.
//   - (nil, error) on failure.
func (sm *SandboxManager) Create(taskID, worktreePath, projectName, projectPath string, opts SandboxOpts) ([]types.AssignedPort, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Validate image ref before any resource allocation to prevent argument
	// injection (e.g. leading dash being interpreted as a podman flag).
	image := opts.BaseImage
	if image == "" {
		image = BaseImage
	}
	if err := sm.rt.ValidateImageRef(image); err != nil {
		return nil, fmt.Errorf("invalid sandbox image: %w", err)
	}

	if !sm.available {
		return nil, fmt.Errorf("gVisor not available (runsc not installed)")
	}

	// Sanitize ID: podman container names must match [a-zA-Z0-9][a-zA-Z0-9_.-]*
	safeID, err := sm.rt.SanitizeID(taskID)
	if err != nil {
		return nil, fmt.Errorf("invalid task ID for sandbox container: %w", err)
	}
	containerName := fmt.Sprintf("%s-%s", ContainerNamePrefix, safeID)

	// Check if container already exists
	exists, err := sm.rt.ContainerExists(containerName)
	if err != nil {
		return nil, fmt.Errorf("check container: %w", err)
	}
	if exists {
		// Clean up stale container from a previous server run
		log.Printf("[sandbox] task=%s removing stale container %s", taskID, containerName)
		if rmErr := sm.rt.RemoveContainer(containerName); rmErr != nil {
			return nil, fmt.Errorf("remove stale container %s: %w", containerName, rmErr)
		}
	}

	// Resolve worktree to absolute path
	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path: %w", err)
	}
	// Compute mount path: /home/sandbox/<project-name>
	mountPath := "/home/sandbox/" + projectName

	// Resolve agent binary path (required)
	agentPath, err := sm.rt.GetAgentBinaryPath()
	if err != nil {
		return nil, fmt.Errorf("sandbox-bridge binary not found: %w", err)
	}

	// Create container with gVisor runtime
	// --runtime-flag overlay2=root:self: use kernel overlay2 for writable rootfs
	// --runtime-flag ignore-cgroups: required for rootless gVisor (no cgroup access)
	// --security-opt label=disable: required for gVisor (no SELinux support)
	// --cap-drop=ALL: drop all capabilities, re-add only those explicitly configured
	// --security-opt no-new-privileges: prevent privilege escalation via setuid binaries
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
		"--security-opt", "no-new-privileges",
		"--workdir", mountPath,
	}
	// Add back any explicitly requested capabilities
	for _, cap := range opts.Capabilities {
		createArgs = append(createArgs, "--cap-add", cap)
	}
	worktreeMode := "rw"
	if opts.PlanMode {
		worktreeMode = "ro"
	}
	createArgs = append(createArgs,
		"-v", absWorktree+":"+mountPath+":rprivate,"+worktreeMode,
		"-e", "TERM=xterm-256color",
		"-e", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	)
	if opts.NetworkIsolation {
		createArgs = append(createArgs, "--network", "none")
	}
	// Port forwarding: auto-assign an ephemeral host port for each container port.
	var assignedPorts []types.AssignedPort
	for _, pf := range opts.ForwardPorts {
		if pf.ContainerPort < 1 || pf.ContainerPort > 65535 {
			log.Printf("[sandbox] task=%s skipping invalid container port: %d", taskID, pf.ContainerPort)
			continue
		}
		// Auto-assign an available ephemeral host port
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Printf("[sandbox] task=%s failed to assign host port for container port %d: %v", taskID, pf.ContainerPort, err)
			continue
		}
		hostPort := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		createArgs = append(createArgs, "-p", fmt.Sprintf("%d:%d", hostPort, pf.ContainerPort))
		assignedPorts = append(assignedPorts, types.AssignedPort{HostPort: hostPort, ContainerPort: pf.ContainerPort})
		log.Printf("[sandbox] task=%s forwarding port %d -> %d", taskID, hostPort, pf.ContainerPort)
	}
	if opts.MemoryLimit > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dM", opts.MemoryLimit))
	}
	// Mount .git/ read-only at the same absolute path.
	// This lets git inside the container resolve refs, objects, worktree metadata, etc.
	if projectPath != "" {
		gitDir := filepath.Join(projectPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			createArgs = append(createArgs,
				"-v", gitDir+":"+gitDir+":rprivate,ro",
			)
			log.Printf("[sandbox] task=%s mounting .git read-only: %s", taskID, gitDir)
		}
	}

	// Agent sandbox mounts and envs (only when an agent extension is configured)
	if opts.AgentSandbox != nil {
		for _, m := range opts.AgentSandbox.Mounts {
			if m.HostPath == "" || m.ContainerPath == "" {
				continue
			}
			absHost, err := filepath.Abs(m.HostPath)
			if err != nil {
				log.Printf("[sandbox] task=%s skipping agent mount: invalid host path %s: %v", taskID, m.HostPath, err)
				continue
			}
			mountMode := m.Mode
			if mountMode != "ro" && mountMode != "rw" {
				mountMode = "ro"
			}
			createArgs = append(createArgs,
				"-v", absHost+":"+m.ContainerPath+":rprivate,"+mountMode,
			)
			log.Printf("[sandbox] task=%s mounting agent sandbox %s -> %s (%s)", taskID, absHost, m.ContainerPath, mountMode)
		}
		for _, e := range opts.AgentSandbox.Envs {
			if e.Key != "" {
				createArgs = append(createArgs, "-e", e.Key+"="+e.Value)
				// Don't log env values — they may contain API keys, tokens, or other secrets.
				log.Printf("[sandbox] task=%s setting agent env %s", taskID, e.Key)
			}
		}
	}

	// Skills directory mount (read-only, for skill packages)
	if opts.SkillsDir != "" {
		absSkillsDir, err := filepath.Abs(opts.SkillsDir)
		if err != nil {
			log.Printf("[sandbox] task=%s skipping skills mount: invalid path %s: %v", taskID, opts.SkillsDir, err)
		} else if _, err := os.Stat(absSkillsDir); err == nil {
			createArgs = append(createArgs,
				"-v", absSkillsDir+":"+SkillsMount+":rprivate,ro",
			)
			log.Printf("[sandbox] task=%s mounting skills read-only: %s -> %s", taskID, absSkillsDir, SkillsMount)
		}
	}

	// Custom mounts (user-configured per-project)
	for _, m := range opts.SandboxMounts {
		hostPath, err := filepath.Abs(m.HostPath)
		if err != nil {
			log.Printf("[sandbox] task=%s skipping mount: invalid host path %s: %v", taskID, m.HostPath, err)
			continue
		}
		if _, err := os.Stat(hostPath); err != nil {
			if mkErr := os.MkdirAll(hostPath, 0o755); mkErr != nil {
				log.Printf("[sandbox] task=%s skipping mount: cannot create host path %s: %v", taskID, hostPath, mkErr)
				continue
			}
			log.Printf("[sandbox] task=%s created host directory %s", taskID, hostPath)
		}
		mode := m.Mode
		if mode != "ro" && mode != "rw" {
			mode = "rw" // default to rw
		}
		createArgs = append(createArgs,
			"-v", hostPath+":"+m.ContainerPath+":rprivate,"+mode,
		)
		log.Printf("[sandbox] task=%s mounting %s -> %s (%s)", taskID, hostPath, m.ContainerPath, mode)
	}

	// Environment variables (user-configured per-project)
	for _, e := range opts.SandboxEnvs {
		if e.Key != "" {
			createArgs = append(createArgs, "-e", e.Key+"="+e.Value)
			log.Printf("[sandbox] task=%s setting env %s", taskID, e.Key)
		}
	}

	// Container entrypoint: just sleep (agent is started via podman exec)
	createArgs = append(createArgs, image,
		"/bin/sh", "-c",
		"chmod +x /sandbox-bridge && sleep infinity",
	)

	out, err := sm.podmanExec(createArgs...)
	if err != nil {
		return nil, fmt.Errorf("create container: %w (%s)", err, out)
	}

	// Copy agent binary into container (before start)
	cpOut, cpErr := sm.podmanExec("cp", agentPath, containerName+":"+"/sandbox-bridge")
	if cpErr != nil {
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return nil, fmt.Errorf("copy agent to container: %w (%s)", cpErr, cpOut)
	}

	// Start the container (needed for exec sessions)
	// --runtime-flag ignore-cgroups=true is required for rootless gVisor
	startOut, err := sm.podmanExec("--runtime-flag", "ignore-cgroups=true", "start", containerName)
	if err != nil {
		// Clean up on start failure
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return nil, fmt.Errorf("start container: %w (%s)", err, startOut)
	}

	// Start agent in container via podman exec (stdin/stdout protocol)
	// --runtime-flag ignore-cgroups=true is required for rootless gVisor
	agentCmd := exec.Command(
		"podman", "--runtime-flag", "ignore-cgroups=true",
		"exec", "-i", "-w", mountPath, containerName,
		"/sandbox-bridge",
	)

	// Create pipes for communication with the agent
	stdin, err := agentCmd.StdinPipe()
	if err != nil {
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := agentCmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	agentCmd.Stderr = nil // agent logs go to container stderr

	if err := agentCmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return nil, fmt.Errorf("start agent: %w", err)
	}

	sm.active[taskID] = containerName
	sm.mountPaths[taskID] = mountPath
	sm.agents[taskID] = &agentConn{cmd: agentCmd, stdin: stdin, stdout: stdout}

	log.Printf("[sandbox] task=%s agent ready", taskID)
	return assignedPorts, nil
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
// Used for JSONL-based processes like coding agents.
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
// Returns (*ShellProcess, assignedPorts, error).
func (sm *SandboxManager) StartShell(taskID string) (*ShellProcess, []types.AssignedPort, error) {
	return sm.startShell(taskID, false, "", "", "", "", SandboxOpts{})
}

// StartShellWithCreate starts an interactive shell, creating the sandbox container
// if it doesn't already exist. Used for project-level terminals where there's no
// task lifecycle to manage the container. The caller is responsible for calling
// Close() on the ShellProcess and Stop() on the container when done.
// Returns (*ShellProcess, assignedPorts, error).
func (sm *SandboxManager) StartShellWithCreate(taskID, worktreePath, projectName, projectPath, projectID string, opts SandboxOpts) (*ShellProcess, []types.AssignedPort, error) {
	return sm.startShell(taskID, true, worktreePath, projectName, projectPath, projectID, opts)
}

// ExecProject runs a command inside the project's sandbox container,
// creating it on demand if it doesn't exist. Returns the combined stdout/stderr.
// This is for project-level filesystem operations (read, write, search, etc.).
func (sm *SandboxManager) ExecProject(projectID, projectPath, projectName, command string, opts SandboxOpts) (string, error) {
	taskID := "proj:" + projectID
	if err := sm.ensureProjectContainer(taskID, projectPath, projectName, projectID, opts); err != nil {
		return "", err
	}
	return sm.Exec(taskID, command, nil)
}

// ensureProjectContainer ensures the project-level sandbox container exists and is running.
// Creates it if missing, or recreates it if stopped/crashed.
// Uses ContainerIsRunning which has a TTL cache to avoid repeated podman inspect calls.
func (sm *SandboxManager) ensureProjectContainer(taskID, projectPath, projectName, projectID string, opts SandboxOpts) error {
	if !sm.ContainerIsRunning(taskID) {
		_ = sm.Stop(taskID) // clean up stale entry if any
		if _, err := sm.Create(taskID, projectPath, projectName, "", opts); err != nil {
			return fmt.Errorf("create sandbox container: %w", err)
		}
	}
	return nil
}

// ExecProjectWithInput runs a command inside the project's sandbox container
// with stdin input, creating the container on demand if needed.
func (sm *SandboxManager) ExecProjectWithInput(projectID, projectPath, projectName, command string, stdin []byte, opts SandboxOpts) (string, error) {
	taskID := "proj:" + projectID
	if err := sm.ensureProjectContainer(taskID, projectPath, projectName, projectID, opts); err != nil {
		return "", err
	}
	return sm.ExecWithInput(taskID, command, stdin, nil)
}

// ExecTask runs a command inside a task's sandbox container,
// creating it on demand if it doesn't exist. Returns the combined stdout/stderr.
// This is for task-level filesystem operations (read, write, etc.) in the diff view editor.
func (sm *SandboxManager) ExecTask(taskID, worktreePath, projectName, projectPath, command string, opts SandboxOpts) (string, error) {
	sm.mu.Lock()
	_, exists := sm.active[taskID]
	sm.mu.Unlock()
	if !exists {
		if _, err := sm.Create(taskID, worktreePath, projectName, projectPath, opts); err != nil {
			return "", fmt.Errorf("create sandbox container: %w", err)
		}
	}
	return sm.Exec(taskID, command, nil)
}

// ExecTaskWithInput runs a command inside a task's sandbox container
// with stdin input, creating the container on demand if needed.
func (sm *SandboxManager) ExecTaskWithInput(taskID, worktreePath, projectName, projectPath, command string, stdin []byte, opts SandboxOpts) (string, error) {
	sm.mu.Lock()
	_, exists := sm.active[taskID]
	sm.mu.Unlock()
	if !exists {
		if _, err := sm.Create(taskID, worktreePath, projectName, projectPath, opts); err != nil {
			return "", fmt.Errorf("create sandbox container: %w", err)
		}
	}
	return sm.ExecWithInput(taskID, command, stdin, nil)
}

func (sm *SandboxManager) startShell(taskID string, createIfMissing bool, worktreePath, projectName, projectPath, projectID string, opts SandboxOpts) (*ShellProcess, []types.AssignedPort, error) {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	mountPath := sm.mountPaths[taskID]
	sm.mu.Unlock()

	var assignedPorts []types.AssignedPort
	if !ok {
		if !createIfMissing || worktreePath == "" {
			return nil, nil, fmt.Errorf("no sandbox container for %s", taskID)
		}
		// Create container on demand
		var err error
		assignedPorts, err = sm.Create(taskID, worktreePath, projectName, projectPath, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("create sandbox container: %w", err)
		}
		sm.mu.Lock()
		containerName = sm.active[taskID]
		mountPath = sm.mountPaths[taskID]
		sm.mu.Unlock()
	} else if !sm.ContainerIsRunning(taskID) {
		// Container exists in map but is not running — recreate it
		log.Printf("[sandbox] task=%s container %s not running, recreating", taskID, containerName)
		_ = sm.Stop(taskID)
		var err error
		assignedPorts, err = sm.Create(taskID, worktreePath, projectName, projectPath, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("recreate sandbox container: %w", err)
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
		return nil, nil, fmt.Errorf("start shell: %w", err)
	}

	// Set non-blocking mode on the PTY
	syscall.SetNonblock(int(ptmx.Fd()), true)

	sp := &ShellProcess{
		Cmd:  cmd,
		Ptmx: ptmx,
	}

	log.Printf("[sandbox] task=%s started shell in container %s", taskID, containerName)
	return sp, assignedPorts, nil
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
// This is suitable for JSONL-based protocols like coding agent RPC mode.
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

// BinaryAvailable checks if the given binary is available inside the task's sandbox container.
// Returns true if `which <binaryName>` succeeds inside the container.
func (sm *SandboxManager) BinaryAvailable(taskID, binaryName string) bool {
	if binaryName == "" {
		return false
	}
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	sm.mu.Unlock()

	if !ok {
		return false
	}

	_, err := sm.podmanExec("exec", containerName, "which", binaryName)
	return err == nil
}

// Stop removes the sandbox container for a task.
func (sm *SandboxManager) Stop(taskID string) error {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	if ok {
		delete(sm.active, taskID)
	}
	client := sm.clients[taskID]
	if client != nil {
		delete(sm.clients, taskID)
	}
	ac := sm.agents[taskID]
	if ac != nil {
		delete(sm.agents, taskID)
	}
	delete(sm.livenessCache, taskID)
	sm.mu.Unlock()

	// Close agent client
	if client != nil {
		_ = client.Close()
	}
	// Kill agent process (podman exec session)
	if ac != nil && ac.cmd.Process != nil {
		_ = ac.stdin.Close()
		_ = ac.stdout.Close()
		_ = ac.cmd.Process.Kill()
		_ = ac.cmd.Wait()
	}

	if !ok {
		return nil // already stopped
	}

	// Remove container (force stop if running)
	err := sm.rt.RemoveContainer(containerName)
	if err != nil {
		log.Printf("[sandbox] task=%s warning: failed to remove container: %v", taskID, err)
		return fmt.Errorf("remove container: %w", err)
	}

	log.Printf("[sandbox] task=%s removed container %s", taskID, containerName)
	return nil
}

// HasActiveContainer returns true if a sandbox container for the given task is
// tracked in the active map (i.e., was created in the current server session).
func (sm *SandboxManager) HasActiveContainer(taskID string) bool {
	sm.mu.Lock()
	_, ok := sm.active[taskID]
	sm.mu.Unlock()
	return ok
}

// ContainerIsRunning returns true if the container is tracked in the active map
// AND is actually running in podman (verified via podman inspect).
// Results are cached for livenessTTL (5 min) to avoid expensive podman inspect
// calls on every FS operation. Returns false if the container is not tracked
// or is stopped/crashed.
func (sm *SandboxManager) ContainerIsRunning(taskID string) bool {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	// Check TTL cache: if we confirmed running recently, skip podman inspect.
	if ok {
		lastCheck := sm.livenessCache[taskID]
		if time.Since(lastCheck) < livenessTTL {
			sm.mu.Unlock()
			return true
		}
	}
	sm.mu.Unlock()

	if !ok {
		return false
	}

	running, err := sm.rt.IsRunning(containerName)
	if err != nil {
		running = false
	}

	sm.mu.Lock()
	if running {
		sm.livenessCache[taskID] = time.Now()
	} else {
		delete(sm.livenessCache, taskID)
	}
	sm.mu.Unlock()
	return running
}

// GetContainerPorts queries podman for the live port mappings of a container.
// Returns an empty slice if the container is not tracked or has no port mappings.
func (sm *SandboxManager) GetContainerPorts(taskID string) ([]types.AssignedPort, error) {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	sm.mu.Unlock()
	if !ok {
		return nil, nil
	}
	out, err := sm.podmanExec("port", containerName)
	if err != nil {
		return nil, fmt.Errorf("podman port: %w", err)
	}
	var ports []types.AssignedPort
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "8080/tcp -> 0.0.0.0:12345"
		parts := strings.Split(line, " -> ")
		if len(parts) != 2 {
			continue
		}
		containerPart := strings.Split(parts[0], "/")[0] // "8080/tcp" -> "8080"
		hostPart := strings.Split(parts[1], ":")[1]      // "0.0.0.0:12345" -> "12345"
		var containerPort, hostPort int
		if _, err := fmt.Sscanf(containerPart, "%d", &containerPort); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(hostPart, "%d", &hostPort); err != nil {
			continue
		}
		ports = append(ports, types.AssignedPort{HostPort: hostPort, ContainerPort: containerPort})
	}
	return ports, nil
}

// ImageReady checks if a container image is available locally.
func (sm *SandboxManager) ImageReady(image string) bool {
	if image == "" {
		image = BaseImage
	}
	if strings.HasPrefix(image, "-") {
		return false
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
	if strings.HasPrefix(image, "-") {
		return fmt.Errorf("image reference must not start with a dash: %q", image)
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
	if strings.HasPrefix(dockerfilePath, "-") {
		return fmt.Errorf("dockerfile path must not start with a dash: %q", dockerfilePath)
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

// StopProjectContainers stops all sandbox containers associated with a project.
// This includes task-level containers (identified by taskIDs) and the project-level
// container (identified by "proj:" + projectID). Used when sandbox settings change
// so containers are recreated with the new settings on next use.
func (sm *SandboxManager) StopProjectContainers(projectID string, taskIDs []string) {
	// Stop task-level containers
	for _, taskID := range taskIDs {
		_ = sm.Stop(taskID)
	}
	// Stop project-level container
	_ = sm.Stop("proj:" + projectID)
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
