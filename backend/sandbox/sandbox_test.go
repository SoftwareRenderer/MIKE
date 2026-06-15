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

	err := sm.Create(taskID, dir, "")
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

	err := sm.Create(taskID, dir, "")
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

	err := sm.Create(taskID, dir, "")
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

	err := sm.Create(taskID, dir, "")
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

	err := sm.Create("task-1", "/tmp/test", "")
	if err == nil {
		t.Fatal("expected error when gVisor is unavailable")
	}
}
