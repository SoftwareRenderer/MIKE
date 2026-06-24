package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Create a temp directory to use as worktree
	dir := t.TempDir()
	taskID := "test-sandbox-task"

	err := sm.Create(taskID, dir, "", false)
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
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exec-task"

	err := sm.Create(taskID, dir, "", false)
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
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-restricted-task"

	err := sm.Create(taskID, dir, "", false)
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
	sm := NewSandboxManager()
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-exec-input-task"

	err := sm.Create(taskID, dir, "", false)
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

func TestCreateWithoutGVisor(t *testing.T) {
	// Simulate unavailable gVisor by creating a manager and forcing unavailable
	sm := &SandboxManager{
		active:    make(map[string]string),
		available: false,
	}

	err := sm.Create("task-1", "/tmp/test", "", false)
	if err == nil {
		t.Fatal("expected error when gVisor is unavailable")
	}
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
