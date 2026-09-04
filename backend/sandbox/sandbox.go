// Package sandbox owns the containers a task runs in: podman with the gVisor runsc
// runtime, the container environment, loopback-bound port forwarding, interactive shells,
// and the sandbox-bridge client.
// There is no listener and no port for the bridge - a connection is one podman exec whose
// lifetime is its consumer's. A sandbox is only created when gVisor is verified, with no
// fallback to the host.
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
	// MountRoot is where a project's worktree is bind-mounted inside its
	// container. Build paths with MountPath, never by hand — the fs API's
	// containment check compares against this same value.
	MountRoot = "/home/sandbox"
	// livenessTTL is how long to cache the result of a container liveness check.
	// Avoids expensive podman inspect calls on every FS operation.
	livenessTTL = 5 * time.Minute
)

// MountPath is the in-container path of projectName's worktree. The result is
// cleaned, so a project name stored before ValidateProjectName rejected ".."
// segments cannot move the mount outside MountRoot.
func MountPath(projectName string) string {
	return filepath.Join(MountRoot, projectName)
}

// TrimMountPrefix maps a container path under the project's mount to the same
// path relative to the mount ("." for the mount root itself). ok is false when
// p is not under that mount.
func TrimMountPrefix(projectName, p string) (rel string, ok bool) {
	mount := MountPath(projectName)
	clean := filepath.Clean(p)
	switch {
	case clean == mount:
		return ".", true
	case strings.HasPrefix(clean, mount+string(filepath.Separator)):
		return clean[len(mount)+1:], true
	default:
		return "", false
	}
}

// ContainerToHost translates a path inside the project's sandbox mount to the
// matching host path under worktreePath. ok is false when p is not under the
// mount, i.e. the caller already holds a host path.
func ContainerToHost(worktreePath, projectName, p string) (string, bool) {
	rel, ok := TrimMountPrefix(projectName, p)
	if !ok {
		return "", false
	}
	if rel == "." {
		return filepath.Clean(worktreePath), true
	}
	return filepath.Join(worktreePath, rel), true
}

// HostToContainer translates a host path under worktreePath to where the same
// file sits under the project's sandbox mount.
func HostToContainer(worktreePath, projectName, p string) string {
	rel, err := filepath.Rel(filepath.Clean(worktreePath), filepath.Clean(p))
	if err != nil {
		return MountPath(projectName)
	}
	return filepath.Join(MountPath(projectName), rel)
}

// SandboxOpts holds the configuration for sandbox container creation and execution.
//
// A dependency directory granted to an LSP extension is deliberately not representable here. What a
// task container may see on the host is decided per project, in its Sandbox Configuration mounts, and
// a setting made once for the whole install would both hand that directory read-write to every
// project's sandbox and cover the project image's own toolchain cache at the same container path. The
// one container that binds a grant is the extension's own sandbox, read-only — see
// SandboxConfig.DependencyGrant in backend/extensions.
type SandboxOpts struct {
	BaseImage        string
	NetworkIsolation bool
	MemoryLimit      int
	ForwardPorts     []types.PortForward
	AgentSandbox     *types.AgentSandbox // mounts and envs for agent sandbox
	SkillsDir        string
	SandboxMounts    []types.SandboxMount
	SandboxEnvs      []types.SandboxEnv
	Capabilities     []string
	PlanMode         bool
}

// shellTERM is what an attached terminal reports. It belongs to the terminal
// session, not to the sandbox, so it is set on the shell's own exec.
const shellTERM = "xterm-256color"

// containerEnvArgs returns the -e flags for a sandbox container: the variables configured for it and
// nothing else. Order is lowest to highest priority — the agent extension's envs, then the project's own
// configuration, since podman takes the last -e. Nothing is invented here: -e overrides the image's ENV,
// so a hardcoded PATH or TERM would silently replace what the image sets.
func containerEnvArgs(taskID string, opts SandboxOpts) []string {
	var args []string
	// The agent's declared environment first, so the project's own configuration wins a clash on the
	// same name (podman takes the last -e): an install-wide default loses to a project that named the
	// variable itself.
	add := func(source string, envs []types.SandboxEnv) {
		for _, e := range envs {
			if e.Key == "" {
				continue
			}
			args = append(args, "-e", e.Key+"="+e.Value)
			// Values are never logged — they are routinely API keys and tokens.
			log.Printf("[sandbox] task=%s setting %s env %s", taskID, source, e.Key)
		}
	}
	if opts.AgentSandbox != nil {
		add("agent", opts.AgentSandbox.Envs)
	}
	add("sandbox", opts.SandboxEnvs)
	return args
}

// shellExecArgs builds the podman arguments for an interactive shell. TERM is set
// here, on the session holding the TTY; the container itself carries only what
// was configured for it.
func shellExecArgs(containerName, mountPath, shell string) []string {
	return []string{
		"exec", "-it",
		"-e", "TERM=" + shellTERM,
		"-w", mountPath,
		containerName,
		shell,
	}
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
	active        map[string]string             // taskID -> containerName
	mountPaths    map[string]string             // taskID -> mount path inside container
	agents        map[string]*agentConn         // taskID -> agent pipes + process
	clients       map[string]*sandbox.Client    // taskID -> agent client (cached)
	livenessCache map[string]time.Time          // taskID -> last time running was confirmed
	mem           *sandboxruntime.MemoryTracker // taskID -> container memory, via containerName
	// imageOps holds in-flight image builds/pulls by image ref, so concurrent sandbox starts share
	// one build. imageOpsMu is separate from mu: a build runs for minutes and must not block the
	// container maps.
	imageOpsMu sync.Mutex
	imageOps   map[string]*imageOp
	available  bool
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
		mem:           sandboxruntime.NewMemoryTracker(rt),
		imageOps:      make(map[string]*imageOp),
	}
	sm.available = sm.checkAvailable()
	if sm.available {
		log.Println("[sandbox] gVisor available, sandboxing enabled")
		// Skip cleanup if MIKE_SANDBOX_SKIP_STARTUP_CLEANUP is set (e.g. during tests
		// where TestMain handles cleanup once to avoid races between parallel tests).
		if !sandboxruntime.SkipStartupCleanup() {
			sm.cleanupStaleContainers()
		}
	} else {
		log.Println("[sandbox] gVisor not available (runsc not found), sandboxing disabled")
	}
	return sm
}

// cleanupStaleContainers removes any kanban-sandbox-* containers left behind.
func (sm *SandboxManager) cleanupStaleContainers() {
	sm.rt.CleanupStaleContainers("sandbox", ContainerNamePrefix+"-")
}

// IsAvailable returns true if gVisor (runsc) is installed and podman can use it.
func (sm *SandboxManager) IsAvailable() bool {
	return sm.available
}

// checkAvailable verifies that runsc and podman are usable. The same probe backs
// the extension sandbox manager — see sandboxruntime.GVisorAvailable.
func (sm *SandboxManager) checkAvailable() bool {
	return sandboxruntime.GVisorAvailable("sandbox")
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

// forwardPortSpec renders the podman `-p` value publishing hostPort →
// containerPort. The host side is pinned to loopback: only this machine's server
// reads a sandbox's forwarded port (the bridge, LSP, terminal port checks), so
// binding every interface would expose a container's listener on the LAN.
func forwardPortSpec(hostPort, containerPort int) string {
	return fmt.Sprintf("127.0.0.1:%d:%d", hostPort, containerPort)
}

func createContainerArgs(containerName, mountPath string) []string {
	return []string{
		"--runtime-flag=overlay2=root:self",
		"--runtime-flag=ignore-cgroups",
		"create",
		// Every caller ensures the image first, with progress the browser can show. An implicit
		// pull here would be silent and unbounded, and would fail as a resolution error instead of
		// "this image was never built".
		"--pull", "never",
		"--name", containerName,
		"--runtime", "runsc",
		"--security-opt", "label=disable",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--entrypoint", "",
		"--workdir", mountPath,
	}
}

// Create creates a gVisor sandbox container for a task.
//
// Mounts: the worktree at /home/sandbox/<project.Name> (read-only in plan mode, read-write
// otherwise); project.Path/.git read-only at its own absolute path, so git can resolve refs,
// objects and worktree metadata; opts.AgentSandbox mounts and envs (default
// /root/.config/agent); skillsDir read-only at SkillsMount when non-empty; opts.SandboxMounts
// appended last.
//
// Configuration: opts.BaseImage or the BaseImage constant; --network none when networkIsolation;
// --memory=<limit>M when memoryLimit > 0; environment limited to what was configured (see
// containerEnvArgs) — PATH and the rest stay as the image sets them.
//
// Each opts.ForwardPorts entry gets an ephemeral host port (net.Listen ":0") mapped to the
// container port and bound to loopback; the returned slice lists those mappings, nil on failure.
func (sm *SandboxManager) Create(taskID, worktreePath string, project *types.Project, opts SandboxOpts) ([]types.AssignedPort, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	projectName, projectPath := project.Name, project.Path

	// Validate image ref before any resource allocation to prevent argument
	// injection (e.g. leading dash being interpreted as a podman flag).
	image := opts.BaseImage
	if image == "" {
		image = BaseImage
	}
	if err := sm.rt.ValidateImageRef(image); err != nil {
		return nil, fmt.Errorf("invalid sandbox image: %w", err)
	}
	// Generated names are qualified already; this covers a value stored before that rule existed.
	// See pkg/sandboxruntime/image_ref.go for why podman must never see a short name.
	image = sm.rt.ResolveImageRef(image)

	if !sm.available {
		return nil, fmt.Errorf("gVisor not available (runsc not installed)")
	}

	// Sanitize ID: podman container names must match [a-zA-Z0-9][a-zA-Z0-9_.-]*
	safeID, err := sm.rt.SanitizeID(taskID)
	if err != nil {
		return nil, fmt.Errorf("invalid task ID for sandbox container: %w", err)
	}
	containerName := fmt.Sprintf("%s-%s", ContainerNamePrefix, safeID)

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

	absWorktree, err := filepath.Abs(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path: %w", err)
	}
	mountPath := MountPath(projectName)

	// Resolve agent binary path (required)
	agentPath, err := sm.rt.GetAgentBinaryPath()
	if err != nil {
		return nil, fmt.Errorf("sandbox-bridge binary not found: %w", err)
	}

	// Create container with gVisor runtime
	createArgs := createContainerArgs(containerName, mountPath)
	// Add back any explicitly requested capabilities
	for _, cap := range opts.Capabilities {
		createArgs = append(createArgs, "--cap-add", cap)
	}
	worktreeMode := "rw"
	if opts.PlanMode {
		worktreeMode = "ro"
	}
	// No -e here: the container's environment comes from containerEnvArgs below.
	createArgs = append(createArgs,
		"-v", absWorktree+":"+mountPath+":rprivate,"+worktreeMode,
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
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Printf("[sandbox] task=%s failed to assign host port for container port %d: %v", taskID, pf.ContainerPort, err)
			continue
		}
		hostPort := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		createArgs = append(createArgs, "-p", forwardPortSpec(hostPort, pf.ContainerPort))
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

	// Agent sandbox mounts (only when an agent extension is configured). Its
	// envs go through containerEnvArgs below, with the rest.
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

	// Environment: exactly what was configured for this sandbox. A dependency cache granted to an LSP
	// extension is deliberately not among it — see SandboxOpts.
	createArgs = append(createArgs, containerEnvArgs(taskID, opts)...)

	// Container entrypoint: just sleep (agent is started via podman exec)
	createArgs = append(createArgs, image,
		"/bin/sh", "-c",
		"chmod +x /sandbox-bridge && sleep infinity",
	)

	out, err := sm.podmanExec(createArgs...)
	if err != nil {
		// "image not known" is the interesting case and podman buries it; say what to do instead.
		if imgErr := sandboxruntime.ImageAvailabilityError(image, out); imgErr != nil {
			return nil, fmt.Errorf("create container: %w", imgErr)
		}
		return nil, fmt.Errorf("create container: %w (%s)", err, out)
	}

	// Copy agent binary into container (before start)
	cpOut, cpErr := sm.podmanExec("cp", agentPath, containerName+":"+"/sandbox-bridge")
	if cpErr != nil {
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return nil, fmt.Errorf("copy agent to container: %w (%s)", cpErr, cpOut)
	}

	// Start the container (needed for exec sessions). ignore-cgroups is a podman
	// global option, so it precedes the subcommand; the create path spells it
	// --runtime-flag=ignore-cgroups, which is equivalent.
	startOut, err := sm.podmanExec("--runtime-flag", "ignore-cgroups=true", "start", containerName)
	if err != nil {
		_, _ = sm.podmanExec("rm", "-f", containerName)
		return nil, fmt.Errorf("start container: %w (%s)", err, startOut)
	}

	// Enforce the memory limit in the container's cgroup. runsc (with the
	// required ignore-cgroups flag) never applies the OCI spec's
	// linux.resources, so podman's --memory alone is not enforced. Fail open:
	// if the limit cannot be applied (e.g. non-cgroup-v2 host), log loudly and
	// continue — the sandbox still works, just without a cap.
	if opts.MemoryLimit > 0 {
		if err := sm.rt.ApplyMemoryLimit(containerName, opts.MemoryLimit); err != nil {
			log.Printf("[sandbox] task=%s WARNING: memory limit %dMB not enforced (cgroup write failed): %v", taskID, opts.MemoryLimit, err)
		}
	}

	// Start agent in container via podman exec (stdin/stdout protocol)
	// ignore-cgroups must precede `exec` — it is a podman global option.
	// The bridge needs the bound most: its child (an LSP server) inherits the pipes carrying the
	// connection, so a dead bridge with a live server is a Wait that never returns.
	agentCmd := podmanCommand(
		"--runtime-flag", "ignore-cgroups=true",
		"exec", "-i", "-w", mountPath, containerName,
		"/sandbox-bridge",
	)

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

	for _, e := range env {
		execArgs = append(execArgs, "-e", e)
	}

	// Run sh -c to handle shell features
	execArgs = append(execArgs, "sh", "-c", command)

	c := podmanCommand(execArgs...)

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

// StartShell starts an interactive shell, creating the sandbox container
// if it doesn't already exist. Used for project-level terminals where there's no
// task lifecycle to manage the container. The caller is responsible for calling
// Close() on the ShellProcess and Stop() on the container when done.
// Returns (*ShellProcess, assignedPorts, error).
func (sm *SandboxManager) StartShell(taskID, worktreePath string, project *types.Project, opts SandboxOpts) (*ShellProcess, []types.AssignedPort, error) {
	return sm.startShell(taskID, worktreePath, project, opts)
}

// ExecProject runs a command inside the project's sandbox container,
// creating it on demand if it doesn't exist. Returns the combined stdout/stderr.
// This is for project-level filesystem operations (read, write, search, etc.).
func (sm *SandboxManager) ExecProject(project *types.Project, command string, opts SandboxOpts) (string, error) {
	taskID := "proj:" + project.ID
	if err := sm.ensureProjectContainer(taskID, project, opts); err != nil {
		return "", err
	}
	return sm.Exec(taskID, command, nil)
}

// ensureProjectContainer ensures the project-level sandbox container exists and is running.
// Creates it if missing, or recreates it if stopped/crashed.
// Uses ContainerIsRunning which has a TTL cache to avoid repeated podman inspect calls.
func (sm *SandboxManager) ensureProjectContainer(taskID string, project *types.Project, opts SandboxOpts) error {
	if !sm.ContainerIsRunning(taskID) {
		_ = sm.Stop(taskID) // clean up stale entry if any
		if _, err := sm.Create(taskID, project.Path, project, opts); err != nil {
			return fmt.Errorf("create sandbox container: %w", err)
		}
		return nil
	}

	// A running container may be stale: its worktree bind mount may point at an
	// old directory (e.g. the project was moved and its path updated). Reuse the
	// container only when the mounted host path still matches the current
	// project path; otherwise stop and recreate it so the file browser and other
	// FS operations reflect the project's current location.
	absWorktree, err := filepath.Abs(project.Path)
	if err != nil {
		return fmt.Errorf("resolve project path: %w", err)
	}
	mountPath := MountPath(project.Name)
	sm.mu.Lock()
	containerName := sm.active[taskID]
	sm.mu.Unlock()
	source, err := sm.rt.GetContainerMountSource(containerName, mountPath)
	if err != nil {
		// If we can't inspect the container, assume it's stale and recreate it
		// rather than risk serving a stale mount.
		log.Printf("[sandbox] project=%s cannot inspect mount source (recreating): %v", project.ID, err)
		source = ""
	}
	if filepath.Clean(source) != filepath.Clean(absWorktree) {
		log.Printf("[sandbox] project=%s mount source %q != worktree %q (recreating stale container)", project.ID, source, absWorktree)
		_ = sm.Stop(taskID)
		if _, err := sm.Create(taskID, project.Path, project, opts); err != nil {
			return fmt.Errorf("create sandbox container: %w", err)
		}
	}
	return nil
}

// ExecProjectWithInput runs a command inside the project's sandbox container
// with stdin input, creating the container on demand if needed.
func (sm *SandboxManager) ExecProjectWithInput(project *types.Project, command string, stdin []byte, opts SandboxOpts) (string, error) {
	taskID := "proj:" + project.ID
	if err := sm.ensureProjectContainer(taskID, project, opts); err != nil {
		return "", err
	}
	return sm.ExecWithInput(taskID, command, stdin, nil)
}

// ExecTask runs a command inside a task's sandbox container,
// creating it on demand if it doesn't exist. Returns the combined stdout/stderr.
// This is for task-level filesystem operations (read, write, etc.) in the diff view editor.
func (sm *SandboxManager) ExecTask(taskID, worktreePath string, project *types.Project, command string, opts SandboxOpts) (string, error) {
	sm.mu.Lock()
	_, exists := sm.active[taskID]
	sm.mu.Unlock()
	if !exists {
		if _, err := sm.Create(taskID, worktreePath, project, opts); err != nil {
			return "", fmt.Errorf("create sandbox container: %w", err)
		}
	}
	return sm.Exec(taskID, command, nil)
}

func (sm *SandboxManager) startShell(taskID, worktreePath string, project *types.Project, opts SandboxOpts) (*ShellProcess, []types.AssignedPort, error) {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	mountPath := sm.mountPaths[taskID]
	sm.mu.Unlock()

	var assignedPorts []types.AssignedPort
	if !ok {
		if worktreePath == "" {
			return nil, nil, fmt.Errorf("no sandbox container for %s", taskID)
		}
		// Create container on demand
		var err error
		assignedPorts, err = sm.Create(taskID, worktreePath, project, opts)
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
		assignedPorts, err = sm.Create(taskID, worktreePath, project, opts)
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
	cmd := podmanCommand(shellExecArgs(containerName, mountPath, shell)...)
	// No cmd.Env: podman needs the server's own environment (rootless storage keys
	// off HOME / XDG_*), and none of it reaches the container — podman exec
	// inherits the container's environment unless -e says otherwise.

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, nil, fmt.Errorf("start shell: %w", err)
	}

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

	// Same reason as the bridge: this is a long-lived connection, and its wait must be bounded.
	cmd := podmanCommand(execArgs...)

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
	if ok {
		sm.mem.Forget(containerName)
	}

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

// ActiveTaskIDs returns the IDs of all containers tracked in the current
// server session (task-level IDs plus project-level "proj:..." keys).
func (sm *SandboxManager) ActiveTaskIDs() []string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	ids := make([]string, 0, len(sm.active))
	for id := range sm.active {
		ids = append(ids, id)
	}
	return ids
}

// GetMemoryStats returns the live memory usage of the task's sandbox container
// by reading the container's cgroup v2 memory controller
func (sm *SandboxManager) GetMemoryStats(taskID string) *types.SandboxMemoryStats {
	sm.mu.Lock()
	name, tracked := sm.active[taskID]
	sm.mu.Unlock()

	if !tracked {
		return &types.SandboxMemoryStats{}
	}

	usage := sm.mem.Usage(name)
	return &types.SandboxMemoryStats{
		Running:   usage.Known,
		CurrentMB: usage.Current,
		LimitMB:   usage.Limit,
		PeakMB:    usage.Peak,
		OOMKills:  usage.OOMKills,
	}
}

// ImageReady reports whether image is available locally. An empty image means the app-wide
// default; a reference podman could misread as a flag is refused without asking it.
func (sm *SandboxManager) ImageReady(image string) bool {
	if image == "" {
		image = BaseImage
	}
	return sm.rt.ImageExists(image)
}

// PullImageWithProgress downloads a container image, streaming progress lines via onProgress.
// onProgress is called for each line of output (nil means no callback).
func (sm *SandboxManager) PullImageWithProgress(image string, onProgress func(line string)) error {
	if image == "" {
		image = BaseImage
	}
	// A short name cannot be pulled either: podman prompts for the registry and has no TTY.
	if err := sm.rt.ValidateImageRef(image); err != nil {
		return err
	}
	image = sm.rt.ResolveImageRef(image)
	log.Printf("[sandbox] pulling image %s", image)

	if err := sm.streamPodman(podmanCommand("pull", image), "pull", image, "pull", onProgress); err != nil {
		return err
	}
	log.Printf("[sandbox] image %s pulled", image)
	return nil
}

// streamPodman runs a podman command whose output is worth watching while it happens — a
// pull or a build takes minutes. stdout and stderr share one pipe because podman writes
// progress to stderr, and one interleaved stream renders readably.
//
// onProgress may be nil; every line is logged under logTag either way. The error names op
// and image so a caller can show it as-is.
// podmanCommand is how this package starts podman, so every call carries WaitDelay: cmd.Wait waits
// for the output pipes, not just the process, and a wedged container holds them open.
func podmanCommand(args ...string) *exec.Cmd {
	c := exec.Command("podman", args...)
	c.WaitDelay = sandboxruntime.PodmanWaitDelay
	return c
}

func (sm *SandboxManager) streamPodman(cmd *exec.Cmd, op, image, logTag string, onProgress func(line string)) error {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("start %s: %w", op, err)
	}

	// Stream output line by line
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			if onProgress != nil {
				onProgress(line)
			}
			log.Printf("[sandbox] %s: %s", logTag, line)
		}
	}()

	waitErr := cmd.Wait()
	pw.Close() // signal EOF to reader goroutine
	if waitErr != nil {
		return fmt.Errorf("%s image %s: %w", op, image, waitErr)
	}
	return nil
}

// ProjectImageName is the image name a project's Dockerfile image is built under, with a
// sanitized project name for readability.
//
// It is qualified with the local registry on purpose: a project name may contain a dot, so
// "sandbox-getmike.dev" looks like a host but is a short name to podman (see image_ref.go) -
// and `podman build -t sandbox-x` stores the result as "localhost/sandbox-x" anyway.
func ProjectImageName(projectName string) string {
	safeName := strings.ReplaceAll(projectName, " ", "-")
	safeName = strings.ReplaceAll(safeName, "/", "-")
	safeName = strings.ToLower(safeName)
	if ProjectImagePrefix == "" {
		return sandboxruntime.QualifyLocalImageRef(safeName)
	}
	return sandboxruntime.QualifyLocalImageRef(fmt.Sprintf("%s-%s", ProjectImagePrefix, safeName))
}

// BuildOptions controls optional behavior of an image build.
type BuildOptions struct {
	// NoCache disables the container build layer cache (podman build --no-cache).
	NoCache bool
}

// buildPodmanBuildArgs returns the podman arguments for an image build.
// The context directory is always last so a flag can never swallow it.
func buildPodmanBuildArgs(imageName, contextDir string, noCache bool) []string {
	args := []string{"build", "-t", imageName}
	if noCache {
		args = append(args, "--no-cache")
	}
	return append(args, contextDir)
}

// BuildImage builds a container image from a Dockerfile and tags it with the
// project-specific name. The Dockerfile is copied to a temp directory for the build.
// The temp directory is cleaned up after the build completes.
func (sm *SandboxManager) BuildImage(projectName, dockerfilePath string) error {
	return sm.BuildImageWithProgress(projectName, dockerfilePath, nil)
}

// BuildImageWithProgress builds a container image, streaming progress lines via onProgress.
// onProgress is called for each line of output (nil means no callback).
// The build layer cache is used; use BuildImageWithOptions to disable it.
func (sm *SandboxManager) BuildImageWithProgress(projectName, dockerfilePath string, onProgress func(line string)) error {
	return sm.BuildImageWithOptions(projectName, dockerfilePath, BuildOptions{}, onProgress)
}

// BuildImageWithOptions builds a container image with explicit options, streaming
// progress lines via onProgress (nil means no callback).
func (sm *SandboxManager) BuildImageWithOptions(projectName, dockerfilePath string, opts BuildOptions, onProgress func(line string)) error {
	if !sm.available {
		return fmt.Errorf("gVisor not available (runsc not installed)")
	}
	// Rejects an empty path and one starting with a dash, which podman would
	// otherwise read as a flag.
	if err := sandboxruntime.ValidateDockerfilePath(dockerfilePath); err != nil {
		return err
	}

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

	// Validate the derived image name before passing to podman to prevent
	// argument injection (e.g. if ProjectImagePrefix is ever emptied).
	if err := sm.rt.ValidateImageRef(imageName); err != nil {
		return fmt.Errorf("invalid project image name: %w", err)
	}

	// Remove any existing image with the same name
	_, _ = sm.podmanExec("rmi", imageName) // best-effort

	// Build the image with streaming output
	log.Printf("[sandbox] project=%s building image %s from %s (no_cache=%v)", projectName, imageName, dockerfilePath, opts.NoCache)
	if err := sm.streamPodman(podmanCommand(buildPodmanBuildArgs(imageName, tmpDir, opts.NoCache)...), "build", imageName, "build", onProgress); err != nil {
		return err
	}
	log.Printf("[sandbox] project=%s image %s built successfully", projectName, imageName)
	return nil
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
