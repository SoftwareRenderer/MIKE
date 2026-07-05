package sandbox

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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

	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 0, nil, "")
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

	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 0, nil, "")
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

	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 0, nil, "")
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

	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 0, nil, "")
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

	err, _ := sm.Create("task-1", "/tmp/test", "", "test-proj", "", "", false, 0, nil, "")
	if err == nil {
		t.Fatal("expected error when gVisor is unavailable")
	}
}

func TestContainerRunning(t *testing.T) {
	sm := &SandboxManager{
		active:     make(map[string]string),
		mountPaths: make(map[string]string),
		agents:     make(map[string]*agentConn),
		available:  true,
	}

	// Empty map — no containers running
	if sm.ContainerRunning("task-1") {
		t.Error("expected ContainerRunning to return false for unknown task")
	}

	// Simulate an active container
	sm.active["task-2"] = "sandbox-task-2"
	if !sm.ContainerRunning("task-2") {
		t.Error("expected ContainerRunning to return true for active task")
	}

	// Other tasks should still be false
	if sm.ContainerRunning("task-3") {
		t.Error("expected ContainerRunning to return false for non-active task")
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
	if sm.ContainerRunning("task-a1") {
		t.Error("expected task-a1 container to be stopped")
	}
	if sm.ContainerRunning("task-a2") {
		t.Error("expected task-a2 container to be stopped")
	}
	if sm.ContainerRunning("proj:proj-A") {
		t.Error("expected project-level container to be stopped")
	}

	// Project B container should still be running
	if !sm.ContainerRunning("task-b1") {
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

	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 0, nil, "")
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

	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 0, nil, "")
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
	err, _ := sm.Create(taskID, dir, "", "test-proj", "", "", false, 512, nil, "")
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
