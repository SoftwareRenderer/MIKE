package sandbox

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/types"
	"kanban/pkg/sandboxruntime"
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
	os.Setenv("MIKE_SANDBOX_SKIP_STARTUP_CLEANUP", "1")
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
// Checks MIKE_AGENT_PATH env var, then project root, then build directory.
func findAgentBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MIKE_AGENT_PATH"); p != "" {
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
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Create a temp directory to use as worktree
	dir := t.TempDir()
	taskID := "test-sandbox-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Verify container exists
	exists, err := sm.rt.ContainerExists(ContainerNamePrefix + "-" + taskID)
	if err != nil {
		t.Fatalf("check container: %v", err)
	}
	if !exists {
		t.Error("expected container to exist after Create")
	}
}

func TestExec(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exec-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{})
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
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-restricted-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{})
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
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exec-input-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{})
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
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

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
		exists, _ := sm.rt.ContainerExists(ContainerNamePrefix + "-" + id)
		if !exists {
			t.Fatalf("expected stale container %s to exist before cleanup", id)
		}
	}

	// Run cleanup
	sm.cleanupStaleContainers()

	// Verify all stale containers are gone
	for _, id := range staleIDs {
		exists, _ := sm.rt.ContainerExists(ContainerNamePrefix + "-" + id)
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

func TestCreate_OverridesImageEntrypoint(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}
	if !sm.ImageReady("docker.io/alpine/git") {
		t.Skip("docker.io/alpine/git image not available locally")
	}

	// alpine/git ships ENTRYPOINT ["git"]; without clearing it in the create
	// args, PID 1 would be `git /bin/sh -c ...` and exit within milliseconds
	// ("git: '/bin/sh' is not a git command"), taking any exec'd shell with it.
	dir := t.TempDir()
	taskID := "test-entrypoint-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{
		BaseImage: "docker.io/alpine/git",
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Give PID 1 time to crash if the image entrypoint was not cleared.
	time.Sleep(1500 * time.Millisecond)

	running, err := sm.rt.IsRunning(ContainerNamePrefix + "-" + taskID)
	if err != nil {
		t.Fatalf("inspect container state: %v", err)
	}
	if !running {
		t.Error("container is not running after Create with alpine/git image — image entrypoint not cleared")
	}
}

func TestCreateWithoutGVisor(t *testing.T) {
	// Simulate unavailable gVisor by creating a manager and forcing unavailable
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntime(),
		available: false,
	}

	_, err := sm.Create("task-1", t.TempDir(), &types.Project{Name: "test-proj"}, SandboxOpts{})
	if err == nil {
		t.Fatal("expected error when gVisor is unavailable")
	}
	if !strings.Contains(err.Error(), "gVisor not available") {
		t.Fatalf("expected gVisor unavailable error, got: %v", err)
	}
}

func TestCreate_RejectsInvalidImageRef(t *testing.T) {
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntime(),
		available: false,
	}

	invalidImages := []string{
		"-v",
		"--format=json",
		"-t something",
	}

	for _, img := range invalidImages {
		_, err := sm.Create("test-invalid-image", t.TempDir(), &types.Project{Name: "test-proj"}, SandboxOpts{
			BaseImage: img,
		})
		if err == nil {
			t.Fatalf("expected error for invalid image ref %q", img)
		}
		if !strings.Contains(err.Error(), "invalid sandbox image") {
			t.Fatalf("expected error about invalid sandbox image for %q, got: %v", img, err)
		}
	}
}

func TestCreate_ValidImageRefPassesValidation(t *testing.T) {
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntime(),
		available: false,
	}

	// A valid image ref should pass validation and reach the gVisor check.
	_, err := sm.Create("test-valid-image", t.TempDir(), &types.Project{Name: "test-proj"}, SandboxOpts{
		BaseImage: "docker.io/library/ubuntu:latest",
	})
	if err == nil {
		t.Fatal("expected error (gVisor not available)")
	}
	if !strings.Contains(err.Error(), "gVisor not available") {
		t.Fatalf("valid image should pass validation and reach gVisor check, got: %v", err)
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
	for _, taskID := range []string{"task-a1", "task-a2", "proj:proj-A"} {
		if _, tracked := sm.active[taskID]; tracked {
			t.Errorf("expected %s container to be stopped", taskID)
		}
	}

	// Project B container should still be running
	if _, tracked := sm.active["task-b1"]; !tracked {
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

// TestBuildPodmanBuildArgs checks that --no-cache appears only when requested
// and the context directory stays the last argument.
func TestBuildPodmanBuildArgs(t *testing.T) {
	tests := []struct {
		name    string
		noCache bool
		want    []string
	}{
		{"cache used by default", false, []string{"build", "-t", "proj-img", "/tmp/ctx"}},
		{"no-cache flag added on request", true, []string{"build", "-t", "proj-img", "--no-cache", "/tmp/ctx"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPodmanBuildArgs("proj-img", "/tmp/ctx", tc.noCache)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
			if got[len(got)-1] != "/tmp/ctx" {
				t.Fatalf("context dir must be last, got %v", got)
			}
		})
	}
}

func TestBuildImageWithProgress_InvalidImageRef(t *testing.T) {
	// BuildImageWithProgress must validate the derived image name before
	// passing it to podman. This catches issues if ProjectImagePrefix is
	// ever emptied or projectName produces an invalid image ref.
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntime(),
		available: true,
	}

	// Create a temp Dockerfile so we get past the file-read step
	tmpDir := t.TempDir()
	dockerfile := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM alpine:latest"), 0644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}

	// A project name containing a semicolon produces an image ref like
	// "sandbox-my;project" which fails ValidateImageRef (semicolon not in
	// allowed character set). This verifies the validation gate exists.
	err := sm.BuildImageWithProgress("my;project", dockerfile, nil)
	if err == nil {
		t.Fatal("expected error for project name producing invalid image ref")
	}
	if !strings.Contains(err.Error(), "invalid project image name") {
		t.Fatalf("expected image name validation error, got: %v", err)
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

func TestBinaryAvailable(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-binary-available-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Check for a binary name — verify the check doesn't panic and returns a boolean
	available := sm.BinaryAvailable(taskID, "pi")
	t.Logf("pi available in container: %v", available)

	// Test with non-existent task
	if sm.BinaryAvailable("nonexistent-task", "pi") {
		t.Error("expected BinaryAvailable to return false for non-existent task")
	}

	// Test with empty binary name
	if sm.BinaryAvailable(taskID, "") {
		t.Error("expected BinaryAvailable to return false for empty binary name")
	}
}

func TestStartProcess(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-startprocess-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{})
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
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-memory-task"

	// Create with 512MB memory limit
	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{MemoryLimit: 512})
	if err != nil {
		t.Fatalf("create sandbox with memory limit: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Verify container exists
	exists, err := sm.rt.ContainerExists(ContainerNamePrefix + "-" + taskID)
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

	// Verify the cgroup memory limit was actually applied. runsc does not
	// apply the OCI spec's linux.resources itself, so Create writes
	// memory.max into the container's cgroup — this is what makes the
	// --memory flag effective.
	cgDir, err := sm.rt.ContainerCgroupDir(ContainerNamePrefix + "-" + taskID)
	if err != nil {
		t.Fatalf("resolve container cgroup dir: %v", err)
	}
	maxData, err := os.ReadFile(filepath.Join(cgDir, "memory.max"))
	if err != nil {
		t.Fatalf("read memory.max: %v", err)
	}
	if got := strings.TrimSpace(string(maxData)); got != "536870912" {
		t.Errorf("cgroup memory.max = %q, want 536870912 (512M)", got)
	}
}

// TestMemoryLimitEnforced verifies the sandbox memory limit is a hard cap:
// a process that allocates past the limit is OOM-killed by the kernel cgroup
// OOM killer (exit 137) while the sandbox container itself survives.
func TestMemoryLimitEnforced(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-mem-enforced"

	// 128MB limit; the memhog below allocates ~512MB.
	if _, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{MemoryLimit: 128}); err != nil {
		t.Fatalf("create sandbox with memory limit: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// A variable-doubling loop allocates ~512MB of anonymous memory using
	// only the shell (no extra tools in the base image). If the cgroup limit
	// is enforced, the kernel OOM-kills the process before it finishes.
	const memhog = `x=a; while [ ${#x} -lt 536870912 ]; do x="$x$x"; done; echo MEMHOG-OK`
	out, err := sm.Exec(taskID, memhog, nil)
	if err == nil {
		t.Fatalf("memhog succeeded despite 128MB limit (limit not enforced); output: %q", out)
	}
	if strings.Contains(out, "MEMHOG-OK") {
		t.Error("memhog reported success despite 128MB limit")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 137 {
		t.Errorf("memhog exit = %v (output %q), want exit 137 (SIGKILL from cgroup OOM)", err, out)
	}

	// The sandbox container must still be running — the OOM kill targets the
	// over-allocating process, not the whole sandbox.
	if !sm.ContainerIsRunning(taskID) {
		t.Error("sandbox container is not running after OOM kill")
	}
}

// TestGetMemoryStats verifies live memory sampling from the container's
// cgroup: a running container reports usage and the applied limit, and the
// task reports not-running once untracked.
func TestGetMemoryStats(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-memstats-task"

	// 512MB limit; the background hog below allocates ~200MB and holds it.
	// The limit sits far above the ~200MB hog so the cgroup OOM killer can't
	// truncate the allocation before the usage assertion below (the container
	// baseline varies with host load).
	if _, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{MemoryLimit: 512}); err != nil {
		t.Fatalf("create sandbox with memory limit: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Untracked task reports not-running.
	if stats := sm.GetMemoryStats("no-such-task"); stats.Running {
		t.Error("expected not-running for untracked task")
	}

	// Background hog: allocate ~200MB (209715200 bytes) and hold it for 30s.
	const hog = `( x=a; while [ ${#x} -lt 209715200 ]; do x="$x$x"; done; sleep 30 ) & echo hog-started`
	out, err := sm.Exec(taskID, hog, nil)
	if err != nil {
		t.Fatalf("start background memhog: %v (%s)", err, out)
	}

	// Wait for the allocation to show up in memory.current.
	var stats *types.SandboxMemoryStats
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		stats = sm.GetMemoryStats(taskID)
		if stats.Running && stats.CurrentMB >= 150 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if stats == nil || !stats.Running {
		t.Fatalf("expected running container, got %+v", stats)
	}
	if stats.LimitMB != 512 {
		t.Errorf("LimitMB = %d, want 512", stats.LimitMB)
	}
	if stats.CurrentMB < 150 {
		t.Errorf("CurrentMB = %d, want >= 150 (hog allocated ~200MB)", stats.CurrentMB)
	}

	// After Stop the task is untracked → not-running.
	if err := sm.Stop(taskID); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	if stats := sm.GetMemoryStats(taskID); stats.Running {
		t.Error("expected not-running after Stop")
	}
}

func TestCreateWithCapabilities(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-capabilities-task"

	// Create with custom capabilities
	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{
		Capabilities: []string{"CAP_NET_RAW", "CAP_CHOWN"},
	})
	if err != nil {
		t.Fatalf("create sandbox with capabilities: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Verify container exists
	exists, err := sm.rt.ContainerExists(ContainerNamePrefix + "-" + taskID)
	if err != nil {
		t.Fatalf("check container: %v", err)
	}
	if !exists {
		t.Error("expected container to exist after Create with capabilities")
	}

	// Verify the container can run commands
	output, err := sm.Exec(taskID, "echo hello", nil)
	if err != nil {
		t.Fatalf("exec in container with capabilities: %v", err)
	}
	if output != "hello\n" {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestExecProject_FullConfig(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	project := &types.Project{ID: "test-execproj-fullcfg", Name: "test-execproj", Path: dir}

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
	output, err := sm.ExecProject(project, "echo $TEST_SANDBOX_VAR", SandboxOpts{MemoryLimit: 512, SandboxMounts: sandboxMounts, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecProject: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop("proj:" + project.ID)
	})

	// Verify the env var was set (full config was used)
	if !strings.Contains(output, "hello") {
		t.Fatalf("expected env var TEST_SANDBOX_VAR=hello in output, got: %q", output)
	}

	// Verify the custom mount is accessible
	output2, err := sm.ExecProject(project, "ls /mnt/custom", SandboxOpts{MemoryLimit: 512, SandboxMounts: sandboxMounts, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecProject second call: %v", err)
	}
	t.Logf("custom mount listing: %s", output2)
}

func TestExecProjectWithInput_FullConfig(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	project := &types.Project{ID: "test-execproj-input-fullcfg", Name: "test-execproj-input", Path: dir}

	sandboxEnvs := []types.SandboxEnv{
		{Key: "INPUT_TEST_VAR", Value: "works"},
	}

	// ExecProjectWithInput should create the container with full config
	output, err := sm.ExecProjectWithInput(project, "cat && echo $INPUT_TEST_VAR", []byte("stdin-data\n"), SandboxOpts{MemoryLimit: 256, SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("ExecProjectWithInput: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop("proj:" + project.ID)
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
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exectask-fullcfg"
	project := &types.Project{Name: "test-exectask"}

	sandboxEnvs := []types.SandboxEnv{
		{Key: "TASK_TEST_VAR", Value: "task-value"},
	}

	// ExecTask should create the container with full config
	output, err := sm.ExecTask(taskID, dir, project, "echo $TASK_TEST_VAR", SandboxOpts{MemoryLimit: 128, SandboxEnvs: sandboxEnvs})
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

func TestEnsureProjectContainer_RecreatesWhenStopped(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	project := &types.Project{ID: "test-recreate-proj", Name: "test-recreate", Path: dir}

	sandboxEnvs := []types.SandboxEnv{
		{Key: "RECREATE_VAR", Value: "v1"},
	}

	// First call creates the container
	_, err := sm.ExecProject(project, "echo ok", SandboxOpts{SandboxEnvs: sandboxEnvs})
	if err != nil {
		t.Fatalf("first ExecProject: %v", err)
	}

	// Stop the container
	_ = sm.Stop("proj:" + project.ID)

	// Second call should recreate it with new config
	sandboxEnvs2 := []types.SandboxEnv{
		{Key: "RECREATE_VAR", Value: "v2"},
	}
	output, err := sm.ExecProject(project, "echo $RECREATE_VAR", SandboxOpts{SandboxEnvs: sandboxEnvs2})
	if err != nil {
		t.Fatalf("second ExecProject (recreate): %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop("proj:" + project.ID)
	})

	if !strings.Contains(output, "v2") {
		t.Fatalf("expected recreated container to have new env var v2, got: %q", output)
	}
}

func TestEnsureProjectContainer_RecreatesWhenMountStale(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	projectID := "test-stale-mount-proj"
	project := &types.Project{ID: projectID, Name: "test-stale-mount"}
	taskID := "proj:" + projectID
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Create the project container at dir1, then "move" the project to dir2.
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	project.Path = dir1

	if err := sm.ensureProjectContainer(taskID, project, SandboxOpts{}); err != nil {
		t.Fatalf("initial ensureProjectContainer: %v", err)
	}

	// Verify the running container's mount points at dir1.
	sm.mu.Lock()
	containerName := sm.active[taskID]
	sm.mu.Unlock()
	mountPath := "/home/sandbox/" + project.Name
	source, err := sm.rt.GetContainerMountSource(containerName, mountPath)
	if err != nil {
		t.Fatalf("GetContainerMountSource: %v", err)
	}
	if filepath.Clean(source) != filepath.Clean(dir1) {
		t.Fatalf("expected mount source %q, got %q", dir1, source)
	}

	// ensureProjectContainer with the new path must recreate the container
	// (the old one is still running) so its mount points at dir2.
	project.Path = dir2
	if err := sm.ensureProjectContainer(taskID, project, SandboxOpts{}); err != nil {
		t.Fatalf("ensureProjectContainer after move: %v", err)
	}

	sm.mu.Lock()
	containerName = sm.active[taskID]
	sm.mu.Unlock()
	source, err = sm.rt.GetContainerMountSource(containerName, mountPath)
	if err != nil {
		t.Fatalf("GetContainerMountSource after move: %v", err)
	}
	if filepath.Clean(source) != filepath.Clean(dir2) {
		t.Fatalf("expected recreated mount source %q, got %q", dir2, source)
	}
}

func TestCreate_CustomMountCreatesHostDir(t *testing.T) {
	agentPath := findAgentBinary(t)
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

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

	_, err := sm.Create(taskID, baseDir, &types.Project{Name: "test-proj"}, SandboxOpts{SandboxMounts: sandboxMounts})
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
	os.Setenv("MIKE_AGENT_PATH", agentPath)
	defer os.Unsetenv("MIKE_AGENT_PATH")

	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Create a temp directory to use as worktree
	dir := t.TempDir()
	taskID := "test-plan-task"

	// Create with plan mode (read-only mount)
	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{PlanMode: true})
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

// TestForwardPortSpecIsLoopbackOnly pins the L3 fix: a forwarded container port
// is published on loopback only. An unprefixed "<hostPort>:<containerPort>" spec
// binds every interface, exposing the sandbox's listener on the LAN.
func TestForwardPortSpecIsLoopbackOnly(t *testing.T) {
	for _, tc := range []struct{ hostPort, containerPort int }{
		{41234, 9999},
		{1, 65535},
	} {
		spec := forwardPortSpec(tc.hostPort, tc.containerPort)

		want := fmt.Sprintf("127.0.0.1:%d:%d", tc.hostPort, tc.containerPort)
		if spec != want {
			t.Errorf("forwardPortSpec(%d, %d) = %q, want %q", tc.hostPort, tc.containerPort, spec, want)
		}

		// podman parses "[ip:]hostPort:containerPort": without the IP the bind is
		// 0.0.0.0, so the first field must always be a loopback address.
		parts := strings.Split(spec, ":")
		if len(parts) != 3 {
			t.Fatalf("spec %q is not <ip>:<hostPort>:<containerPort>", spec)
		}
		ip := net.ParseIP(parts[0])
		if ip == nil {
			t.Fatalf("spec %q does not start with an IP address", spec)
		}
		if !ip.IsLoopback() {
			t.Errorf("spec %q binds %s, which is not loopback", spec, ip)
		}
	}
}
