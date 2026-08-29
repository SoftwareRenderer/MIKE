package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kanban/backend/types"
	sandbox "kanban/pkg/bridge"
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
	clients       map[string]*sandbox.Client        // extID -> bridge client
	refs          map[string]int                    // extID -> active client count
	states        map[string]*extensionSandboxState // extID -> runtime state
	cgroupDirs    map[string]string                 // extID -> cached cgroup v2 memory dir
	registry      *RegistryClient
	store         *ExtensionStore
	available     bool
	extensionsDir string
	workspaceDir  string
}

// extensionSandboxState tracks the runtime state of an extension sandbox.
type extensionSandboxState struct {
	containerName string
	client        *sandbox.Client
	agentCmd      *exec.Cmd // podman exec process
	ports         []types.AssignedPort
	createdAt     time.Time
	lastCheckedAt time.Time
	isAlive       bool
	// stopTimer fires after ExtensionStopGracePeriod when refcount hits 0.
	// Cancelled if a new client connects before it fires.
	stopTimer *time.Timer
	// Project-based mount mapping for URI translation.
	// Each registered project is mounted at /mnt/<project-dirname>.
	// projectMounts maps host project path → container mount path.
	projectMounts map[string]string
	// mountedProjects is the sorted list of host paths that were mounted,
	// used to detect when the project set has changed.
	mountedProjects []string
	// customMounts is the sorted list of "host:container:mode" strings for the
	// extension's SandboxConfig.Mounts, used to detect when custom mounts
	// (e.g. a node_modules mount) have changed and the container needs recreation.
	customMounts []string
}

// NewExtensionSandboxManager creates a new ExtensionSandboxManager.
func NewExtensionSandboxManager(reg *RegistryClient, store *ExtensionStore) *ExtensionSandboxManager {
	rt := sandboxruntime.NewRuntime()
	sm := &ExtensionSandboxManager{
		rt:            rt,
		clients:       make(map[string]*sandbox.Client),
		refs:          make(map[string]int),
		states:        make(map[string]*extensionSandboxState),
		cgroupDirs:    make(map[string]string),
		registry:      reg,
		store:         store,
		available:     checkExtensionSandboxAvailable(),
		extensionsDir: "extensions",
	}
	if sm.available {
		log.Println("[ext-sandbox] gVisor available, extension sandboxing enabled")
	} else {
		log.Println("[ext-sandbox] gVisor not available, extension sandboxing disabled")
	}
	// Clean up stale containers from previous runs
	sm.cleanupStaleContainers()
	return sm
}

// checkExtensionSandboxAvailable checks if gVisor and podman are available.
func checkExtensionSandboxAvailable() bool {
	if _, err := exec.LookPath("runsc"); err != nil {
		return false
	}
	if _, err := exec.LookPath("podman"); err != nil {
		return false
	}
	return true
}

// cleanupStaleContainers removes any kanban-ext-* containers left behind.
// The list call is time-bounded: best-effort cleanup, and a wedged podman
// must not block server startup.
func (sm *ExtensionSandboxManager) cleanupStaleContainers() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := sm.rt.PodmanExecContext(ctx, "ps", "-a", "--filter", fmt.Sprintf("name=%s-", ExtensionContainerPrefix), "--format", "{{.Names}}")
	if err != nil {
		log.Printf("[ext-sandbox] startup: failed to list stale containers: %v", err)
		return
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		log.Printf("[ext-sandbox] startup: removing stale container %s", name)
		_ = sm.rt.RemoveContainer(name)
	}
}

// IsAvailable returns true if extension sandboxing is available.
func (sm *ExtensionSandboxManager) IsAvailable() bool {
	return sm.available
}

// SetExtensionsDir records the directory extension files are materialized in,
// so it can be part of the mount allowlist (and so findLocalDockerfile looks in
// the right place).
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

// MountPolicy returns the host-mount allowlist for extension sandboxes: the
// configured workspace dir, the registered project paths, the extensions dir,
// the system temp dir and the EnvMountRoots extras. Built on demand so project
// additions take effect without a restart. A nil manager yields an empty policy,
// which permits no custom mount at all (fail closed).
func (sm *ExtensionSandboxManager) MountPolicy() *MountPolicy {
	var (
		workspaceDir  string
		extensionsDir string
		projectPaths  []string
	)
	if sm != nil {
		workspaceDir = sm.workspaceDir
		extensionsDir = sm.extensionsDir
		var err error
		if projectPaths, err = sm.store.ListProjectPaths(); err != nil {
			log.Printf("[ext-sandbox] WARNING: cannot list project paths for the mount allowlist: %v", err)
			projectPaths = nil
		}
	}
	return newMountPolicy(mountRoots(extensionsDir, workspaceDir, projectPaths))
}

// extensionMemoryLimit resolves the memory limit (MB) for an extension's
// sandbox: the extension's configured value. 0 means no limit; negative
// values are clamped to 0.
func extensionMemoryLimit(ext *Extension) int {
	if ext.SandboxConfig.MemoryLimit < 0 {
		return 0
	}
	return ext.SandboxConfig.MemoryLimit
}

// buildExtensionCreateArgs assembles the `podman create` argument list for an
// extension sandbox container.
//
// Extension sandboxes always run with --network none.
//
// The mount and capability lists that reach podman are re-validated here — even
// though the write paths (PUT /api/extensions/{id}, install) already reject
// them — so a value persisted before validation existed, or arriving from a
// manifest, is skipped (and logged) instead of becoming a container argument.
// A nil policy permits no custom mount at all (fail closed).
//
// Returns the create args and the sorted list of mounted host project paths
// (used to detect mount-set changes).
func buildExtensionCreateArgs(ext *Extension, containerName, image string, projectMounts map[string]string, policy *MountPolicy) ([]string, []string) {
	// Container runs sleep infinity; sandbox-bridge is started via podman exec when needed.
	createArgs := []string{
		"--runtime-flag=overlay2=root:self",
		"--runtime-flag=ignore-cgroups",
		"create",
		"--name", containerName,
		"--runtime", "runsc",
		"--security-opt", "label=disable",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--entrypoint", "",
		"--network", "none",
	}

	// Add back explicitly requested capabilities — except the ones that reach
	// past the container, which never make it into the argument list.
	for _, cap := range ext.SandboxConfig.Capabilities {
		if err := ValidateCapability(cap); err != nil {
			log.Printf("[ext-sandbox] ext=%s SKIPPED capability %q: %v", ext.ID, cap, err)
			continue
		}
		createArgs = append(createArgs, "--cap-add", cap)
	}

	// Mount each registered project at /mnt/<project-dirname> read-only.
	// This allows the container to serve any workspace without recreation.
	mountedProjects := make([]string, 0, len(projectMounts))
	for hostPath, containerPath := range projectMounts {
		createArgs = append(createArgs,
			"-v", hostPath+":"+containerPath+":rprivate,ro",
		)
		mountedProjects = append(mountedProjects, hostPath)
	}
	// Sort for deterministic comparison
	sort.Strings(mountedProjects)

	// Environment variables
	createArgs = append(createArgs,
		"-e", "TERM=xterm-256color",
	)
	for _, env := range ext.SandboxConfig.Envs {
		createArgs = append(createArgs, "-e", fmt.Sprintf("%s=%s", env.Key, env.Value))
	}

	// Memory limit (0 = no limit — omit the flag so the container starts unlimited)
	if memLimit := extensionMemoryLimit(ext); memLimit > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dM", memLimit))
	}

	// Custom mounts, each re-checked against the mount allowlist: the container
	// path charset is what keeps the "<host>:<container>:rprivate,<mode>" spec
	// from being rewritten by the value itself, and the host path must resolve
	// inside one of the app's own roots.
	for _, m := range ext.SandboxConfig.Mounts {
		if err := ValidateContainerPath(m.ContainerPath); err != nil {
			log.Printf("[ext-sandbox] ext=%s SKIPPED mount (host %q): %v", ext.ID, m.HostPath, err)
			continue
		}
		resolved, err := policy.ValidateHostPath(m.HostPath)
		if err != nil {
			log.Printf("[ext-sandbox] ext=%s SKIPPED mount (container %q): %v", ext.ID, m.ContainerPath, err)
			continue
		}
		mode := "ro"
		if m.Mode == "rw" {
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

// Create creates and starts a sandbox container for an extension.
// The container runs sleep infinity; the sandbox-bridge is started via podman exec.
// projectMounts maps host project paths to container mount paths (/mnt/<name>).
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
		log.Printf("[ext-sandbox] ext=%s removing stale container %s", ext.ID, containerName)
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

	// Determine image
	image := ext.ContainerConfig.Image
	if image == "" {
		image = "docker.io/library/ubuntu:latest"
	}
	if err := sm.rt.ValidateImageRef(image); err != nil {
		return nil, fmt.Errorf("invalid extension image: %w", err)
	}

	if err := sm.ensureExtensionImage(ext, image); err != nil {
		return nil, err
	}

	createArgs, mountedProjects := buildExtensionCreateArgs(ext, containerName, image, projectMounts, sm.MountPolicy())

	// Create container
	out, err := exec.Command("podman", createArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("create container %s: %w (output: %s)", containerName, err, string(out))
	}

	// Copy agent binary into container
	cpOut, cpErr := exec.Command("podman", "cp", agentPath, containerName+":"+"/sandbox-bridge").CombinedOutput()
	if cpErr != nil {
		_, _ = exec.Command("podman", "rm", "-f", containerName).CombinedOutput()
		return nil, fmt.Errorf("copy agent to container: %w (%s)", cpErr, cpOut)
	}

	// Start the container (needed for exec sessions)
	startOut, err := exec.Command("podman", "--runtime-flag=overlay2=root:self", "--runtime-flag=ignore-cgroups", "start", containerName).CombinedOutput()
	if err != nil {
		_, _ = exec.Command("podman", "rm", "-f", containerName).CombinedOutput()
		return nil, fmt.Errorf("start container: %w (%s)", err, startOut)
	}

	// Enforce the memory limit in the container's cgroup (runsc does not
	// apply OCI resource limits itself — see sandboxruntime.ApplyMemoryLimit).
	// Fail open: warn and continue if it cannot be applied. 0 = no limit: the
	// container's cgroup is unlimited by default, so nothing to write.
	if memLimit := extensionMemoryLimit(ext); memLimit > 0 {
		if memErr := sm.rt.ApplyMemoryLimit(containerName, memLimit); memErr != nil {
			log.Printf("[ext-sandbox] ext=%s WARNING: memory limit %dMB not enforced (cgroup write failed): %v", ext.ID, memLimit, memErr)
		}
	}

	log.Printf("[ext-sandbox] ext=%s created and started container %s (mounts: %d projects)", ext.ID, containerName, len(projectMounts))

	// Snapshot of the extension's custom mounts for compatibility checks.
	customMounts := customMountSnapshot(ext.SandboxConfig.Mounts)

	// Store state
	sm.states[ext.ID] = &extensionSandboxState{
		containerName:   containerName,
		createdAt:       time.Now(),
		lastCheckedAt:   time.Now(),
		isAlive:         true,
		projectMounts:   projectMounts,
		mountedProjects: mountedProjects,
		customMounts:    customMounts,
	}

	return nil, nil
}

// Start starts the sandbox-bridge agent in the extension container via podman exec.
// The agent communicates over stdin/stdout.
func (sm *ExtensionSandboxManager) Start(extID string) (*sandbox.Client, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, ok := sm.states[extID]
	if !ok {
		return nil, fmt.Errorf("extension sandbox not created: %s", extID)
	}

	containerName := state.containerName

	// Start agent in container via podman exec (stdin/stdout protocol)
	agentCmd := exec.Command(
		"podman", "--runtime-flag=overlay2=root:self", "--runtime-flag=ignore-cgroups",
		"exec", "-i", containerName,
		"/sandbox-bridge",
	)

	// Create pipes for communication with the agent
	stdin, err := agentCmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdout, err := agentCmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	// Capture stderr for debugging
	agentCmd.Stderr = os.Stderr

	if err := agentCmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start agent: %w", err)
	}

	// Create bridge client using stdin/stdout pipes
	client := sandbox.NewClient(stdin, stdout)
	sm.clients[extID] = client
	state.client = client
	state.agentCmd = agentCmd
	sm.refs[extID] = 1
	// Cancel any pending stop timer — a client has connected
	if state.stopTimer != nil {
		state.stopTimer.Stop()
		state.stopTimer = nil
		log.Printf("[ext-sandbox] ext=%s grace period cancelled (client connected)", extID)
	}

	log.Printf("[ext-sandbox] ext=%s started agent in container %s", extID, containerName)
	return client, nil
}

// GetOrCreate starts the extension sandbox if not running, returns existing bridge client.
func (sm *ExtensionSandboxManager) GetOrCreate(extID string) (*sandbox.Client, error) {
	sm.mu.Lock()
	client := sm.clients[extID]
	if client != nil && client.IsAlive() {
		sm.refs[extID]++
		// Cancel any pending stop timer — a new client has connected
		state := sm.states[extID]
		if state != nil && state.stopTimer != nil {
			state.stopTimer.Stop()
			state.stopTimer = nil
			log.Printf("[ext-sandbox] ext=%s grace period cancelled (client reconnected)", extID)
		}
		sm.mu.Unlock()
		return client, nil
	}
	sm.mu.Unlock()

	// Not running — caller needs to create and start
	return nil, fmt.Errorf("extension sandbox not running: %s", extID)
}

// Release decrements the refcount for an extension sandbox.
// When refcount reaches zero, a grace period timer starts.
// If no new client connects within the grace period, the sandbox is stopped.
func (sm *ExtensionSandboxManager) Release(extID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.refs[extID]--
	if sm.refs[extID] <= 0 {
		// Cancel any existing timer (shouldn't happen, but be safe)
		state := sm.states[extID]
		if state != nil && state.stopTimer != nil {
			state.stopTimer.Stop()
		}
		// Schedule delayed stop
		state = sm.states[extID]
		if state != nil {
			state.stopTimer = time.AfterFunc(ExtensionStopGracePeriod, func() {
				sm.mu.Lock()
				defer sm.mu.Unlock()
				// Only stop if refcount is still 0 (no reconnect during grace period)
				if sm.refs[extID] <= 0 {
					sm.stopLocked(extID)
				}
			})
			log.Printf("[ext-sandbox] ext=%s entering %v grace period", extID, ExtensionStopGracePeriod)
		}
	}
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

	// Kill the agent process
	if state.agentCmd != nil && state.agentCmd.Process != nil {
		_ = state.agentCmd.Process.Kill()
		_ = state.agentCmd.Wait()
	}

	// Close client
	if state.client != nil {
		_ = state.client.Close()
	}

	// Stop container
	_, _ = exec.Command("podman", "stop", "-t", "5", state.containerName).CombinedOutput()

	delete(sm.clients, extID)
	delete(sm.refs, extID)
	delete(sm.states, extID)
	delete(sm.cgroupDirs, extID)

	log.Printf("[ext-sandbox] ext=%s stopped", extID)
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

	// Remove image if it was built locally
	if ext, err := sm.store.GetByID(extID); err == nil && ext.IsLocalInstalled() && ext.ImageRef != "" {
		if !strings.HasPrefix(ext.ImageRef, "-") {
			_, _ = sm.rt.PodmanExec("rmi", ext.ImageRef)
		}
	}

	log.Printf("[ext-sandbox] ext=%s destroyed", extID)
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
		log.Printf("[ext-sandbox] shutdown: stopping ext=%s", extID)
		sm.Stop(extID)
	}
}

// UpdateMemoryLimit persists a new memory limit (MB)
func (sm *ExtensionSandboxManager) UpdateMemoryLimit(extID string, limitMB int) error {
	if limitMB < 0 {
		limitMB = 0
	}

	ext, err := sm.store.GetByID(extID)
	if err != nil {
		return fmt.Errorf("load extension %s: %w", extID, err)
	}
	ext.SandboxConfig.MemoryLimit = limitMB
	// Keep config.memory_limit in sync with sandbox_config (the edit form's
	// save path keys off config).
	if ext.Config == nil {
		ext.Config = map[string]any{}
	}
	ext.Config["memory_limit"] = limitMB
	if err := sm.store.Update(extID, ext); err != nil {
		return fmt.Errorf("persist memory limit for extension %s: %w", extID, err)
	}

	if err := sm.ApplyMemoryLimit(extID, limitMB); err != nil {
		return fmt.Errorf("apply memory limit %dMB to running sandbox %s: %w", limitMB, extID, err)
	}
	return nil
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

// GetMemoryStats returns the live memory usage of the extension's sandbox
// container by reading its cgroup v2 memory controller. Returns zero stats
// (Running: false) when the sandbox was never created, is not running, or
// the cgroup cannot be read. The resolved cgroup directory is cached per
// extension and invalidated when a read fails (cgroup gone — container
// stopped or removed).
func (sm *ExtensionSandboxManager) GetMemoryStats(extID string) *types.SandboxMemoryStats {
	sm.mu.Lock()
	state, ok := sm.states[extID]
	dir := sm.cgroupDirs[extID]
	sm.mu.Unlock()

	if !ok {
		return &types.SandboxMemoryStats{}
	}

	if dir == "" {
		var err error
		dir, err = sm.rt.ContainerCgroupDir(state.containerName)
		if err != nil {
			return &types.SandboxMemoryStats{}
		}
		sm.mu.Lock()
		if sm.cgroupDirs[extID] == "" {
			sm.cgroupDirs[extID] = dir
		}
		sm.mu.Unlock()
	}

	stats, err := memoryStatsFromDir(dir)
	if err != nil {
		// Drop the cached dir so the next call re-resolves it.
		sm.mu.Lock()
		delete(sm.cgroupDirs, extID)
		sm.mu.Unlock()
		return &types.SandboxMemoryStats{}
	}
	return stats
}

// memoryStatsFromDir reads the cgroup v2 memory controller statistics from
// dir and converts them to MB-granularity SandboxMemoryStats. A missing or
// malformed memory.current is an error; the optional peak/max/events files
// are tolerated (see sandboxruntime.ReadCgroupMemory).
func memoryStatsFromDir(dir string) (*types.SandboxMemoryStats, error) {
	mem, err := sandboxruntime.ReadCgroupMemory(dir)
	if err != nil {
		return nil, err
	}
	return &types.SandboxMemoryStats{
		Running:   true,
		CurrentMB: mem.CurrentBytes >> 20,
		LimitMB:   mem.MaxBytes >> 20,
		PeakMB:    mem.PeakBytes >> 20,
		OOMKills:  mem.OOMKills,
	}, nil
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

// MountMapping returns the project mount mapping for an extension sandbox.
// Returns nil if the sandbox is not running.
func (sm *ExtensionSandboxManager) MountMapping(extID string) map[string]string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state := sm.states[extID]
	if state == nil {
		return nil
	}
	// Return a copy to avoid external mutation
	result := make(map[string]string, len(state.projectMounts))
	for k, v := range state.projectMounts {
		result[k] = v
	}
	return result
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

// MountsCompatible checks if the current sandbox mounts match the desired set.
// Returns true if the mounted projects and custom extension mounts match,
// false if the container needs recreation.
func (sm *ExtensionSandboxManager) MountsCompatible(extID string, desired map[string]string, desiredCustom []types.SandboxMount) bool {
	state := sm.states[extID]
	if state == nil {
		return false
	}
	// Compare sorted lists of project host paths
	desiredSorted := make([]string, 0, len(desired))
	for p := range desired {
		desiredSorted = append(desiredSorted, p)
	}
	sort.Strings(desiredSorted)
	if len(desiredSorted) != len(state.mountedProjects) {
		return false
	}
	for i := range desiredSorted {
		if desiredSorted[i] != state.mountedProjects[i] {
			return false
		}
	}
	// Compare custom extension mounts (e.g. node_modules)
	desiredCustomSnap := customMountSnapshot(desiredCustom)
	if len(desiredCustomSnap) != len(state.customMounts) {
		return false
	}
	for i := range desiredCustomSnap {
		if desiredCustomSnap[i] != state.customMounts[i] {
			return false
		}
	}
	return true
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

// ensureExtensionImage verifies the extension's container image exists locally,
// rebuilding it from the extension's Dockerfile if it has gone missing
func (sm *ExtensionSandboxManager) ensureExtensionImage(ext *Extension, image string) error {
	if sm.registry.ImageExists(image) {
		return nil
	}

	dfPath, err := sm.findLocalDockerfile(ext.Name)
	if err != nil {
		return fmt.Errorf("locate Dockerfile for extension %s: %w", ext.Name, err)
	}
	if dfPath == "" {
		// No local Dockerfile to rebuild from — leave it to podman to pull (e.g.
		// a fully-qualified registry image or the default base).
		return nil
	}

	data, err := os.ReadFile(dfPath)
	if err != nil {
		return fmt.Errorf("read Dockerfile %s: %w", dfPath, err)
	}
	log.Printf("[ext-sandbox] ext=%s image %s missing; rebuilding from %s", ext.ID, image, dfPath)
	if err := sm.registry.BuildImageFromReader(string(data), image); err != nil {
		return fmt.Errorf("rebuild image %s from %s: %w", image, dfPath, err)
	}
	return nil
}

// findLocalDockerfile locates the Dockerfile for a locally-installed extension by
// walking the extensions directory for a subdirectory whose basename matches the
// extension name (extensions/<author>/<name>/Dockerfile). Returns "" if none is
// found.
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

// InstallAndCreate installs an extension from a registry entry and creates its sandbox.
func (sm *ExtensionSandboxManager) InstallAndCreate(entry *RegistryEntry, ext *Extension, workspacePath string) ([]types.AssignedPort, error) {
	// Determine image reference
	imageRef := entry.Image
	if imageRef == "" {
		// Build from Dockerfile
		imageRef = fmt.Sprintf("kanban-ext-%s:%s", entry.Name, entry.Version)
	}

	// Pull or build image
	if entry.Image != "" {
		// Pull OCI image
		if !sm.registry.ImageExists(entry.Image) {
			if err := sm.registry.PullImage(entry.Image); err != nil {
				return nil, fmt.Errorf("pull image %s: %w", entry.Image, err)
			}
		}
	} else if entry.DockerfileURL != "" {
		// Build from Dockerfile
		dockerfile, err := sm.registry.FetchDockerfile(entry.DockerfileURL)
		if err != nil {
			return nil, fmt.Errorf("fetch dockerfile: %w", err)
		}
		if err := sm.registry.BuildImageFromReader(dockerfile, imageRef); err != nil {
			return nil, fmt.Errorf("build image: %w", err)
		}
	}

	// Update extension with image ref
	ext.ContainerConfig.Image = imageRef
	ext.ImageRef = imageRef

	// Create sandbox with workspace as a single project mount
	absWorkspace, _ := filepath.Abs(workspacePath)
	projectMounts := map[string]string{
		absWorkspace: "/mnt/" + filepath.Base(absWorkspace),
	}
	return sm.Create(ext, projectMounts)
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
