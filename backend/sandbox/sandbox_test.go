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

	"mike/backend/types"
	"mike/pkg/sandboxruntime"
)

// TestMain sweeps stale containers before and after the suite: an interrupted run can leave containers
// whose overlay storage blocks a normal `podman rm -f`, and the next run would inherit them.
//
// The sweep is this package's own, made explicitly. No manager started here sweeps: every
// NewSandboxManager call gets the zero Boot, which declines the host sweep, and that is what keeps
// parallel tests from deleting each other's containers. Under the old MIKE_SANDBOX_SKIP_STARTUP_CLEANUP
// switch the same safety depended on this file remembering to set a variable.
func TestMain(m *testing.M) {
	cleanupStaleTestContainers()
	code := m.Run()
	cleanupStaleTestContainers()
	os.Exit(code)
}

// cleanupStaleTestContainers removes every kanban-sandbox-* container through the production path,
// so a test run cannot reach a merged directory its own containers do not own.
func cleanupStaleTestContainers() {
	rt := sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{})
	out, err := rt.PodmanExec("ps", "-a",
		"--filter", "name=kanban-sandbox-",
		"--format", "{{.Names}}")
	if err != nil {
		return
	}
	for _, name := range strings.Fields(strings.TrimSpace(string(out))) {
		_ = rt.RemoveContainer(name)
	}
}

// findAgentBinary locates the sandbox-bridge binary for tests.
// bootWithBridge is the boot a sandbox test runs under: the bridge override pointed at the committed
// binary, and no claim on the host's container namespace (a unit test must never sweep the host it is
// running on). The override is a Boot field rather than MIKE_AGENT_PATH, so it cannot change underneath
// a manager that was already built.
func bootWithBridge(t *testing.T) sandboxruntime.Boot {
	t.Helper()
	return sandboxruntime.Boot{AgentPath: findAgentBinary(t)}
}

// findAgentBinary locates the committed bridge beside the source tree. It reads no environment: the
// binary a test exercises is the one in this checkout, so a MIKE_AGENT_PATH exported in the shell cannot
// point an integration test at some other build (production takes its override through startup.Config).
func findAgentBinary(t *testing.T) string {
	t.Helper()
	// go test runs from the package directory, so the first candidate is the project root.
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
	sm := NewSandboxManager(bootWithBridge(t))

	// runsc should be available (installed by install-gvisor.sh)
	if _, err := exec.LookPath("runsc"); err != nil {
		t.Skip("runsc not installed, skipping availability test")
	}
	if !sm.IsAvailable() {
		t.Error("expected gVisor to be available")
	}
}

func TestCreateAndStop(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()

	// The created-only container is what a crashed server leaves behind: a name nobody owns. The
	// running one is a live sandbox, and the sweep must walk past it.
	leftover := ContainerNamePrefix + "-stale-task-alpha"
	running := ContainerNamePrefix + "-stale-task-beta"
	for _, name := range []string{leftover, running} {
		_, _ = sm.podmanExec(
			"--runtime-flag=overlay2=root:self",
			"--runtime-flag=ignore-cgroups",
			"create",
			"--name", name,
			"--runtime", "runsc",
			"--security-opt", "label=disable",
			"--cap-drop", "ALL",
			"--security-opt", "no-new-privileges",
			"-v", dir+":/home/sandbox/test:rprivate,rw",
			BaseImage,
			"/bin/sh", "-c", "sleep infinity",
		)
		t.Cleanup(func() {
			_, _ = sm.podmanExec("rm", "-f", name)
		})
	}
	if _, err := sm.podmanExec("--runtime-flag", "ignore-cgroups=true", "start", running); err != nil {
		t.Skipf("cannot start a container for the sweep to leave alone: %v", err)
	}

	// Verify both containers exist
	for _, name := range []string{leftover, running} {
		exists, _ := sm.rt.ContainerExists(name)
		if !exists {
			t.Fatalf("expected container %s to exist before cleanup", name)
		}
	}

	// Run cleanup
	sm.cleanupStaleContainers()

	// The leftover is gone; the live one is not.
	if exists, _ := sm.rt.ContainerExists(leftover); exists {
		t.Errorf("expected leftover container %s to be removed after cleanup", leftover)
	}
	if exists, _ := sm.rt.ContainerExists(running); !exists {
		t.Errorf("running container %s was swept: a running sandbox belongs to a live owner, and removing it takes a working container away from them", running)
	}
}

func TestCleanupStaleContainers_NoneExist(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Should not panic or error when no stale containers exist
	sm.cleanupStaleContainers()
}

func TestCreate_OverridesImageEntrypoint(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
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
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
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
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
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
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
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

// A missing sandbox image used to reach the user as "exit status 125 (short-name resolution
// enforced but cannot prompt without a TTY)". With qualified refs and --pull=never it must be
// named plainly, and fast — no retries against a guessed registry.
// TestCreateContainerArgs pins the argument order: podman-global flags before `create`, `--pull
// never` after it. Container creation must not fetch an image; the caller does that, visibly.
func TestCreateContainerArgs(t *testing.T) {
	args := createContainerArgs("kanban-sandbox-task-1", "/home/sandbox/proj")

	indexOf := func(want string) int {
		for i, a := range args {
			if a == want {
				return i
			}
		}
		return -1
	}
	hasPair := func(flag, value string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == flag && args[i+1] == value {
				return true
			}
		}
		return false
	}

	if !hasPair("--pull", "never") {
		t.Fatalf("args missing --pull never: %v", args)
	}
	createAt, pullAt := indexOf("create"), indexOf("--pull")
	if pullAt < createAt {
		t.Errorf("--pull at %d precedes `create` at %d — podman reads it as a global flag: %v", pullAt, createAt, args)
	}
	if indexOf("--runtime-flag=overlay2=root:self") > createAt || indexOf("--runtime-flag=ignore-cgroups") > createAt {
		t.Errorf("podman-global --runtime-flag must come before `create`: %v", args)
	}
	for _, pair := range [][2]string{{"--runtime", "runsc"}, {"--security-opt", "label=disable"},
		{"--cap-drop", "ALL"}, {"--security-opt", "no-new-privileges"}, {"--workdir", "/home/sandbox/proj"},
		{"--name", "kanban-sandbox-task-1"}} {
		if !hasPair(pair[0], pair[1]) {
			t.Errorf("args missing %s %s: %v", pair[0], pair[1], args)
		}
	}
}

func TestCreate_MissingImageIsActionable(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	taskID := "test-missing-image-task"
	missing := "localhost/kanban-does-not-exist-image:1"
	if sm.ImageReady(missing) {
		t.Skipf("%s unexpectedly exists locally", missing)
	}

	started := time.Now()
	_, err := sm.Create(taskID, t.TempDir(), &types.Project{Name: "test-proj"}, SandboxOpts{BaseImage: missing})
	elapsed := time.Since(started)
	if err == nil {
		_ = sm.Stop(taskID)
		t.Fatal("expected an error creating a container from a missing image")
	}
	t.Cleanup(func() { _ = sm.Stop(taskID) })

	if msg := err.Error(); !strings.Contains(msg, "is not available locally") {
		t.Fatalf("error must name the condition and say what to do, got: %s", msg)
	}
	if msg := err.Error(); !strings.Contains(msg, missing) {
		t.Fatalf("error must name the image %q, got: %s", missing, msg)
	}
	for _, masked := range []string{"short-name resolution", "cannot prompt", "exit status 125"} {
		if strings.Contains(err.Error(), masked) {
			t.Errorf("error still leaks podman's masked wording (%q): %s", masked, err)
		}
	}
	// --pull=never: a missing image fails at once instead of trying registries.
	if elapsed > 20*time.Second {
		t.Errorf("missing image took %s to fail — that looks like registry retries", elapsed)
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
	sm := NewSandboxManager(bootWithBridge(t))
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
	sm := NewSandboxManager(bootWithBridge(t))
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
	err := sm.BuildImageWithProgress(projectName, dockerfile, nil)
	if err != nil {
		t.Fatalf("build image: %v", err)
	}
	t.Cleanup(func() {
		_, _ = sm.podmanExec("rmi", ProjectImageName(projectName))
	})

	expectedName := ProjectImageName(projectName)
	if !sm.ImageReady(expectedName) {
		t.Fatalf("expected image %q to exist", expectedName)
	}
}

func TestBuildImage_InvalidDockerfile(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))
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

	err := sm.BuildImageWithProgress("test-invalid-proj", dockerfile, nil)
	if err == nil {
		t.Fatal("expected error for invalid Dockerfile")
	}
}

func TestBuildImage_MissingFile(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping build test")
	}

	err := sm.BuildImageWithProgress("test-missing-proj", "/nonexistent/path/Dockerfile", nil)
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
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
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
	if name != "localhost/sandbox-my-project" {
		t.Fatalf("unexpected image name: %q", name)
	}
	// Test with spaces
	name2 := ProjectImageName("my project name")
	if name2 != "localhost/sandbox-my-project-name" {
		t.Fatalf("unexpected image name with spaces: %q", name2)
	}
	// Test with slashes
	name3 := ProjectImageName("my/project")
	if name3 != "localhost/sandbox-my-project" {
		t.Fatalf("unexpected image name with slashes: %q", name3)
	}
	// A dotted project name still has to produce a qualified ref: "sandbox-getmike.dev" looks
	// like a host but podman treats it as a short name.
	name4 := ProjectImageName("getmike.dev")
	if name4 != "localhost/sandbox-getmike.dev" {
		t.Fatalf("unexpected image name for a dotted project: %q", name4)
	}
	if !sandboxruntime.IsQualifiedImageRef(name4) {
		t.Fatalf("project image name %q is not fully qualified", name4)
	}
}

func TestBinaryAvailable(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	ph.Stdout.Close()

	// Wait for process to exit
	_ = ph.Cmd.Wait()
}

func TestStartProcess_NonexistentTask(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-mem-enforced"

	// The smallest cap that kills the process and not the sandbox. The sandbox holds ~23MB idle, and at
	// a 64MB cap the cgroup was measured killing runsc itself instead of the over-allocating guest
	// process (exit 128, `WaitPID failed: EOF`, container gone, 3 runs out of 3). The memhog's 512MB
	// target is never reached: the limit stops it on the way.
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

	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	dir := t.TempDir()
	taskID := "test-memstats-task"

	// A cap above the 64MB the test holds plus the sandbox's own ~23MB, so the allocation is never
	// truncated before the reading below. It is also what the memory budget charges this container, so
	// a wider cap would only cost more. TestMemoryLimitEnforced is the one that asserts the kill.
	if _, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{MemoryLimit: 192}); err != nil {
		t.Fatalf("create sandbox with memory limit: %v", err)
	}
	t.Cleanup(func() {
		_ = sm.Stop(taskID)
	})

	// Untracked task reports not-running.
	if stats := sm.GetMemoryStats("no-such-task"); stats.Running {
		t.Error("expected not-running for untracked task")
	}

	// Background hog: hold a 64MB string for 30s, built by one command substitution so that it does
	// not grow past what it asks for the way a doubling loop does.
	const hog = `( x=$(head -c 67108864 /dev/zero | tr "\0" a); sleep 30 ) & echo hog-started`
	out, err := sm.Exec(taskID, hog, nil)
	if err != nil {
		t.Fatalf("start background memhog: %v (%s)", err, out)
	}

	// Wait for the allocation to show up in memory.current.
	var stats *types.SandboxMemoryStats
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		stats = sm.GetMemoryStats(taskID)
		if stats.Running && stats.CurrentMB >= 60 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if stats == nil || !stats.Running {
		t.Fatalf("expected running container, got %+v", stats)
	}
	if stats.LimitMB != 192 {
		t.Errorf("LimitMB = %d, want the 192 the container was created with", stats.LimitMB)
	}
	if stats.CurrentMB < 60 {
		t.Errorf("CurrentMB = %d, want >= 60 (the hog holds a 64MB string)", stats.CurrentMB)
	}

	// After Stop the task is untracked → not-running, and the peak has been handed off with the
	// cgroup it was read from.
	var reported []int64
	sm.SetMemoryPeakSink(func(_ string, peakMB int64, _ int64) { reported = append(reported, peakMB) })
	if err := sm.Stop(taskID); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	if stats := sm.GetMemoryStats(taskID); stats.Running {
		t.Error("expected not-running after Stop")
	}
	if len(reported) != 1 || reported[0] < 60 {
		t.Errorf("peak reported at teardown = %v, want one reading of >= 60 MB — without it the sandbox panel has no memory number once the task leaves In Progress", reported)
	}
}

func TestCreateWithCapabilities(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
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

	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	// Create a temp directory to use as worktree
	dir := t.TempDir()
	taskID := "test-plan-task"

	// Create with plan mode (read-only mount)
	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{WorktreeReadOnly: true})
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

// The bind address reaches podman as given, since podman derives the forward target from
// it. Every-interface is spelled with no address at all, and a loopback address maps a port
// a gVisor guest cannot answer - both are the user's choice, not ours to rewrite.
func TestForwardPortSpec(t *testing.T) {
	tests := []struct {
		name          string
		hostIP        string
		hostPort      int
		containerPort int
		want          string
	}{
		{"no address", "", 41234, 3000, "41234:3000"},
		{"LAN address", "192.168.1.100", 41234, 80, "192.168.1.100:41234:80"},
		{"loopback passed through", "127.0.0.1", 41234, 80, "127.0.0.1:41234:80"},
		{"IPv6 is bracketed", "fd00::5", 41234, 80, "[fd00::5]:41234:80"},
		{"port range edges", "", 1, 65535, "1:65535"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forwardPortSpec(tt.hostIP, tt.hostPort, tt.containerPort)
			if got != tt.want {
				t.Errorf("forwardPortSpec(%q, %d, %d) = %q, want %q", tt.hostIP, tt.hostPort, tt.containerPort, got, tt.want)
			}
		})
	}
}

// firstGlobalIPv4 returns an address of this machine to bind a forwarded port to, or skips
// the test when the machine has none. Loopback is not usable: it publishes a port that no
// connection can reach on a gVisor guest.
func firstGlobalIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("enumerate interfaces: %v", err)
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
			return ipnet.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 address on this machine")
	return ""
}

// The interface a forward is configured with is what podman publishes on and what the UI
// has to show, since it is also where the port answers. A stored 0.0.0.0 means the same
// thing as no address and must not read back as an address.
func TestCreate_PublishesOnConfiguredInterface(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}
	hostIP := firstGlobalIPv4(t)

	taskID := "test-sandbox-iface"
	dir := t.TempDir()
	assigned, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{
		ForwardPorts: []types.PortForward{
			{ContainerPort: 8080},
			{ContainerPort: 8081, HostIP: hostIP},
			{ContainerPort: 8082, HostIP: "0.0.0.0"},
		},
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sm.Stop(taskID) })

	want := map[int]string{8080: "", 8081: hostIP, 8082: ""}
	got := map[int]string{}
	for _, p := range assigned {
		got[p.ContainerPort] = p.HostIP
		if p.HostPort < 1 || p.HostPort > 65535 {
			t.Errorf("container port %d got host port %d", p.ContainerPort, p.HostPort)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("assigned ports = %v, want %v", got, want)
	}

	live, err := sm.GetContainerPorts(taskID)
	if err != nil {
		t.Fatalf("read back ports: %v", err)
	}
	liveByContainer := map[int]string{}
	for _, p := range live {
		liveByContainer[p.ContainerPort] = p.HostIP
	}
	if fmt.Sprint(liveByContainer) != fmt.Sprint(want) {
		t.Errorf("podman reports %v for the same container, want %v", liveByContainer, want)
	}
}

// Lines as printed by `podman port` on this host, including the unbracketed IPv6 form that
// the old colon-split silently dropped.
func TestParsePortBinding(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		want  types.AssignedPort
		valid bool
	}{
		{"wildcard IPv4", "8080/tcp -> 0.0.0.0:18120", types.AssignedPort{HostPort: 18120, ContainerPort: 8080}, true},
		{"star", "3000/tcp -> *:41305", types.AssignedPort{HostPort: 41305, ContainerPort: 3000}, true},
		{"loopback bind", "3000/tcp -> 127.0.0.1:42585", types.AssignedPort{HostPort: 42585, ContainerPort: 3000, HostIP: "127.0.0.1"}, true},
		{"LAN bind", "8080/tcp -> 192.168.1.100:41234", types.AssignedPort{HostPort: 41234, ContainerPort: 8080, HostIP: "192.168.1.100"}, true},
		{"IPv6 bind, unbracketed", "8080/tcp -> fdd4:9b13:e8c4:62bc:6f03:7d15:4b28:c062:18121", types.AssignedPort{HostPort: 18121, ContainerPort: 8080, HostIP: "fdd4:9b13:e8c4:62bc:6f03:7d15:4b28:c062"}, true},
		{"bracketed IPv6 wildcard", "8080/tcp -> [::]:18121", types.AssignedPort{HostPort: 18121, ContainerPort: 8080}, true},
		{"no arrow", "8080/tcp", types.AssignedPort{}, false},
		{"no host port", "8080/tcp -> 192.168.1.100", types.AssignedPort{}, false},
		{"port is not a number", "8080/tcp -> 192.168.1.100:http", types.AssignedPort{}, false},
		{"container port is not a number", "http/tcp -> 0.0.0.0:8080", types.AssignedPort{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parsePortBinding(tt.line)
			if ok != tt.valid {
				t.Fatalf("parsePortBinding(%q) ok = %v, want %v", tt.line, ok, tt.valid)
			}
			if ok && got != tt.want {
				t.Errorf("parsePortBinding(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

// Every-interface spellings must never reach podman as an address: an explicit 0.0.0.0 is
// the same publish as no address, and anything prefixed changes the forward target.
func TestForwardPortSpecDefaultStaysUnprefixed(t *testing.T) {
	for _, in := range []string{"", "0.0.0.0", "::", " 0.0.0.0 "} {
		hostIP, err := types.NormalizeSandboxHostIP(in)
		if err != nil {
			t.Fatalf("NormalizeSandboxHostIP(%q): %v", in, err)
		}
		if got := forwardPortSpec(hostIP, 41234, 80); got != "41234:80" {
			t.Errorf("%q normalized to %q, publishing %q; want \"41234:80\"", in, hostIP, got)
		}
	}
}

// TestCreate_RejectsAgentSandboxMount checks an agent extension's declared mounts are refused
// before any container exists, the way the project's own mounts are.
func TestCreate_RejectsAgentSandboxMount(t *testing.T) {
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
		available: false,
	}
	host := t.TempDir()
	for _, tc := range []struct {
		name string
		m    types.SandboxMount
	}{
		{"the host root", types.SandboxMount{HostPath: "/", ContainerPath: "/mnt/host", Mode: "rw"}},
		{"a relative host path", types.SandboxMount{HostPath: "agent-config", ContainerPath: "/mnt/x", Mode: "ro"}},
		{"a host path that walks up", types.SandboxMount{HostPath: host + "/../outside", ContainerPath: "/mnt/x", Mode: "ro"}},
		{"a container path rewriting the mount spec", types.SandboxMount{HostPath: host, ContainerPath: "/mnt/x:rw", Mode: "ro"}},
		{"a container path that walks up", types.SandboxMount{HostPath: host, ContainerPath: "/mnt/../etc", Mode: "ro"}},
		{"a mode that is neither ro nor rw", types.SandboxMount{HostPath: host, ContainerPath: "/mnt/x", Mode: "write"}},
	} {
		_, err := sm.Create("task-1", t.TempDir(), &types.Project{Name: "test-proj"}, SandboxOpts{
			AgentSandbox: &types.AgentSandbox{Mounts: []types.SandboxMount{tc.m}},
		})
		if err == nil || !strings.Contains(err.Error(), "agent sandbox mount") {
			t.Errorf("%s: Create error = %v, want a refusal naming the agent sandbox mount", tc.name, err)
		}
	}
}

// TestCreate_AcceptsAgentSandboxMount checks the gate refuses only what it is about.
func TestCreate_AcceptsAgentSandboxMount(t *testing.T) {
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
		available: false,
	}
	_, err := sm.Create("task-1", t.TempDir(), &types.Project{Name: "test-proj"}, SandboxOpts{
		AgentSandbox: &types.AgentSandbox{Mounts: []types.SandboxMount{
			{HostPath: t.TempDir(), ContainerPath: "/mnt/agent", Mode: "ro"},
			{},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "gVisor not available") {
		t.Fatalf("Create error = %v, want the unavailable-runtime refusal that follows the gate", err)
	}
}
