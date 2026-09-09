package extensions

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kanban/backend/types"
	"kanban/pkg/bridge"
	"kanban/pkg/logging"
	"kanban/pkg/sandboxruntime"
)

const (
	// ExtensionContainerPrefix is the prefix for extension sandbox container names.
	ExtensionContainerPrefix = "kanban-ext"
	// DefaultExtensionMemory is the default memory limit for extension sandboxes (MB).
	// 0 means no limit — extension sandboxes run unlimited unless the user sets one.
	DefaultExtensionMemory = 0
	// ExtensionLivenessTTL is how long to cache a liveness check result.
	ExtensionLivenessTTL = 5 * time.Minute
	// ExtensionStopGracePeriod is how long to keep a sandbox alive after the last
	// client disconnects, to avoid restart overhead if the user quickly reconnects.
	ExtensionStopGracePeriod = 15 * time.Minute
)

// ExtensionSandboxManager manages gVisor sandbox containers for extensions.
type ExtensionSandboxManager struct {
	mu            sync.Mutex
	rt            *sandboxruntime.Runtime
	refs          map[string]int                    // extID -> active bridge-connection count
	states        map[string]*extensionSandboxState // extID -> runtime state
	mem           *sandboxruntime.MemoryTracker     // extID -> container memory, via containerName
	registry      *RegistryClient
	store         *ExtensionStore
	available     bool
	extensionsDir string
	workspaceDir  string
}

// extensionSandboxState tracks the runtime state of an extension sandbox.
type extensionSandboxState struct {
	containerName string
	// sessions are the live bridge connections into this container, each with the `podman exec`
	// behind it. There is no single "the" client: a language server belongs to one workspace, so
	// every consumer gets its own connection and the agent keeps its session on that connection.
	sessions      map[*bridge.Client]*exec.Cmd
	ports         []types.AssignedPort
	createdAt     time.Time
	lastCheckedAt time.Time
	isAlive       bool
	// stopTimer fires after ExtensionStopGracePeriod when refcount hits 0.
	// Cancelled if a new client connects before it fires.
	stopTimer *time.Timer
	// mountedProjects is the sorted list of host project paths mounted into the container (each
	// at /mnt/<project-dirname>); a change in this set means the container must be recreated.
	mountedProjects []string
	// customMounts is the sorted list of "host:container:mode" strings for the
	// extension's SandboxConfig.Mounts, used to detect when custom mounts
	// (e.g. a node_modules mount) have changed and the container needs recreation.
	customMounts []string
	// envs is the sorted "KEY=VALUE" list the container was created with. The
	// environment is fixed at creation, so an env change needs a recreate — including
	// the granted dependency assignment, so revoking a grant recycles the sandbox.
	envs []string
}

func NewExtensionSandboxManager(reg *RegistryClient, store *ExtensionStore) *ExtensionSandboxManager {
	rt := sandboxruntime.NewRuntime()
	sm := &ExtensionSandboxManager{
		rt:            rt,
		refs:          make(map[string]int),
		states:        make(map[string]*extensionSandboxState),
		mem:           sandboxruntime.NewMemoryTracker(rt),
		registry:      reg,
		store:         store,
		available:     checkExtensionSandboxAvailable(),
		extensionsDir: "extensions",
	}
	if sm.available {
		logging.For(logging.Sandbox).Info("gVisor available, extension sandboxing enabled")
	} else {
		logging.For(logging.Sandbox).Info("gVisor not available, extension sandboxing disabled")
	}
	// Clean up stale containers from previous runs — except when this process is a test binary
	// that has no business touching the host's sandboxes (see SkipStartupCleanup), and except
	// when there is no gVisor to sweep for in the first place.
	if sm.available && !sandboxruntime.SkipStartupCleanup() {
		sm.cleanupStaleContainers()
	}
	return sm
}

// checkExtensionSandboxAvailable checks if gVisor and podman are available. Same
// probe as the task sandbox manager — see sandboxruntime.GVisorAvailable.
func checkExtensionSandboxAvailable() bool {
	return sandboxruntime.GVisorAvailable("ext-sandbox")
}

// cleanupStaleContainers removes any kanban-ext-* containers left behind. The list call is
// time-bounded: a wedged podman must not block server startup.
func (sm *ExtensionSandboxManager) cleanupStaleContainers() {
	sm.rt.CleanupStaleContainers("ext-sandbox", ExtensionContainerPrefix+"-")
}

// IsAvailable returns true if extension sandboxing is available.
func (sm *ExtensionSandboxManager) IsAvailable() bool {
	return sm.available
}

// SetExtensionsDir records the directory extension files are materialized in: a mount-allowlist
// root, and where findLocalDockerfile looks.
func (sm *ExtensionSandboxManager) SetExtensionsDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	sm.extensionsDir = dir
}

// SetWorkspaceDir records the configured worktrees/workspace directory, so it
// can be part of the mount allowlist.
func (sm *ExtensionSandboxManager) SetWorkspaceDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	sm.workspaceDir = dir
}

// MountPolicy returns the host roots an extension sandbox may mount: workspace dir, registered
// project paths, the extensions dir, the temp dir, the EnvMountRoots extras, and the paths
// projects declare in their own Sandbox Configuration. Which of those a sandbox may write is
// ForceReadOnly's rule. Built on demand, so new projects apply without a restart.
//
// Install and settings validation (ExtensionHandler.mountPolicy) never gets anyone's granted
// dependency directory: a grant enters only through MountPolicyForExtension.
func (sm *ExtensionSandboxManager) MountPolicy() *MountPolicy {
	return sm.mountPolicy("")
}

// MountPolicyForExtension is MountPolicy plus the dependency directory granted to THIS
// extension, pinned read-only. It is not a widening: a person named that directory for this
// extension and could already have mounted it as ordinary sandbox configuration. What it buys
// is that the bind passes the same gate as every other mount (ValidateHostPath, container-path
// charset, read-only pinning) instead of trusting the stored string.
//
// The pin is unconditional: one granted directory is shared by every project this extension
// serves, and a language server writing into a cache every project reads is how one project's
// turn changes another's dependencies.
func (sm *ExtensionSandboxManager) MountPolicyForExtension(ext *Extension) *MountPolicy {
	var grant string
	if ext != nil {
		grant = ext.SandboxConfig.DependencyGrant()
	}
	return sm.mountPolicy(grant)
}

func (sm *ExtensionSandboxManager) mountPolicy(grantHostPath string) *MountPolicy {
	var (
		workspaceDir  string
		extensionsDir string
		projectPaths  []string
		projectMounts []string
	)
	if sm != nil {
		workspaceDir = sm.workspaceDir
		extensionsDir = sm.extensionsDir
		var err error
		if projectPaths, err = sm.store.ListProjectPaths(); err != nil {
			logging.For(logging.Sandbox).Infof("WARNING: cannot list project paths for the mount allowlist: %v", err)
			projectPaths = nil
		}
		if projectMounts, err = sm.store.ListProjectSandboxMountPaths(); err != nil {
			logging.For(logging.Sandbox).Infof("WARNING: cannot list project sandbox mounts for the mount allowlist: %v", err)
			projectMounts = nil
		}
	}

	roots := append(mountRoots(extensionsDir, workspaceDir, projectPaths), projectMounts...)
	readOnlyRoots := append([]string{workspaceDir}, projectPaths...)
	readOnlyRoots = append(readOnlyRoots, projectMounts...)
	if grantHostPath != "" {
		roots = append(roots, grantHostPath)
		readOnlyRoots = append(readOnlyRoots, grantHostPath)
	}
	return newMountPolicyWithReadOnly(roots, readOnlyRoots)
}

// extensionMemoryLimit resolves the memory limit (MB) for an extension's sandbox: 0 means no
// limit, negative values are clamped to 0.
func extensionMemoryLimit(ext *Extension) int {
	if ext.SandboxConfig.MemoryLimit < 0 {
		return 0
	}
	return ext.SandboxConfig.MemoryLimit
}

// DependencyMount is what a granted dependency directory gives a container: a mount plus the
// environment assignment that points at it, or the reason there is nothing to bind.
//
// Derived in one place and used on both sides of the lifecycle: if the compatibility check and
// the create each computed their own answer, a container built before a grant would be called
// compatible with it and the grant would never take effect. The assignment belongs on the
// container, not the server process.
type DependencyMount struct {
	Mount  types.SandboxMount
	Env    types.SandboxEnv
	OK     bool
	Reason string // why nothing is bound; empty when there is no grant at all, or when OK
}

// dependencyBind resolves the grant against the extension's own policy. Pass the per-extension one
// (MountPolicyForExtension): with the narrow policy the granted path is not a root.
//
// A grant that cannot be honored is reported, not fatal. The extension still starts, resolves what
// its image contains, and its environment check says so; refusing to create the container would turn
// a deleted directory into an extension that will not load at all.
func dependencyBind(ext *Extension, policy *MountPolicy) DependencyMount {
	host := ext.SandboxConfig.DependencyGrant()
	if host == "" {
		return DependencyMount{}
	}
	declared := ext.SandboxConfig.Dependency
	resolved, err := policy.ValidateHostPath(host)
	if err != nil {
		return DependencyMount{Reason: fmt.Sprintf("the granted directory %s cannot be mounted: %v", host, err)}
	}
	// Checked here too, not only when the grant was applied: a directory deleted afterwards would be
	// recreated by podman root-owned at container start, and the task that later warms it could not
	// write to it.
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		if err != nil {
			return DependencyMount{Reason: fmt.Sprintf("the granted directory %s is not there any more: %v", resolved, err)}
		}
		return DependencyMount{Reason: fmt.Sprintf("the granted path %s is not a directory", resolved)}
	}
	return DependencyMount{
		// Read-only, always: the mode is not a manifest's to choose and not this container's to want.
		Mount: types.SandboxMount{HostPath: resolved, ContainerPath: declared.ContainerPath, Mode: "ro"},
		// The container path, not the host path: the host path means nothing in there, and must not
		// reach a process that could report it back. Both halves are fields the manifest already
		// states, so this derives nothing the manifest could invent at runtime; an extension whose
		// dependencies sit in the checkout (node_modules) or in the image declares no cache at all
		// and is answered above with no mount.
		Env: types.SandboxEnv{Key: declared.Env, Value: declared.ContainerPath},
		OK:  true,
	}
}

// DesiredDependencyMount is what a create for ext would bind. Compatibility checks need this, not
// their own guess.
func (sm *ExtensionSandboxManager) DesiredDependencyMount(ext *Extension) DependencyMount {
	if ext == nil {
		return DependencyMount{}
	}
	return dependencyBind(ext, sm.MountPolicyForExtension(ext))
}

// withDependencyMount appends the bind to a mount list for the snapshot a compatibility check
// compares against. No-op when nothing is bound.
func withDependencyMount(mounts []types.SandboxMount, bind DependencyMount) []types.SandboxMount {
	if !bind.OK {
		return mounts
	}
	return append(append([]types.SandboxMount(nil), mounts...), bind.Mount)
}

// withDependencyEnv is withDependencyMount for the env list, and for the same reason: both sides of
// the comparison have to include it, or every check asks for a recreate.
func withDependencyEnv(envs []types.SandboxEnv, bind DependencyMount) []types.SandboxEnv {
	if !bind.OK {
		return envs
	}
	return append(append([]types.SandboxEnv(nil), envs...), bind.Env)
}

// buildExtensionCreateArgs assembles the `podman create` args for an extension sandbox (always
// `--network none`) and returns the mounted host project paths, so a mount-set change is
// detectable later.
//
// Mounts and capabilities are re-validated here so a value stored before validation existed is
// skipped and logged instead of becoming a container argument; a nil policy permits no custom
// mount at all (fail closed).
func buildExtensionCreateArgs(ext *Extension, containerName, image string, projectMounts map[string]string, policy *MountPolicy) ([]string, []string) {
	// Container runs sleep infinity; sandbox-bridge is started via podman exec when needed.
	createArgs := append(sandboxruntime.GVisorCreateArgs(containerName), "--network", "none")

	// Capabilities. A malformed NAME is skipped one at a time — one stale entry in a stored manifest
	// must not cost the rest of the grant — while the POSTURE is all-or-nothing: AppendCapabilities
	// refuses the whole grant onto a command line that does not run runsc on top of a full
	// `--cap-drop ALL`, so a refusal here means this builder stopped asserting its own isolation and
	// the extension starts without the privileges it cannot safely be given.
	var valid []string
	for _, cap := range ext.SandboxConfig.Capabilities {
		if err := ValidateCapability(cap); err != nil {
			logging.For(logging.Sandbox).Infof("ext=%s SKIPPED capability %q: %v", ext.ID, cap, err)
			continue
		}
		valid = append(valid, cap)
	}
	if len(valid) > 0 {
		withCaps, err := sandboxruntime.AppendCapabilities(createArgs, valid)
		if err != nil {
			logging.For(logging.Sandbox).Errorf("ext=%s granted no capabilities (%v): %v", ext.ID, valid, err)
		} else {
			createArgs = withCaps
		}
	}

	// Mount each registered project at /mnt/<project-dirname> read-only. This allows the container
	// to serve any workspace without recreation. A source here is not always operator-chosen: the
	// current LSP workspace arrives in this map and originates from a client message, so it is
	// judged by the same policy as the extension's own mounts — resolved, then inside a root.
	// A refused source is still recorded, because the record is what MountsCompatible compares the
	// desired set against: recording it only when mounted would make every request look like
	// "project mounts changed" and recreate the sandbox forever.
	mountedProjects := make([]string, 0, len(projectMounts))
	for hostPath, containerPath := range projectMounts {
		if policy != nil {
			if _, err := policy.ValidateHostPath(hostPath); err != nil {
				logging.For(logging.Sandbox).Warnf("ext=%s SKIPPED project mount: %v", ext.ID, err)
				mountedProjects = append(mountedProjects, hostPath)
				continue
			}
		}
		createArgs = append(createArgs,
			"-v", hostPath+":"+containerPath+":rprivate,ro",
		)
		mountedProjects = append(mountedProjects, hostPath)
	}
	// Sort for deterministic comparison
	sort.Strings(mountedProjects)

	// Environment variables. Each one goes through types.SandboxEnvArgs, which can only produce
	// `-e KEY=VALUE`: a bare `-e NAME` means "copy NAME out of the podman process's environment" to
	// podman, and that process is the server, so an empty or malformed key must never reach the
	// argument vector. Skipped and logged rather than returned as an error, matching how mounts and
	// capabilities are handled here — and a manifest's envs are validated at install too.
	envArgs, err := types.SandboxEnvArgs([]types.SandboxEnv{{Key: "TERM", Value: "xterm-256color"}})
	if err != nil {
		logging.For(logging.Sandbox).Infof("ext=%s SKIPPED TERM: %v", ext.ID, err)
	}
	createArgs = append(createArgs, envArgs...)
	for _, env := range ext.SandboxConfig.Envs {
		assigned, err := types.SandboxEnvArgs([]types.SandboxEnv{env})
		if err != nil {
			logging.For(logging.Sandbox).Infof("ext=%s SKIPPED env %q: %v", ext.ID, env.Key, err)
			continue
		}
		createArgs = append(createArgs, assigned...)
	}

	// Memory limit (0 = no limit — omit the flag so the container starts unlimited)
	if memLimit := extensionMemoryLimit(ext); memLimit > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dM", memLimit))
	}

	// The granted dependency directory, first among the mounts: where it lands is declared by the
	// extension and cannot be moved, so a borrowed mount that wants the same container path is the
	// one that has to give way (see the collision check below).
	bind := dependencyBind(ext, policy)
	if bind.OK {
		bindEnv, err := types.SandboxEnvArgs([]types.SandboxEnv{bind.Env})
		if err != nil {
			logging.For(logging.Sandbox).Infof("ext=%s SKIPPED dependency-cache env: %v", ext.ID, err)
			bindEnv = nil
		}
		createArgs = append(createArgs,
			"-v", fmt.Sprintf("%s:%s:rprivate,%s", bind.Mount.HostPath, bind.Mount.ContainerPath, bind.Mount.Mode),
		)
		createArgs = append(createArgs, bindEnv...)
		logging.For(logging.Sandbox).Infof("ext=%s mounted the granted dependency directory %s at %s (%s) and assigned %s=%s", ext.ID, bind.Mount.HostPath, bind.Mount.ContainerPath, bind.Mount.Mode, bind.Env.Key, bind.Env.Value)
	} else if bind.Reason != "" {
		logging.For(logging.Sandbox).Infof("ext=%s SKIPPED dependency directory: %s — the server resolves only what its image contains, and its environment check will say so", ext.ID, bind.Reason)
	}

	// Custom mounts, each re-checked: the container path charset keeps the
	// "<host>:<container>:rprivate,<mode>" spec from being rewritten by the value
	// itself, and the host path must resolve inside one of the app's own roots.
	for _, m := range ext.SandboxConfig.Mounts {
		if bind.OK && m.ContainerPath == bind.Mount.ContainerPath {
			// Two -v specs on one container path make podman fail the whole create, which would
			// look like a broken extension rather than a mount that collides with a declaration.
			logging.For(logging.Sandbox).Infof("ext=%s SKIPPED mount (host %q -> container %q): core mounts the granted dependency directory there", ext.ID, m.HostPath, m.ContainerPath)
			continue
		}
		if err := ValidateContainerPath(m.ContainerPath); err != nil {
			logging.For(logging.Sandbox).Infof("ext=%s SKIPPED mount (host %q): %v", ext.ID, m.HostPath, err)
			continue
		}
		resolved, err := policy.ValidateHostPath(m.HostPath)
		if err != nil {
			logging.For(logging.Sandbox).Infof("ext=%s SKIPPED mount (container %q): %v", ext.ID, m.ContainerPath, err)
			continue
		}
		// Read-only unless the caller asked for write access to a path the app owns.
		// A path that only exists because a user configured it for a project stays
		// read-only no matter what mode was requested.
		mode := "ro"
		if m.Mode == "rw" && !policy.ForceReadOnly(resolved) {
			mode = "rw"
		}
		createArgs = append(createArgs,
			"-v", fmt.Sprintf("%s:%s:rprivate,%s", resolved, m.ContainerPath, mode),
		)
	}

	// Container entrypoint: sleep infinity (agent is started via podman exec)
	createArgs = append(createArgs, image,
		"/bin/sh", "-c",
		"chmod +x /sandbox-bridge && sleep infinity",
	)

	return createArgs, mountedProjects
}

// Create creates and starts an extension's sandbox container, which runs `sleep infinity`;
// the bridge is started later per connection via `podman exec`.
func (sm *ExtensionSandboxManager) Create(ext *Extension, projectMounts map[string]string) ([]types.AssignedPort, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.available {
		return nil, fmt.Errorf("gVisor not available (runsc not installed)")
	}

	safeID, err := sm.rt.SanitizeID(ext.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid extension ID for sandbox container: %w", err)
	}
	containerName := fmt.Sprintf("%s-%s", ExtensionContainerPrefix, safeID)

	// Check if container already exists
	exists, err := sm.rt.ContainerExists(containerName)
	if err != nil {
		return nil, fmt.Errorf("check container: %w", err)
	}
	if exists {
		logging.For(logging.Sandbox).Infof("ext=%s removing stale container %s", ext.ID, containerName)
		if rmErr := sm.rt.RemoveContainer(containerName); rmErr != nil {
			return nil, fmt.Errorf("remove stale container %s: %w", containerName, rmErr)
		}
	}

	// Resolve agent binary path
	agentPath, err := sm.rt.GetAgentBinaryPath()
	if err != nil {
		return nil, fmt.Errorf("sandbox-bridge binary not found: %w", err)
	}
	agentPath, err = filepath.Abs(agentPath)
	if err != nil {
		return nil, fmt.Errorf("resolve agent path: %w", err)
	}

	// Which image this container runs is a precondition, never a default (see extensionImageSource).
	image, dockerfilePath, err := sm.extensionImageSource(ext)
	if err != nil {
		return nil, err
	}

	if err := sm.ensureExtensionImage(ext, image, dockerfilePath); err != nil {
		return nil, err
	}
	if ext.ContainerConfig.Image == "" {
		sm.persistDerivedImage(ext, image)
	}

	// Per-extension on purpose: this is the one place a granted dependency directory becomes a root,
	// and it is a root for this container only.
	createArgs, mountedProjects := buildExtensionCreateArgs(ext, containerName, image, projectMounts, sm.MountPolicyForExtension(ext))

	// Create container
	out, err := sm.rt.PodmanExec(createArgs...)
	if err != nil {
		if imgErr := sandboxruntime.ImageAvailabilityError(image, out); imgErr != nil {
			return nil, fmt.Errorf("create container %s: %w", containerName, imgErr)
		}
		return nil, fmt.Errorf("create container %s: %w (output: %s)", containerName, err, string(out))
	}

	// Copy agent binary into container
	cpOut, cpErr := sm.rt.PodmanExec("cp", agentPath, containerName+":"+"/sandbox-bridge")
	if cpErr != nil {
		_ = sm.rt.RemoveContainer(containerName)
		return nil, fmt.Errorf("copy agent to container: %w (%s)", cpErr, cpOut)
	}

	// Start the container (needed for exec sessions)
	startOut, err := sm.rt.PodmanExec("--runtime-flag=overlay2=root:self", "--runtime-flag=ignore-cgroups", "start", containerName)
	if err != nil {
		_ = sm.rt.RemoveContainer(containerName)
		return nil, fmt.Errorf("start container: %w (%s)", err, startOut)
	}

	// Enforce the memory limit in the container's cgroup — runsc does not apply OCI resource
	// limits itself (see sandboxruntime.ApplyMemoryLimit). Fail open, and 0 needs no write.
	if memLimit := extensionMemoryLimit(ext); memLimit > 0 {
		if memErr := sm.rt.ApplyMemoryLimit(containerName, memLimit); memErr != nil {
			logging.For(logging.Sandbox).Warnf("ext=%s WARNING: memory limit %dMB not enforced (cgroup write failed): %v", ext.ID, memLimit, memErr)
		}
	}

	logging.For(logging.Sandbox).Infof("ext=%s created and started container %s (mounts: %d projects)", ext.ID, containerName, len(projectMounts))

	// Snapshot of the extension's custom mounts and own envs for compatibility checks.
	// The same derivation the create used, so a later compatibility check wants what this container
	// actually got — including the dependency directory, if it was bound.
	bind := dependencyBind(ext, sm.MountPolicyForExtension(ext))
	customMounts := customMountSnapshot(withDependencyMount(ext.SandboxConfig.Mounts, bind))

	// Store state
	sm.states[ext.ID] = &extensionSandboxState{
		containerName:   containerName,
		createdAt:       time.Now(),
		lastCheckedAt:   time.Now(),
		isAlive:         true,
		mountedProjects: mountedProjects,
		customMounts:    customMounts,
		envs:            envSnapshot(withDependencyEnv(ext.SandboxConfig.Envs, bind)),
	}

	return nil, nil
}

// StartSession opens a dedicated bridge connection into a running extension sandbox and takes a
// reference for the caller. The connection is the reference: ReleaseSession ends both together.
func (sm *ExtensionSandboxManager) StartSession(extID string) (*bridge.Client, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, ok := sm.states[extID]
	if !ok {
		return nil, fmt.Errorf("extension sandbox not created: %s", extID)
	}

	agentCmd, client, err := startBridgeLocked(state.containerName)
	if err != nil {
		return nil, err
	}
	if state.sessions == nil {
		state.sessions = make(map[*bridge.Client]*exec.Cmd)
	}
	state.sessions[client] = agentCmd
	sm.refs[extID]++
	sm.cancelStopLocked(extID, state, "session connected")

	logging.For(logging.Sandbox).Infof("ext=%s started a bridge session in container %s (%d live)", extID, state.containerName, len(state.sessions))
	return client, nil
}

// startBridgeLocked spawns `podman exec -i <container> /sandbox-bridge` and wraps its pipes in a
// bridge client, recording nothing: the caller owns the process and must reap it.
func startBridgeLocked(containerName string) (*exec.Cmd, *bridge.Client, error) {
	// PodmanCommand, not exec.Command. This process lives as long as the container, and used to
	// inherit the whole server environment — it carries no -e, so none of it reached the container,
	// but MIKE_LOGIN_PASSWORD and MIKE_ENCRYPTION_KEY sat readable in /proc/<pid>/environ for the
	// life of the sandbox — and it set no WaitDelay, so a container that stopped answering could
	// hold cmd.Wait open past shutdown.
	agentCmd := sandboxruntime.PodmanCommand(
		"--runtime-flag=overlay2=root:self", "--runtime-flag=ignore-cgroups",
		"exec", "-i", containerName,
		"/sandbox-bridge",
	)

	stdin, err := agentCmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := agentCmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	// The agent logs to stderr; keep it on the server's log.
	agentCmd.Stderr = os.Stderr

	if err := agentCmd.Start(); err != nil {
		stdin.Close()
		return nil, nil, fmt.Errorf("start agent: %w", err)
	}

	return agentCmd, bridge.NewClient(stdin, stdout), nil
}

// cancelStopLocked calls off a pending grace-period stop.
func (sm *ExtensionSandboxManager) cancelStopLocked(extID string, state *extensionSandboxState, reason string) {
	if state == nil || state.stopTimer == nil {
		return
	}
	state.stopTimer.Stop()
	state.stopTimer = nil
	logging.For(logging.Sandbox).Infof("ext=%s grace period cancelled (%s)", extID, reason)
}

// ReleaseSession closes one bridge connection and hands back the reference that came with it;
// with none left, a grace period starts and the sandbox stops if nothing reconnects.
//
// It reports whether the sandbox still knew this connection — a late hand-back must not decrement
// the count of a sandbox recreated behind the caller's back. The connection is closed either way.
func (sm *ExtensionSandboxManager) ReleaseSession(extID string, client *bridge.Client) bool {
	if client == nil {
		return false
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, ok := sm.states[extID]
	if !ok {
		_ = client.Close()
		return false
	}
	agentCmd, known := state.sessions[client]
	if !known {
		_ = client.Close()
		return false
	}
	delete(state.sessions, client)
	_ = client.Close()
	if agentCmd != nil && agentCmd.Process != nil {
		_ = agentCmd.Process.Kill()
		_ = agentCmd.Wait()
	}

	sm.refs[extID]--
	if sm.refs[extID] > 0 {
		return true
	}
	// A stop timer from an earlier release must not fire in the middle of this grace period.
	if state.stopTimer != nil {
		state.stopTimer.Stop()
		state.stopTimer = nil
	}
	state.stopTimer = time.AfterFunc(ExtensionStopGracePeriod, func() {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		// Only stop if refcount is still 0 (no reconnect during grace period)
		if sm.refs[extID] <= 0 {
			sm.stopLocked(extID)
		}
	})
	logging.For(logging.Sandbox).Infof("ext=%s entering %v grace period", extID, ExtensionStopGracePeriod)
	return true
}

// stopLocked stops an extension sandbox (must hold sm.mu).
func (sm *ExtensionSandboxManager) stopLocked(extID string) {
	state := sm.states[extID]
	if state == nil {
		return
	}

	// Cancel any pending stop timer
	if state.stopTimer != nil {
		state.stopTimer.Stop()
		state.stopTimer = nil
	}

	// End every bridge session. Closing the connection is what reaps the language server: the
	// agent kills its own session when the connection goes.
	for client, agentCmd := range state.sessions {
		_ = client.Close()
		if agentCmd != nil && agentCmd.Process != nil {
			_ = agentCmd.Process.Kill()
			_ = agentCmd.Wait()
		}
	}
	state.sessions = nil

	// Stop container
	_, _ = sm.rt.PodmanExec("stop", "-t", "5", state.containerName)

	delete(sm.refs, extID)
	delete(sm.states, extID)
	sm.mem.Forget(state.containerName)

	logging.For(logging.Sandbox).Infof("ext=%s stopped", extID)
}

// Stop stops an extension sandbox.
func (sm *ExtensionSandboxManager) Stop(extID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stopLocked(extID)
	return nil
}

// Destroy fully removes an extension sandbox (stop + remove container).
func (sm *ExtensionSandboxManager) Destroy(extID string) error {
	sm.mu.Lock()
	state := sm.states[extID]
	sm.mu.Unlock()

	if state != nil {
		// Stop
		sm.Stop(extID)

		// Remove container
		if err := sm.rt.RemoveContainer(state.containerName); err != nil {
			return fmt.Errorf("remove container: %w", err)
		}
	}

	// Remove image if it was built locally. A manager built without a store has no record
	// to look up; the container above is what Destroy owns either way.
	if sm.store == nil {
		return nil
	}
	if ext, err := sm.store.GetByID(extID); err == nil && ext.IsLocalInstalled() && ext.ImageRef != "" {
		if !strings.HasPrefix(ext.ImageRef, "-") {
			_, _ = sm.rt.PodmanExec("rmi", ext.ImageRef)
		}
	}

	logging.For(logging.Sandbox).Infof("ext=%s destroyed", extID)
	return nil
}

// Close stops all running extension sandboxes.
// Called during server shutdown to clean up all containers.
func (sm *ExtensionSandboxManager) Close() {
	sm.mu.Lock()
	extIDs := make([]string, 0, len(sm.states))
	for extID := range sm.states {
		extIDs = append(extIDs, extID)
	}
	sm.mu.Unlock()

	for _, extID := range extIDs {
		logging.For(logging.Sandbox).Infof("shutdown: stopping ext=%s", extID)
		sm.Stop(extID)
	}
}

// ApplyMemoryLimit applies the given memory limit (MB)
func (sm *ExtensionSandboxManager) ApplyMemoryLimit(extID string, limitMB int) error {
	sm.mu.Lock()
	state, ok := sm.states[extID]
	sm.mu.Unlock()

	if !ok {
		return nil
	}
	running, err := sm.rt.IsRunning(state.containerName)
	if err != nil || !running {
		return nil
	}

	if limitMB <= 0 {
		return sm.rt.ClearMemoryLimit(state.containerName)
	}
	return sm.rt.ApplyMemoryLimit(state.containerName, limitMB)
}

// GetMemoryStats reads the extension sandbox's cgroup v2 memory controller, returning zero stats
// (Running: false) when it was never created, isn't running, or can't be read.
//
// The resolved cgroup directory is cached per extension and invalidated on a failed read.
func (sm *ExtensionSandboxManager) GetMemoryStats(extID string) *types.SandboxMemoryStats {
	sm.mu.Lock()
	state, ok := sm.states[extID]
	var containerName string
	if ok {
		containerName = state.containerName
	}
	sm.mu.Unlock()

	if !ok {
		return &types.SandboxMemoryStats{}
	}

	usage := sm.mem.Usage(containerName)
	return &types.SandboxMemoryStats{
		Running:   usage.Known,
		CurrentMB: usage.Current,
		LimitMB:   usage.Limit,
		PeakMB:    usage.Peak,
		OOMKills:  usage.OOMKills,
	}
}

// IsRunning checks if an extension sandbox is running.
func (sm *ExtensionSandboxManager) IsRunning(extID string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state := sm.states[extID]
	if state == nil {
		return false
	}

	// Check cache
	if time.Since(state.lastCheckedAt) < ExtensionLivenessTTL {
		return state.isAlive
	}

	running, err := sm.rt.IsRunning(state.containerName)
	state.isAlive = running && err == nil
	state.lastCheckedAt = time.Now()
	return state.isAlive
}

// customMountSnapshot returns a sorted list of "host:container:mode" strings
// for the given mounts, suitable for equality comparison.
func customMountSnapshot(mounts []types.SandboxMount) []string {
	if len(mounts) == 0 {
		return nil
	}
	snap := make([]string, len(mounts))
	for i, m := range mounts {
		snap[i] = m.HostPath + ":" + m.ContainerPath + ":" + m.Mode
	}
	sort.Strings(snap)
	return snap
}

// envSnapshot returns a sorted "KEY=VALUE" list of the extension's own container
// environment, suitable for equality comparison.
func envSnapshot(envs []types.SandboxEnv) []string {
	if len(envs) == 0 {
		return nil
	}
	snap := make([]string, len(envs))
	for i, e := range envs {
		snap[i] = e.Key + "=" + e.Value
	}
	sort.Strings(snap)
	return snap
}

// equalStrings reports whether two sorted snapshots are identical.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MountsCompatible reports whether a live sandbox still matches what the caller would
// create, with a reason when it must be recreated: a different project set, mounts, or
// extension envs (a container's env is fixed at `podman create`).
//
// desiredEnvs is the extension's own env list (manifest + user config). Per-workspace
// assignments are not part of it: they belong to one connection, and letting them in
// would make one project's dependency root restart every other project's server.
func (sm *ExtensionSandboxManager) MountsCompatible(extID string, desired map[string]string, desiredCustom []types.SandboxMount, desiredEnvs []types.SandboxEnv) (bool, string) {
	sm.mu.Lock()
	state := sm.states[extID]
	sm.mu.Unlock()
	if state == nil {
		return false, "no sandbox"
	}
	// Compare sorted lists of project host paths
	desiredSorted := make([]string, 0, len(desired))
	for p := range desired {
		desiredSorted = append(desiredSorted, p)
	}
	sort.Strings(desiredSorted)
	if !equalStrings(desiredSorted, state.mountedProjects) {
		return false, "project mounts changed"
	}
	// Compare custom extension mounts (e.g. node_modules)
	if !equalStrings(customMountSnapshot(desiredCustom), state.customMounts) {
		return false, "mounts changed"
	}
	// Compare the extension's own container env
	if !equalStrings(envSnapshot(desiredEnvs), state.envs) {
		return false, "envs changed"
	}
	return true, ""
}

// ListRunning returns all running extension sandboxes with their status.
func (sm *ExtensionSandboxManager) ListRunning() map[string]ExtensionRuntimeStatus {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	result := make(map[string]ExtensionRuntimeStatus)
	for extID, state := range sm.states {
		running, _ := sm.rt.IsRunning(state.containerName)
		result[extID] = ExtensionRuntimeStatus{
			Running:   running,
			Container: state.containerName,
			Ports:     state.ports,
			RefCount:  sm.refs[extID],
			CreatedAt: state.createdAt,
		}
	}
	return result
}

// ExtensionRuntimeStatus describes the runtime state of an extension sandbox.
type ExtensionRuntimeStatus struct {
	Running   bool                 `json:"running"`
	Container string               `json:"container"`
	Ports     []types.AssignedPort `json:"ports,omitempty"`
	RefCount  int                  `json:"ref_count"`
	CreatedAt time.Time            `json:"created_at"`
}

// pullsMissingRemoteImage reports whether a missing extension image has to be pulled to exist. A
// "localhost/…" ref is a build made on this host: it is rebuilt from the extension's Dockerfile or
// reported as missing, never fetched — podman would look for a registry on this machine and fail
// after retries.
func pullsMissingRemoteImage(image string) bool {
	return image != "" && !strings.HasPrefix(image, sandboxruntime.LocalImageRegistry+"/")
}

// extensionImageSource answers two questions together because they must agree: which image an
// extension's sandbox runs, and what builds it when the host lacks it.
//
// There is no default image. The old one (ubuntu:latest for an empty record) failed in the worst
// way for a language server: the container started, reported running, and held no server. Now an
// extension that cannot name an image is an error and no container is created. A record with no
// image but a Dockerfile on disk is repaired here, so it heals on its next start.
func (sm *ExtensionSandboxManager) extensionImageSource(ext *Extension) (image, dockerfilePath string, err error) {
	dockerfilePath, dfErr := sm.findLocalDockerfile(ext.Name)
	if dfErr != nil {
		// An unreadable extensions dir does not disqualify an extension that declares its image; it
		// only means a missing local build cannot be rebuilt.
		logging.For(logging.Sandbox).Warnf("ext=%s Dockerfile lookup failed: %v", ext.ID, dfErr)
		dockerfilePath = ""
	}

	if declared := ext.ContainerConfig.Image; declared != "" {
		if err := sm.rt.ValidateImageRef(declared); err != nil {
			return "", "", fmt.Errorf("extension %s declares an unusable image: %w", ext.ID, err)
		}
		// Old records carry a short ref ("kanban-ext-gopls:1.0.0"); resolve it to the local build.
		return sm.rt.ResolveImageRef(declared), dockerfilePath, nil
	}
	if dockerfilePath == "" {
		return "", "", fmt.Errorf("extension %s (%s) has no container image and no Dockerfile to build one from, so its sandbox cannot start", ext.ID, ext.Name)
	}
	return ExtensionImageRef(ext.Name, ext.Version), dockerfilePath, nil
}

// persistDerivedImage saves an image the sandbox had to derive, so it is derived once. Best effort: the
// container is already built, so a failed write means the next start re-derives the same ref and builds
// nothing. The caller's copy is the LSP manager's own and is never written back wholesale.
func (sm *ExtensionSandboxManager) persistDerivedImage(ext *Extension, image string) {
	if sm.store == nil {
		logging.For(logging.Sandbox).Infof("ext=%s image %s derived at start, but with no store it will be derived again next time", ext.ID, image)
		return
	}
	cc := ext.ContainerConfig
	cc.Image = image
	if err := sm.store.SetImageRef(ext.ID, image, cc); err != nil {
		logging.For(logging.Sandbox).Warnf("ext=%s WARNING: derived image %s could not be recorded: %v", ext.ID, image, err)
		return
	}
	ext.ContainerConfig.Image = image
	ext.ImageRef = image
	logging.For(logging.Sandbox).Infof("ext=%s had no image on record; built %s and recorded it", ext.ID, image)
}

// ensureExtensionImage makes the image exist locally: rebuild it from the extension's Dockerfile, pull
// it if it is a remote ref, or fail. The create uses --pull never, so an image that is not here has to
// be settled now or it fails later with a worse message.
func (sm *ExtensionSandboxManager) ensureExtensionImage(ext *Extension, image, dockerfilePath string) error {
	if sm.registry.ImageExists(image) {
		return nil
	}
	if dockerfilePath != "" {
		data, err := os.ReadFile(dockerfilePath)
		if err != nil {
			return fmt.Errorf("read Dockerfile %s: %w", dockerfilePath, err)
		}
		logging.For(logging.Sandbox).Warnf("ext=%s image %s missing; rebuilding from %s", ext.ID, image, dockerfilePath)
		if err := sm.registry.BuildExtensionImage(string(data), image); err != nil {
			return fmt.Errorf("rebuild image %s from %s: %w", image, dockerfilePath, err)
		}
		return nil
	}
	// Nothing to rebuild from, so the image is remote, and with --pull never on the create this is the
	// only way it arrives.
	if pullsMissingRemoteImage(image) {
		logging.For(logging.Sandbox).Warnf("ext=%s image %s missing; pulling", ext.ID, image)
		if err := sm.registry.PullImage(image); err != nil {
			return fmt.Errorf("pull image %s: %w", image, err)
		}
		return nil
	}
	return fmt.Errorf("extension %s image %s is not on this host and there is no Dockerfile to build it from", ext.ID, image)
}

// findLocalDockerfile walks the extensions directory for a subdirectory named after the
// extension and returns its Dockerfile (extensions/<author>/<name>/Dockerfile), or "".
func (sm *ExtensionSandboxManager) findLocalDockerfile(name string) (string, error) {
	var found string
	err := filepath.WalkDir(sm.extensionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries (e.g. missing dir, permission errors).
			return nil
		}
		if d.IsDir() || d.Name() != "Dockerfile" {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == name {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return found, nil
}

// MarshalJSON implements json.Marshaler for ExtensionRuntimeStatus.
func (s ExtensionRuntimeStatus) MarshalJSON() ([]byte, error) {
	type Alias ExtensionRuntimeStatus
	return json.Marshal(&struct {
		CreatedAt string `json:"created_at"`
		*Alias
	}{
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		Alias:     (*Alias)(&s),
	})
}
