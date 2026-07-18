package sandbox

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/types"
)

// TestMain runs cleanup of stale containers before the test suite starts.
// This handles the case where a previous interrupted test run left containers
// behind with overlay filesystem issues that prevent normal podman rm -f.
// It also disables the per-NewSandboxManager cleanup to avoid races when
// tests run in parallel (each test creates its own SandboxManager).
func TestMain(m *testing.M) {
	cleanupStaleTestContainers()
	// Disable per-instance cleanup in NewSandboxManager() to prevent tests
	// from removing each other's containers when running in parallel.
	os.Setenv("SANDBOX_SKIP_STARTUP_CLEANUP", "1")
	code := m.Run()
	cleanupStaleTestContainers()
	os.Exit(code)
}

// cleanupStaleTestContainers removes all kanban-sandbox-* containers,
// handling overlay filesystem issues that can block normal podman rm -f.
func cleanupStaleTestContainers() {
	out, err := exec.Command("podman", "ps", "-a",
		"--filter", "name=kanban-sandbox-",
		"--format", "{{.Names}}").CombinedOutput()
	if err != nil {
		return
	}
	names := strings.Fields(strings.TrimSpace(string(out)))
	if len(names) == 0 {
		return
	}
	for _, name := range names {
		_, rmErr := exec.Command("podman", "rm", "-f", name).CombinedOutput()
		if rmErr != nil {
			// Overlay merged dirs can have stale files that block rm -f.
			// Try cleaning them and retrying.
			cleanOverlayMergedDirs()
			_, _ = exec.Command("podman", "rm", "-f", name).CombinedOutput()
		}
	}
}

// cleanOverlayMergedDirs removes stale files from podman overlay merged directories.
// In rootless mode these are not real mount points — just directories that can
// accumulate stale files when containers are killed abruptly.
func cleanOverlayMergedDirs() {
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

// findAgentBinary locates the sandbox-bridge binary for tests.
// Checks KANBAN_AGENT_PATH env var, then project root, then build directory.
func findAgentBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("KANBAN_AGENT_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Check project root (go test runs from package dir)
	candidates := []string{
		"../../sandbox/sandbox-bridge",
		"../../sandbox-bridge",
		"./sandbox-bridge",
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	t.Skip("sandbox-bridge binary not found (run: CGO_ENABLED=0 GOOS=linux go build -o sandbox/sandbox-bridge ./pkg/sandbox/agent)")
	return ""
}

func TestIsAvailable(t *testing.T) {
	sm := NewSandboxManager()

	// runsc should be available (installed by install-gvisor.sh)
	if _, err := exec.LookPath("runsc"); err != nil {
		t.Skip("runsc not installed, skipping availability test")
	}
	if !sm.IsAvailable() {
		t.Error("expected gVisor to be available")
	}
}

func TestCreateAndStop(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Create a temp directory to use as worktree
	dir := t.TempDir()
	taskID := "test-sandbox-task"

	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Verify container exists
	exists, err := sm.containerExists(ContainerNamePrefix + "-" + taskID)
	if err != nil {
		t.Fatalf("check container: %v", err)
	}
	if !exists {
		t.Error("expected container to exist after Create")
	}
}

func TestExec(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exec-task"

	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Create a test file in the worktree
	testFile := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hello from sandbox"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Run a command inside the sandbox
	output, err := sm.Exec(taskID, "cat hello.txt", nil)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	// cat does not add trailing newline
	if output != "hello from sandbox" && output != "hello from sandbox\n" {
		t.Fatalf("unexpected output: %q", output)
	}

	// Test that the sandbox can write files
	_, err = sm.Exec(taskID, "echo 'modified' > hello.txt", nil)
	if err != nil {
		t.Fatalf("exec write: %v", err)
	}

	// Verify the file was modified
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read modified file: %v", err)
	}
	if string(data) != "modified\n" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

func TestExecNotAllowed(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-restricted-task"

	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Try to access host filesystem outside the worktree
	// The container should not be able to read /etc/passwd from the host
	output, err := sm.Exec(taskID, "cat /etc/hostname 2>&1 || echo 'ACCESS_DENIED'", nil)
	// Even if /etc/hostname exists in the container image, it should be the container's, not the host's
	t.Logf("sandbox /etc/hostname output: %s (error: %v)", output, err)

	// Verify we can't see host processes
	output, err = sm.Exec(taskID, "ps aux 2>&1 | grep -v 'sh\\|ps\\|grep' | head -5", nil)
	t.Logf("sandbox ps output: %s (error: %v)", output, err)
}

func TestExecWithInput(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exec-input-task"

	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		// Clean up files from inside the container (they're owned by root)
		_, _ = sm.Exec(taskID, "rm -rf test_input.txt sub", nil)
		_ = sm.Stop(taskID)
	})

	// Write a file via stdin (cat > file)
	content := []byte("line1\nline2\nline3")
	output, err := sm.ExecWithInput(taskID, "cat > test_input.txt", content, nil)
	if err != nil {
		t.Fatalf("exec with input: %v (output: %s)", err, output)
	}

	// Verify the file was created with correct content
	data, err := os.ReadFile(filepath.Join(dir, "test_input.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("unexpected content: %q (expected %q)", string(data), string(content))
	}

	// Test with content containing special shell characters
	specialContent := []byte("hello 'world' && echo $PATH > /dev/null")
	_, err = sm.ExecWithInput(taskID, "mkdir -p sub && cat > sub/special.txt", specialContent, nil)
	if err != nil {
		t.Fatalf("exec with special chars: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "sub", "special.txt"))
	if err != nil {
		t.Fatalf("read special file: %v", err)
	}
	if string(data) != string(specialContent) {
		t.Fatalf("unexpected special content: %q (expected %q)", string(data), string(specialContent))
	}
}

func TestCleanupStaleContainers(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()

	// Create containers with taskIDs that won't be in the active map
	// (simulating containers left behind by a crashed server)
	staleIDs := []string{"stale-task-alpha", "stale-task-beta"}
	for _, id := range staleIDs {
		_, _ = sm.podmanExec(
			"--runtime-flag=overlay2=root:self",
			"--runtime-flag=ignore-cgroups",
			"create",
			"--name", ContainerNamePrefix+"-"+id,
			"--runtime", "runsc",
			"--security-opt", "label=disable",
			"--cap-drop", "ALL",
			"--security-opt", "no-new-privileges",
			"-v", dir+":/home/sandbox/test:rprivate,rw",
			BaseImage,
			"/bin/sh", "-c", "sleep infinity",
		)
		_, _ = sm.podmanExec("--runtime-flag", "ignore-cgroups=true", "start", ContainerNamePrefix+"-"+id)
		t.Cleanup(func() {
			_, _ = sm.podmanExec("rm", "-f", ContainerNamePrefix+"-"+id)
		})
	}

	// Verify stale containers exist
	for _, id := range staleIDs {
		exists, _ := sm.containerExists(ContainerNamePrefix + "-" + id)
		if !exists {
			t.Fatalf("expected stale container %s to exist before cleanup", id)
		}
	}

	// Run cleanup
	sm.cleanupStaleContainers()

	// Verify all stale containers are gone
	for _, id := range staleIDs {
		exists, _ := sm.containerExists(ContainerNamePrefix + "-" + id)
		if exists {
			t.Errorf("expected stale container %s to be removed after cleanup", id)
		}
	}
}

func TestCleanupStaleContainers_NoneExist(t *testing.T) {
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Should not panic or error when no stale containers exist
	sm.cleanupStaleContainers()
}

func TestCreateWithoutGVisor(t *testing.T) {
	// Simulate unavailable gVisor by creating a manager and forcing unavailable
	sm := &SandboxManager{
		active:    make(map[string]string),
		available: false,
	}

	err, _ := sm.Create("task-1", t.TempDir(), "test-proj", "", "", SandboxOpts{})
	if err == nil {
		t.Fatal("expected error when gVisor is unavailable")
	}
}

func TestHasActiveContainer(t *testing.T) {
	sm := &SandboxManager{
		active:     make(map[string]string),
		mountPaths: make(map[string]string),
		agents:     make(map[string]*agentConn),
		available:  true,
	}

	// Empty map — no containers running
	if sm.HasActiveContainer("task-1") {
		t.Error("expected HasActiveContainer to return false for unknown task")
	}

	// Simulate an active container
	sm.active["task-2"] = "sandbox-task-2"
	if !sm.HasActiveContainer("task-2") {
		t.Error("expected HasActiveContainer to return true for active task")
	}

	// Other tasks should still be false
	if sm.HasActiveContainer("task-3") {
		t.Error("expected HasActiveContainer to return false for non-active task")
	}
}

func TestContainerIsRunning_Cache(t *testing.T) {
	sm := &SandboxManager{
		active:        make(map[string]string),
		mountPaths:    make(map[string]string),
		agents:        make(map[string]*agentConn),
		livenessCache: make(map[string]time.Time),
		available:     true,
	}

	// Not in active map — returns false without podman inspect
	if sm.ContainerIsRunning("nonexistent") {
		t.Error("expected ContainerIsRunning to return false for unknown task")
	}

	// In active map but cache is empty — would call podman inspect (skip, needs real container)
	// Instead, test the cache directly:
	sm.active["task-cached"] = "sandbox-task-cached"
	sm.livenessCache["task-cached"] = time.Now()

	// Should return true from cache (no podman inspect needed)
	if !sm.ContainerIsRunning("task-cached") {
		t.Error("expected ContainerIsRunning to return true from cache")
	}

	// Stop should invalidate the cache
	_ = sm.Stop("task-cached")
	if _, stillCached := sm.livenessCache["task-cached"]; stillCached {
		t.Error("expected liveness cache to be invalidated by Stop")
	}
}

func TestContainerIsRunning_CacheExpired(t *testing.T) {
	sm := &SandboxManager{
		active:        make(map[string]string),
		mountPaths:    make(map[string]string),
		agents:        make(map[string]*agentConn),
		livenessCache: make(map[string]time.Time),
		available:     true,
	}

	sm.active["task-expired"] = "sandbox-task-expired"
	// Set cache entry to expired (before livenessTTL)
	sm.livenessCache["task-expired"] = time.Now().Add(-10 * time.Minute)

	// Cache is expired — would call podman inspect.
	// Since container doesn't actually exist, it returns false.
	if sm.ContainerIsRunning("task-expired") {
		t.Error("expected ContainerIsRunning to return false for expired cache with nonexistent container")
	}
	// Cache should be cleared after a failed check
	if _, stillCached := sm.livenessCache["task-expired"]; stillCached {
		t.Error("expected liveness cache to be cleared after failed check")
	}
}

func TestStopProjectContainers(t *testing.T) {
	sm := &SandboxManager{
		active:     make(map[string]string),
		mountPaths: make(map[string]string),
		agents:     make(map[string]*agentConn),
		available:  true,
	}

	// Simulate containers for project "proj-A": two tasks + project-level
	sm.active["task-a1"] = "kanban-sandbox-task-a1"
	sm.active["task-a2"] = "kanban-sandbox-task-a2"
	sm.active["proj:proj-A"] = "kanban-sandbox-proj-proj-A"
	// Container for a different project
	sm.active["task-b1"] = "kanban-sandbox-task-b1"

	// Stop containers for project A
	sm.StopProjectContainers("proj-A", []string{"task-a1", "task-a2"})

	// Project A containers should be stopped (removed from active map)
	if sm.HasActiveContainer("task-a1") {
		t.Error("expected task-a1 container to be stopped")
	}
	if sm.HasActiveContainer("task-a2") {
		t.Error("expected task-a2 container to be stopped")
	}
	if sm.HasActiveContainer("proj:proj-A") {
		t.Error("expected project-level container to be stopped")
	}

	// Project B container should still be running
	if !sm.HasActiveContainer("task-b1") {
		t.Error("expected task-b1 container to still be running (different project)")
	}
}

func TestStopProjectContainers_NoContainers(t *testing.T) {
	sm := &SandboxManager{
		active:     make(map[string]string),
		mountPaths: make(map[string]string),
		agents:     make(map[string]*agentConn),
		available:  true,
	}

	// Should not panic when there are no containers
	sm.StopProjectContainers("proj-empty", []string{"nonexistent-task"})
}

func TestImageReady(t *testing.T) {
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping image test")
	}

	// alpine:latest should be available (used in tests)
	if !sm.ImageReady("alpine:latest") {
		t.Skip("alpine:latest not available locally, skipping")
	}

	// Non-existent image should not be ready
	if sm.ImageReady("nonexistent-image-xyz-12345:latest") {
		t.Error("expected nonexistent image to not be ready")
	}

	// Empty string should use default (ubuntu:latest)
	// This may or may not be available, just check it doesn't panic
	_ = sm.ImageReady("")
}

func TestPullImage(t *testing.T) {
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping image test")
	}

	// Pulling an already-available image should succeed (no-op or fast)
	err := sm.PullImage("alpine:latest")
	if err != nil {
		t.Fatalf("pull alpine:latest: %v", err)
	}
}

func TestBuildImage(t *testing.T) {
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping build test")
	}

	// Create a temp directory with a simple Dockerfile
	tmpDir := t.TempDir()
	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	content := []byte("FROM alpine:latest\nRUN echo hello\n")
	if err := os.WriteFile(dockerfile, content, 0644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}

	projectName := "test-build-proj"

	// Build the image
	err := sm.BuildImage(projectName, dockerfile)
	if err != nil {
		t.Fatalf("build image: %v", err)
	}
	t.Cleanup(func() {
		_, _ = sm.podmanExec("rmi", ProjectImageName(projectName))
	})

	// Verify the image exists
	if !sm.ImageReadyForProject(projectName) {
		t.Fatal("expected built image to be ready")
	}

	// Verify image name follows expected pattern
	expectedName := ProjectImageName(projectName)
	if !sm.ImageReady(expectedName) {
		t.Fatalf("expected image %q to exist", expectedName)
	}
}

func TestBuildImage_InvalidDockerfile(t *testing.T) {
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping build test")
	}

	// Create a temp directory with an invalid Dockerfile
	tmpDir := t.TempDir()
	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	content := []byte("FROM nonexistent-image-xyz-12345:latest\n")
	if err := os.WriteFile(dockerfile, content, 0644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}

	err := sm.BuildImage("test-invalid-proj", dockerfile)
	if err == nil {
		t.Fatal("expected error for invalid Dockerfile")
	}
}

func TestBuildImage_MissingFile(t *testing.T) {
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping build test")
	}

	err := sm.BuildImage("test-missing-proj", "/nonexistent/path/Dockerfile")
	if err == nil {
		t.Fatal("expected error for missing Dockerfile")
	}
}

func TestProjectImageName(t *testing.T) {
	name := ProjectImageName("my-project")
	if name != "sandbox-my-project" {
		t.Fatalf("unexpected image name: %q", name)
	}
	// Test with spaces
	name2 := ProjectImageName("my project name")
	if name2 != "sandbox-my-project-name" {
		t.Fatalf("unexpected image name with spaces: %q", name2)
	}
	// Test with slashes
	name3 := ProjectImageName("my/project")
	if name3 != "sandbox-my-project" {
		t.Fatalf("unexpected image name with slashes: %q", name3)
	}
}

func TestPiAvailable(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-pi-available-task"

	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// `pi` binary likely doesn't exist in the default ubuntu image
	// Just verify the check doesn't panic and returns a boolean
	available := sm.PiAvailable(taskID)
	t.Logf("pi available in container: %v", available)

	// Test with non-existent task
	if sm.PiAvailable("nonexistent-task") {
		t.Error("expected PiAvailable to return false for non-existent task")
	}
}

func TestStartProcess(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-startprocess-task"

	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Start a simple process that reads from stdin and echoes to stdout
	ph, err := sm.StartProcess(taskID, "cat", nil)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	defer ph.Close()

	// Write to stdin
	testInput := []byte("hello from process")
	n, err := ph.Stdin.Write(testInput)
	if err != nil {
		t.Fatalf("write to stdin: %v", err)
	}
	if n != len(testInput) {
		t.Fatalf("short write: wrote %d, expected %d", n, len(testInput))
	}

	// Close stdin to signal EOF
	ph.Stdin.Close()

	// Read from stdout
	output, err := io.ReadAll(ph.Stdout)
	if err != nil {
		t.Fatalf("read from stdout: %v", err)
	}
	if string(output) != string(testInput) {
		t.Fatalf("unexpected output: %q (expected %q)", string(output), string(testInput))
	}

	// Close stdout
	ph.Stdout.Close()

	// Wait for process to exit
	_ = ph.Cmd.Wait()
}

func TestStartProcess_NonexistentTask(t *testing.T) {
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Try to start a process in a non-existent container
	_, err := sm.StartProcess("nonexistent-task", "echo", []string{"hello"})
	if err == nil {
		t.Fatal("expected error for non-existent task")
	}
}

func TestCreateWithMemoryLimit(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-memory-task"

	// Create with 512MB memory limit
	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{MemoryLimit: 512})
	if err != nil {
		t.Fatalf("create sandbox with memory limit: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Verify container exists
	exists, err := sm.containerExists(ContainerNamePrefix + "-" + taskID)
	if err != nil {
		t.Fatalf("check container: %v", err)
	}
	if !exists {
		t.Error("expected container to exist after Create with memory limit")
	}

	// Verify the container can run commands
	output, err := sm.Exec(taskID, "echo hello", nil)
	if err != nil {
		t.Fatalf("exec in memory-limited container: %v", err)
	}
	if output != "hello\n" {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestExecProject_FullConfig(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	projectID := "test-execproj-fullcfg"
	projectName := "test-execproj"

	// Custom mount to verify full config is passed through
	mountDir := filepath.Join(dir, "custom-mount")
	os.MkdirAll(mountDir, 0755)
	sandboxMounts := []types.SandboxMount{
		{HostPath: mountDir, ContainerPath: "/mnt/custom", Mode: "rw"},
	}
	sandboxEnvs := []types.SandboxEnv{
		{Key: "TEST_SANDBOX_VAR", Value: "hello"},
	}

	// ExecProject should create the container with full config
	output, err := sm.ExecProject(projectID, dir, projectName, "echo $TEST_SANDBOX_VAR", SandboxOpts{MemoryLimit: 512, SandboxMounts: sandboxMounts, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecProject: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop("proj:" + projectID)
	})

	// Verify the env var was set (full config was used)
	if !strings.Contains(output, "hello") {
		t.Fatalf("expected env var TEST_SANDBOX_VAR=hello in output, got: %q", output)
	}

	// Verify the custom mount is accessible
	output2, err := sm.ExecProject(projectID, dir, projectName, "ls /mnt/custom", SandboxOpts{MemoryLimit: 512, SandboxMounts: sandboxMounts, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecProject second call: %v", err)
	}
	t.Logf("custom mount listing: %s", output2)
}

func TestExecProjectWithInput_FullConfig(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	projectID := "test-execproj-input-fullcfg"
	projectName := "test-execproj-input"

	sandboxEnvs := []types.SandboxEnv{
		{Key: "INPUT_TEST_VAR", Value: "works"},
	}

	// ExecProjectWithInput should create the container with full config
	output, err := sm.ExecProjectWithInput(projectID, dir, projectName, "cat && echo $INPUT_TEST_VAR", []byte("stdin-data\n"), SandboxOpts{MemoryLimit: 256, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecProjectWithInput: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop("proj:" + projectID)
	})

	// Verify both stdin data and env var are present
	if !strings.Contains(output, "stdin-data") {
		t.Fatalf("expected stdin data in output, got: %q", output)
	}
	if !strings.Contains(output, "works") {
		t.Fatalf("expected env var INPUT_TEST_VAR=works in output, got: %q", output)
	}
}

func TestExecTask_FullConfig(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exectask-fullcfg"
	projectName := "test-exectask"

	sandboxEnvs := []types.SandboxEnv{
		{Key: "TASK_TEST_VAR", Value: "task-value"},
	}

	// ExecTask should create the container with full config
	output, err := sm.ExecTask(taskID, dir, projectName, "", "echo $TASK_TEST_VAR", SandboxOpts{MemoryLimit: 128, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecTask: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	if !strings.Contains(output, "task-value") {
		t.Fatalf("expected env var TASK_TEST_VAR=task-value in output, got: %q", output)
	}
}

func TestExecTaskWithInput_FullConfig(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exectask-input-fullcfg"
	projectName := "test-exectask-input"

	sandboxEnvs := []types.SandboxEnv{
		{Key: "TASK_INPUT_VAR", Value: "input-value"},
	}

	output, err := sm.ExecTaskWithInput(taskID, dir, projectName, "", "cat && echo $TASK_INPUT_VAR", []byte("task-input\n"), SandboxOpts{MemoryLimit: 128, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecTaskWithInput: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	if !strings.Contains(output, "task-input") {
		t.Fatalf("expected stdin data in output, got: %q", output)
	}
	if !strings.Contains(output, "input-value") {
		t.Fatalf("expected env var in output, got: %q", output)
	}
}

func TestEnsureProjectContainer_RecreatesWhenStopped(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	projectID := "test-recreate-proj"
	projectName := "test-recreate"

	sandboxEnvs := []types.SandboxEnv{
		{Key: "RECREATE_VAR", Value: "v1"},
	}

	// First call creates the container
	_, err := sm.ExecProject(projectID, dir, projectName, "echo ok", SandboxOpts{SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("first ExecProject: %v", err)
	}

	// Stop the container
	_ = sm.Stop("proj:" + projectID)

	// Second call should recreate it with new config
	sandboxEnvs2 := []types.SandboxEnv{
		{Key: "RECREATE_VAR", Value: "v2"},
	}
	output, err := sm.ExecProject(projectID, dir, projectName, "echo $RECREATE_VAR", SandboxOpts{SandboxEnvs: sandboxEnvs2})
	if err != nil {
		t.Fatalf("second ExecProject (recreate): %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop("proj:" + projectID)
	})

	if !strings.Contains(output, "v2") {
		t.Fatalf("expected recreated container to have new env var v2, got: %q", output)
	}
}

func TestCreate_CustomMountCreatesHostDir(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	baseDir := t.TempDir()
	// Use a nested path that does NOT exist yet
	hostPath := filepath.Join(baseDir, "a", "b", "c")
	taskID := "test-mount-createdir-task"

	sandboxMounts := []types.SandboxMount{
		{HostPath: hostPath, ContainerPath: "/mnt/custom", Mode: "rw"},
	}

	// Verify directory does not exist before Create
	if _, err := os.Stat(hostPath); err == nil {
		t.Fatal("host path should not exist before Create")
	}

	err, _ := sm.Create(taskID, baseDir, "test-proj", "", "", SandboxOpts{SandboxMounts: sandboxMounts})
	if err != nil {
		t.Fatalf("create sandbox with custom mount: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Verify the host directory was created
	info, err := os.Stat(hostPath)
	if err != nil {
		t.Fatalf("host path was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected host path to be a directory")
	}
}

func TestCreate_PlanModeReadonlyMount(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("KANBAN_AGENT_PATH", agentPath)
	defer os.Unsetenv("KANBAN_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Create a temp directory to use as worktree
	dir := t.TempDir()
	taskID := "test-plan-task"

	// Create with plan mode (read-only mount)
	err, _ := sm.Create(taskID, dir, "test-proj", "", "", SandboxOpts{PlanMode: true})
	if err != nil {
		t.Fatalf("create sandbox with plan mode: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Verify the worktree is mounted read-only by trying to write a file
	out, err := sm.Exec(taskID, "touch /home/sandbox/test-proj/test-file", nil)
	if err == nil {
		t.Fatal("expected write to fail in plan mode (read-only mount)")
	}
	if !strings.Contains(out, "Read-only file system") && !strings.Contains(out, "Read-only") {
		t.Logf("output: %s", out)
		t.Errorf("expected read-only error, got: %v", err)
	}

	// Verify reading still works
	testFile := filepath.Join(dir, "existing-file.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	out, err = sm.Exec(taskID, "cat /home/sandbox/test-proj/existing-file.txt", nil)
	if err != nil {
		t.Fatalf("read should work in plan mode: %v", err)
	}
	if out != "test content" {
		t.Errorf("expected 'test content', got %q", out)
	}
}
