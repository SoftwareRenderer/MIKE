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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"mike/backend/prompts"
	"mike/backend/toolresult"
	"mike/backend/types"
	"mike/pkg/bridge"
	"mike/pkg/logging"
	"mike/pkg/sandboxruntime"
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

// projectKeyPrefix marks the container and terminal session a project owns directly, which has no
// task to be keyed by.
const projectKeyPrefix = "proj:"

// ProjectKey is the key a project's own container and its project terminal are held under, because
// neither has a task ID. A key of any other shape is a task's.
func ProjectKey(projectID string) string { return projectKeyPrefix + projectID }

// IsProjectKey reports whether a key names a project's own container or terminal rather than a task's.
func IsProjectKey(key string) bool { return strings.HasPrefix(key, projectKeyPrefix) }

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
	// WorktreeReadOnly mounts the task's worktree read-only in the container. Plan mode is the
	// only operating mode that asks for it; explore writes, like act.
	WorktreeReadOnly bool
}

// ProjectOpts is the container configuration a project's own row asks for. What a caller decides
// rather than the project — the image it settled on, the folders an agent extension was granted, and
// whether the worktree mounts read-only — is set on the result, so a new project column reaches
// every container a project gets.
func ProjectOpts(project *types.Project) SandboxOpts {
	return SandboxOpts{
		NetworkIsolation: project.SandboxNetworkIsolation,
		MemoryLimit:      project.SandboxMemoryLimit,
		ForwardPorts:     project.ForwardPorts,
		SkillsDir:        prompts.SkillsDir(),
		SandboxMounts:    project.SandboxMounts,
		SandboxEnvs:      project.SandboxEnvs,
		Capabilities:     project.SandboxCapabilities,
	}
}

// shellTERM is what an attached terminal reports. It belongs to the terminal
// session, not to the sandbox, so it is set on the shell's own exec.
const shellTERM = "xterm-256color"

// containerEnvArgs returns the -e flags for a sandbox container: the variables configured for it and
// nothing else, lowest priority first (podman takes the last -e). Nothing is invented here: -e overrides
// the image's ENV, so a hardcoded PATH or TERM would silently replace what the image sets.
//
// It returns an error and Create refuses to start the container on one, because emitting what parsed and
// dropping the rest produces a container with an environment nobody configured. types.SandboxEnvArgs
// assembles the list and cannot produce a bare `-e NAME`, which podman reads as "copy NAME out of the
// server's own environment".
func containerEnvArgs(taskID string, opts SandboxOpts) ([]string, error) {
	var args []string
	// The agent's declared environment first, so the project's own configuration wins a clash on the
	// same name (podman takes the last -e): an install-wide default loses to a project that named the
	// variable itself.
	if opts.AgentSandbox != nil {
		agentArgs, err := types.SandboxEnvArgs(opts.AgentSandbox.Envs)
		if err != nil {
			return nil, fmt.Errorf("agent extension env: %w", err)
		}
		args = append(args, agentArgs...)
		// Values are never logged — they are routinely API keys and tokens.
		for _, e := range opts.AgentSandbox.Envs {
			if e.Key != "" {
				logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("setting agent env %s", e.Key)
			}
		}
	}
	projectArgs, err := types.SandboxEnvArgs(opts.SandboxEnvs)
	if err != nil {
		return nil, fmt.Errorf("project sandbox configuration: %w", err)
	}
	args = append(args, projectArgs...)
	for _, e := range opts.SandboxEnvs {
		if e.Key != "" {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("setting sandbox env %s", e.Key)
		}
	}
	return args, nil
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

// memory answers the tracker to call; the nil-tracker rule is sandboxruntime.MemoryOrDefault's.
func (sm *SandboxManager) memory() sandboxruntime.MemoryUsage {
	return sandboxruntime.MemoryOrDefault(sm.mem)
}

// SandboxManager manages gVisor sandbox containers for tasks.
type SandboxManager struct {
	mu            sync.Mutex
	rt            *sandboxruntime.Runtime
	active        map[string]string          // taskID -> containerName
	mountPaths    map[string]string          // taskID -> mount path inside container
	agents        map[string]*agentConn      // taskID -> agent pipes + process
	clients       map[string]*bridge.Client  // taskID -> agent client (cached)
	livenessCache map[string]time.Time       // taskID -> last time running was confirmed
	mem           sandboxruntime.MemoryUsage // taskID -> container memory, via containerName
	// capsMB is each live container's limit in MB as it was created, or 0 when it is unlimited. It is
	// recorded here because the project's configured limit can change while the container is running.
	capsMB map[string]int64
	// Set under mu; never called with mu held — the sink writes to SQLite.
	peakSink func(taskID string, peakMB int64, oomKills int64)
	// imageOps holds in-flight image builds/pulls by image ref, so concurrent sandbox starts share
	// one build. imageOpsMu is separate from mu: a build runs for minutes and must not block the
	// container maps.
	imageOpsMu sync.Mutex
	imageOps   map[string]*imageOp
	available  bool
}

// NewSandboxManager creates a new SandboxManager and checks gVisor availability.
func NewSandboxManager(boot sandboxruntime.Boot) *SandboxManager {
	rt := sandboxruntime.NewRuntimeWithBoot(boot)
	sm := &SandboxManager{
		rt:            rt,
		active:        make(map[string]string),
		mountPaths:    make(map[string]string),
		agents:        make(map[string]*agentConn),
		clients:       make(map[string]*bridge.Client),
		livenessCache: make(map[string]time.Time),
		mem:           sandboxruntime.NewMemoryTracker(rt),
		imageOps:      make(map[string]*imageOp),
	}
	sm.available = sm.checkAvailable()
	if sm.available {
		logging.For(logging.Sandbox).Info("gVisor available, sandboxing enabled")
		// The sweep is a host-wide delete, so it happens only when this process claimed that
		// authority at startup (server.NewServer does; a test binary's zero Boot does not).
		if boot.SweepStaleContainersAtBoot {
			sm.cleanupStaleContainers()
		}
	} else {
		logging.For(logging.Sandbox).Info("gVisor not available (runsc not found), sandboxing disabled")
	}
	return sm
}

// SetMemoryPeakSink registers the receiver of a container's final memory record.
func (sm *SandboxManager) SetMemoryPeakSink(sink func(taskID string, peakMB int64, oomKills int64)) {
	sm.mu.Lock()
	sm.peakSink = sink
	sm.mu.Unlock()
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
func (sm *SandboxManager) GetClient(taskID string) (*bridge.Client, error) {
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

	client := bridge.NewClient(ac.stdin, ac.stdout)

	sm.mu.Lock()
	sm.clients[taskID] = client
	sm.mu.Unlock()
	return client, nil
}

// forwardPortSpec renders the podman `-p` value publishing hostPort on hostIP as a
// container port. Empty hostIP binds every interface, which podman spells as no address.
// The bind address also sets where pasta sends the connection, and a gVisor container owns
// only the namespace's public address: a loopback bind maps a port nothing answers.
func forwardPortSpec(hostIP string, hostPort, containerPort int) string {
	if hostIP == "" {
		return fmt.Sprintf("%d:%d", hostPort, containerPort)
	}
	// podman needs an IPv6 address bracketed.
	return net.JoinHostPort(hostIP, strconv.Itoa(hostPort)) + ":" + strconv.Itoa(containerPort)
}

// createContainerArgs is the task sandbox's head of the create: the shared gVisor posture, plus
// the one task-only flag.
func createContainerArgs(containerName, mountPath string) []string {
	return append(sandboxruntime.GVisorCreateArgs(containerName), "--workdir", mountPath)
}

// Create creates a gVisor sandbox container for a task.
//
// Mounts: the worktree at /home/sandbox/<project.Name> (read-only in plan mode);
// project.Path/.git read-only at its own absolute path, so git can resolve refs and
// worktree metadata; opts.AgentSandbox mounts and envs; skillsDir read-only at SkillsMount
// when non-empty; opts.SandboxMounts appended last.
//
// Configuration: opts.BaseImage or the BaseImage constant; --network none when
// networkIsolation; --memory=<limit>M when memoryLimit > 0; environment limited to what was
// configured (see containerEnvArgs). Each opts.ForwardPorts entry gets an ephemeral host port;
// the returned slice lists those mappings, nil on failure.
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
	if err := sandboxruntime.ValidateImageRef(image); err != nil {
		return nil, fmt.Errorf("invalid sandbox image: %w", err)
	}
	// Generated names are qualified already; this covers a value stored before that rule existed.
	// See pkg/sandboxruntime/image_ref.go for why podman must never see a short name.
	image = sm.rt.ResolveImageRef(image)

	// Capabilities and mounts reach podman as written, so they are checked here as well
	// as by the API that stores them: a project row can predate the rule. These are the
	// rules in pkg/sandboxruntime, the same ones an extension sandbox is refused under.
	//
	// Before the availability check: an unsafe configuration is a configuration error,
	// and reporting "runsc is missing" instead would hide it.
	if err := sandboxruntime.ValidateCapabilities(opts.Capabilities); err != nil {
		return nil, fmt.Errorf("sandbox capability for project %s: %w", projectName, err)
	}
	for i, m := range opts.SandboxMounts {
		if err := types.ValidateSandboxMount(m); err != nil {
			return nil, fmt.Errorf("sandbox mount %d for project %s: %w", i, projectName, err)
		}
	}
	// These are folders the user approved for the extension, not folders this project chose, so they get
	// the same shape check as the project's own mounts.
	if opts.AgentSandbox != nil {
		for i, m := range opts.AgentSandbox.Mounts {
			if m.HostPath == "" || m.ContainerPath == "" {
				continue
			}
			if err := types.ValidateSandboxMount(m); err != nil {
				return nil, fmt.Errorf("agent sandbox mount %d for project %s: %w", i, projectName, err)
			}
		}
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
	// AppendCapabilities is the only path to a --cap-add; it refuses the whole grant if this
	// command line stopped isolating as the grant assumes.
	if len(opts.Capabilities) > 0 {
		createArgs, err = sandboxruntime.AppendCapabilities(createArgs, opts.Capabilities)
		if err != nil {
			return nil, fmt.Errorf("cannot grant sandbox capabilities to project %s: %w", projectName, err)
		}
	}
	worktreeMode := "rw"
	if opts.WorktreeReadOnly {
		worktreeMode = "ro"
	}
	// No -e here: the container's environment comes from containerEnvArgs below.
	createArgs = append(createArgs,
		"-v", sandboxruntime.BindMountSpec(absWorktree, mountPath, worktreeMode),
	)
	if opts.NetworkIsolation {
		createArgs = append(createArgs, "--network", "none")
	}
	// Port forwarding: auto-assign an ephemeral host port for each container port.
	var assignedPorts []types.AssignedPort
	for _, pf := range opts.ForwardPorts {
		if pf.ContainerPort < 1 || pf.ContainerPort > 65535 {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("skipping invalid container port: %d", pf.ContainerPort)
			continue
		}
		hostIP, err := types.NormalizeSandboxHostIP(pf.HostIP)
		if err != nil {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("skipping port %d: %v", pf.ContainerPort, err)
			continue
		}
		// Binding the probe to hostIP reports a missing interface here, against the port we
		// were asked for, instead of later as a podman failure.
		ln, err := net.Listen("tcp", net.JoinHostPort(hostIP, "0"))
		if err != nil {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("failed to assign host port on %q for container port %d: %v", hostIP, pf.ContainerPort, err)
			continue
		}
		hostPort := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		binding := forwardPortSpec(hostIP, hostPort, pf.ContainerPort)
		createArgs = append(createArgs, "-p", binding)
		assignedPorts = append(assignedPorts, types.AssignedPort{HostPort: hostPort, ContainerPort: pf.ContainerPort, HostIP: hostIP})
		logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("publishing %s for container port %d", binding, pf.ContainerPort)
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
				"-v", sandboxruntime.BindMountSpec(gitDir, gitDir, "ro"),
			)
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("mounting .git read-only: %s", gitDir)
		}
	}

	// Folders the manifest suggests are not used unless the user approved them. A missing folder is created
	// here so that a read-write approval works inside the container. Environment variables are added by
	// containerEnvArgs below.
	if opts.AgentSandbox != nil {
		for _, m := range opts.AgentSandbox.Mounts {
			if m.HostPath == "" || m.ContainerPath == "" {
				continue
			}
			absHost, err := filepath.Abs(m.HostPath)
			if err != nil {
				logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("skipping agent mount: invalid host path %s: %v", m.HostPath, err)
				continue
			}
			if _, err := os.Stat(absHost); err != nil {
				if mkErr := os.MkdirAll(absHost, 0o755); mkErr != nil {
					logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("skipping agent mount %s -> %s: cannot create the granted directory: %v", absHost, m.ContainerPath, mkErr)
					continue
				}
				logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("created granted agent directory %s", absHost)
			}
			mountMode := m.Mode
			if mountMode != "ro" && mountMode != "rw" {
				mountMode = "ro"
			}
			createArgs = append(createArgs,
				"-v", sandboxruntime.BindMountSpec(absHost, m.ContainerPath, mountMode),
			)
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("mounting agent sandbox %s -> %s (%s)", absHost, m.ContainerPath, mountMode)
		}
	}

	// Skills directory mount (read-only, for skill packages)
	if opts.SkillsDir != "" {
		absSkillsDir, err := filepath.Abs(opts.SkillsDir)
		if err != nil {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("skipping skills mount: invalid path %s: %v", opts.SkillsDir, err)
		} else if _, err := os.Stat(absSkillsDir); err == nil {
			createArgs = append(createArgs,
				"-v", sandboxruntime.BindMountSpec(absSkillsDir, SkillsMount, "ro"),
			)
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("mounting skills read-only: %s -> %s", absSkillsDir, SkillsMount)
		}
	}

	// Custom mounts (user-configured per-project)
	for _, m := range opts.SandboxMounts {
		hostPath, err := filepath.Abs(m.HostPath)
		if err != nil {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("skipping mount: invalid host path %s: %v", m.HostPath, err)
			continue
		}
		if _, err := os.Stat(hostPath); err != nil {
			if mkErr := os.MkdirAll(hostPath, 0o755); mkErr != nil {
				logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("skipping mount: cannot create host path %s: %v", hostPath, mkErr)
				continue
			}
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("created host directory %s", hostPath)
		}
		mode := m.Mode
		if mode != "ro" && mode != "rw" {
			mode = "rw" // default to rw
		}
		createArgs = append(createArgs,
			"-v", sandboxruntime.BindMountSpec(hostPath, m.ContainerPath, mode),
		)
		logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("mounting %s -> %s (%s)", hostPath, m.ContainerPath, mode)
	}

	// Environment: exactly what was configured for this sandbox, and nothing else. Built here, at
	// the point of use, which is still before the container exists — `podman run` below is the
	// create, and everything above it is read-only — so a malformed configuration row refuses the
	// create rather than starting a container with a partial environment. A dependency cache granted
	// to an LSP extension is deliberately not among it: see SandboxOpts.
	envArgs, err := containerEnvArgs(taskID, opts)
	if err != nil {
		return nil, fmt.Errorf("task %s: sandbox environment rejected: %w", taskID, err)
	}
	createArgs = append(createArgs, envArgs...)

	// Container entrypoint: just sleep (agent is started via podman exec)
	createArgs = append(createArgs, image,
		"/bin/sh", "-c",
		"chmod +x /sandbox-bridge && sleep infinity",
	)

	// The stale-container bracket sits here, not at the top. `docs/sandbox-runtime.md`.
	exists, err := sm.rt.ContainerExists(containerName)
	if err != nil {
		return nil, fmt.Errorf("check container: %w", err)
	}
	if exists {
		// Clean up stale container from a previous server run
		logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("removing stale container %s", containerName)
		if rmErr := sm.rt.RemoveContainer(containerName); rmErr != nil {
			return nil, fmt.Errorf("remove stale container %s: %w", containerName, rmErr)
		}
	}

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
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("WARNING: memory limit %dMB not enforced (cgroup write failed): %v", opts.MemoryLimit, err)
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
	if sm.capsMB == nil {
		sm.capsMB = map[string]int64{}
	}
	sm.capsMB[taskID] = int64(opts.MemoryLimit)
	sm.mountPaths[taskID] = mountPath
	sm.agents[taskID] = &agentConn{cmd: agentCmd, stdin: stdin, stdout: stdout}

	logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("agent ready")
	return assignedPorts, nil
}

// Exec runs a command inside the task's sandbox container.
// Returns the combined stdout/stderr output.
//
// env is a declared-variable list, not a list of names, and that is the point of the type: a bare
// `-e NAME` tells podman to copy NAME out of the environment of the podman process — the server's —
// so a signature that accepted names would make host-environment leakage the natural-looking thing
// to write. types.SandboxEnvArgs cannot emit one.
func (sm *SandboxManager) Exec(taskID, command string, env []types.SandboxEnv) (string, error) {
	return sm.execInternal(taskID, command, nil, env)
}

// ExecWithInput runs a command inside the task's sandbox container with stdin input.
// Returns the combined stdout/stderr output.
func (sm *SandboxManager) ExecWithInput(taskID, command string, stdin []byte, env []types.SandboxEnv) (string, error) {
	return sm.execInternal(taskID, command, stdin, env)
}

// execInternal is Exec and ExecWithInput's shared body; a non-nil stdin is piped to the command.
func (sm *SandboxManager) execInternal(taskID, command string, stdin []byte, env []types.SandboxEnv) (string, error) {
	// Validated before the container lookup: a malformed declaration is the caller's bug and should
	// report itself as one whether or not a container happens to be running.
	envArgs, err := types.SandboxEnvArgs(env)
	if err != nil {
		return "", fmt.Errorf("task %s: exec environment rejected: %w", taskID, err)
	}

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

	execArgs = append(execArgs, envArgs...)

	// Run sh -c to handle shell features
	execArgs = append(execArgs, "sh", "-c", command)

	c := podmanCommand(execArgs...)

	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}

	// The capture bounds what one tool command can make this process hold: output past
	// MaxCaptureBytes is counted, not stored, and the text says it stopped.
	out := toolresult.NewCapture()
	c.Stdout = out
	c.Stderr = out
	err = c.Run()
	if err != nil {
		return out.String(), fmt.Errorf("exec failed: %w", err)
	}
	return out.String(), nil
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
	taskID := ProjectKey(project.ID)
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
		logging.For(logging.Sandbox).With(logging.ProjectID, project.ID).Infof("cannot inspect mount source (recreating): %v", err)
		source = ""
	}
	if filepath.Clean(source) != filepath.Clean(absWorktree) {
		logging.For(logging.Sandbox).With(logging.ProjectID, project.ID).Infof("mount source %q != worktree %q (recreating stale container)", source, absWorktree)
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
	taskID := ProjectKey(project.ID)
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
		logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("container %s not running, recreating", containerName)
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
	// cmd.Env comes from podmanCommand → sandboxruntime.PodmanEnv: the rootless-podman essentials
	// (HOME, XDG_RUNTIME_DIR, the D-Bus address) and nothing else — notably not
	// MIKE_LOGIN_PASSWORD or MIKE_ENCRYPTION_KEY, which os.Environ() used to hand this process.
	// The claim that none of it reaches the container is still true and still the reason the
	// terminal's own environment is the container's: podman exec inherits the container's
	// environment unless an -e says otherwise.
	cmd := podmanCommand(shellExecArgs(containerName, mountPath, shell)...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, nil, fmt.Errorf("start shell: %w", err)
	}

	syscall.SetNonblock(int(ptmx.Fd()), true)

	sp := &ShellProcess{
		Cmd:  cmd,
		Ptmx: ptmx,
	}

	logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("started shell in container %s", containerName)
	return sp, assignedPorts, nil
}

// Close terminates the process handle and cleans up pipes.
func (ph *ProcessHandle) Close() {
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
}

// Close terminates the shell process and cleans up the PTY.
func (sp *ShellProcess) Close() {
	if sp.Ptmx != nil {
		_ = sp.Ptmx.Close()
	}
	if sp.Cmd != nil && sp.Cmd.Process != nil {
		_ = sp.Cmd.Process.Kill()
		_ = sp.Cmd.Wait()
	}
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

	logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("started process %q in container %s (pid=%d)", command, containerName, cmd.Process.Pid)
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

// recordFinalPeak gives the sink the container's last memory numbers. Stop calls it before
// removal: memory.peak dies with the container.
func (sm *SandboxManager) recordFinalPeak(taskID, containerName string) {
	sm.mu.Lock()
	sink := sm.peakSink
	sm.mu.Unlock()
	mem := sm.memory()
	if sink != nil {
		// Reporting neither number records nothing, so a run cannot erase a previous one.
		if usage := mem.Usage(containerName); usage.Known && (usage.Peak > 0 || usage.OOMKills > 0) {
			sink(taskID, usage.Peak, int64(usage.OOMKills))
		}
	}
	mem.Forget(containerName)
}

// retainContainer keeps a task mapped to its container when `podman rm -f` fails. Stop drops the
// mapping first, so without this a later Stop finds nothing to remove, and the startup sweep skips
// running containers: the container survives until the server shuts down cleanly. The mapping is
// restored only while the task still names this container.
func (sm *SandboxManager) retainContainer(taskID, containerName string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if cur, tracked := sm.active[taskID]; !tracked || cur == containerName {
		sm.active[taskID] = containerName
	}
}

// Charges returns what each live container costs the memory budget, in MB, keyed by the task ID it
// was created for. The cost of one container is sandboxruntime.ChargeMB.
func (sm *SandboxManager) Charges() map[string]int64 {
	charges, _ := sm.memoryCharges()
	return charges
}

// MemoryAccounting returns, in MB, what this manager's live containers hold and what they cost the
// memory budget.
func (sm *SandboxManager) MemoryAccounting() (heldMB, chargeMB int64) {
	charges, heldMB := sm.memoryCharges()
	for _, charge := range charges {
		chargeMB += charge
	}
	return heldMB, chargeMB
}

// memoryCharges returns what each live container costs the budget, keyed by task ID, and what they hold
// in total.
func (sm *SandboxManager) memoryCharges() (map[string]int64, int64) {
	sm.mu.Lock()
	containers := make(map[string]string, len(sm.active))
	caps := make(map[string]int64, len(sm.active))
	for id, name := range sm.active {
		containers[id] = name
		caps[id] = sm.capsMB[id]
	}
	sm.mu.Unlock()

	charges := make(map[string]int64, len(containers))
	var heldMB int64
	mem := sm.memory()
	for id, name := range containers {
		used, charge := sandboxruntime.ChargeMB(mem.Usage(name), caps[id])
		heldMB += used
		charges[id] = charge
	}
	return charges, heldMB
}

// Stop removes the sandbox container for a task.
func (sm *SandboxManager) Stop(taskID string) error {
	sm.mu.Lock()
	containerName, ok := sm.active[taskID]
	if ok {
		delete(sm.active, taskID)
		delete(sm.capsMB, taskID)
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
		sm.recordFinalPeak(taskID, containerName)
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
		logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("warning: failed to remove container: %v", err)
		sm.retainContainer(taskID, containerName)
		return fmt.Errorf("remove container: %w", err)
	}

	logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("removed container %s", containerName)
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

// parsePortBinding reads one `podman port` line. IPv6 addresses are printed unbracketed
// ("fd00::5:18121"), so the host port is whatever follows the last colon.
func parsePortBinding(line string) (types.AssignedPort, bool) {
	left, right, ok := strings.Cut(line, " -> ")
	if !ok {
		return types.AssignedPort{}, false
	}
	containerPort, err := strconv.Atoi(strings.Split(left, "/")[0])
	if err != nil {
		return types.AssignedPort{}, false
	}
	i := strings.LastIndex(right, ":")
	if i < 0 {
		return types.AssignedPort{}, false
	}
	hostPort, err := strconv.Atoi(right[i+1:])
	if err != nil {
		return types.AssignedPort{}, false
	}
	// A wildcard address, and "*" where podman prints it, mean every interface: "".
	host, err := types.NormalizeSandboxHostIP(right[:i])
	if err != nil {
		host = ""
	}
	return types.AssignedPort{HostPort: hostPort, ContainerPort: containerPort, HostIP: host}, true
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
	seen := make(map[types.AssignedPort]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		port, ok := parsePortBinding(line)
		if !ok {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Infof("ignoring unreadable port line %q", line)
			continue
		}
		// One container port bound on both families reads as the same mapping twice.
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
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

	return types.NewSandboxMemoryStats(sm.memory().Usage(name))
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
	if err := sandboxruntime.ValidateImageRef(image); err != nil {
		return err
	}
	image = sm.rt.ResolveImageRef(image)
	logging.For(logging.Sandbox).Infof("pulling image %s", image)

	if err := sm.streamPodman(podmanCommand("pull", image), "pull", image, "pull", onProgress); err != nil {
		return err
	}
	logging.For(logging.Sandbox).Infof("image %s pulled", image)
	return nil
}

// podmanCommand is how this package starts podman. It delegates to sandboxruntime.PodmanCommand so the
// environment allowlist and WaitDelay are set in one place: cmd.Wait waits for the output pipes, not just
// the process, and a wedged container holds them open.
func podmanCommand(args ...string) *exec.Cmd {
	return sandboxruntime.PodmanCommand(args...)
}

// streamPodman runs a podman command whose output is worth watching - a pull or a build takes minutes.
// stdout and stderr share one pipe because podman writes progress to stderr, and one interleaved stream
// renders readably. onProgress may be nil; every line is logged under logTag either way. The error names
// op and image so a caller can show it as-is.
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
			logging.For(logging.Sandbox).Infof("%s: %s", logTag, line)
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
	if err := sandboxruntime.ValidateImageRef(imageName); err != nil {
		return fmt.Errorf("invalid project image name: %w", err)
	}

	// Remove any existing image with the same name
	_, _ = sm.podmanExec("rmi", imageName) // best-effort

	// Build the image with streaming output
	logging.For(logging.Sandbox).With(logging.ProjectID, projectName).Infof("building image %s from %s (no_cache=%v)", imageName, dockerfilePath, opts.NoCache)
	if err := sm.streamPodman(podmanCommand(buildPodmanBuildArgs(imageName, tmpDir, opts.NoCache)...), "build", imageName, "build", onProgress); err != nil {
		return err
	}
	logging.For(logging.Sandbox).With(logging.ProjectID, projectName).Infof("image %s built successfully", imageName)
	return nil
}

// StopProjectContainers stops all sandbox containers associated with a project.
// This includes task-level containers (identified by taskIDs) and the project-level
// container (identified by ProjectKey). Used when sandbox settings change
// so containers are recreated with the new settings on next use.
func (sm *SandboxManager) StopProjectContainers(projectID string, taskIDs []string) {
	// Stop task-level containers
	for _, taskID := range taskIDs {
		_ = sm.Stop(taskID)
	}
	// Stop project-level container
	_ = sm.Stop(ProjectKey(projectID))
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
