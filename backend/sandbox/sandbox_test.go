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

	"mike/backend/testutil/sandboxcheck"
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
	cleanupFixtureImages()
	code := m.Run()
	cleanupStaleTestContainers()
	cleanupFixtureImages()
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

// cleanupFixtureImages removes any fixture image an interrupted run left staged. The name is this file's
// own, so removing it asks nothing of the images the product builds and owns.
func cleanupFixtureImages() {
	rt := sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{})
	out, err := rt.PodmanExec("images", "--format", "{{.Repository}}:{{.Tag}}", fixtureImageRef("*"))
	if err != nil {
		return
	}
	for _, ref := range strings.Fields(strings.TrimSpace(string(out))) {
		_, _ = rt.PodmanExec("rmi", "-f", ref)
	}
}

// stageFixtureImage builds an image declaring one hostile shape GVisorCreateArgs exists to neutralise,
// so the claim about that shape needs no pull and the property under test is this file's declaration
// rather than whatever a registry image happened to ship. The base is MIKE's own default, which every
// container test in this package starts from, and the build is told to pull nothing.
func stageFixtureImage(t *testing.T, name, instruction string) string {
	t.Helper()
	sandboxcheck.RequireBuild(t)
	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.ImageReady(BaseImage) {
		t.Skipf("%s is not in this host's image store, and every container test in this package starts from it", BaseImage)
	}

	ref := fixtureImageRef(name)
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM "+BaseImage+"\n"+instruction+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := sandboxruntime.PodmanCommandContext(t.Context(), "build", "--pull=false", "-t", ref, "-f", dockerfile, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("stage the fixture image %s: %s", ref, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _, _ = sm.podmanExec("rmi", "-f", ref) })
	return ref
}

// fixtureImageRef names a staged fixture outside the families the product names and sweeps. It takes "*"
// to answer the glob a sweep filters by.
func fixtureImageRef(name string) string { return "localhost/mike-sandbox-fixture-" + name + ":1" }

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
	t.Fatalf("no sandbox-bridge binary beside this checkout (looked for %s); the committed one is restored by `make bridge`", strings.Join(candidates, ", "))
	return ""
}

// TestManagerAvailabilityComesFromTheSharedProbe: the manager's answer is the shared probe's answer,
// read once when the manager is built — which is why a task sandbox and an extension sandbox cannot
// disagree about whether this host runs gVisor. It is a setting rather than a container, so the test
// asks it with stub binaries on PATH and runs on every host.
func TestManagerAvailabilityComesFromTheSharedProbe(t *testing.T) {
	runscOK := "#!/bin/sh\necho 'runsc version test'\n"
	for _, tc := range []struct {
		name string
		bins map[string]string
		want bool
	}{
		{"nothing on PATH", nil, false},
		{"runsc that cannot answer --version", map[string]string{"runsc": "#!/bin/sh\nexit 1\n"}, false},
		{"runsc without podman", map[string]string{"runsc": runscOK}, false},
		{"runsc and podman", map[string]string{"runsc": runscOK, "podman": "#!/bin/sh\nexit 0\n"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			for name, body := range tc.bins {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
					t.Fatalf("write stub %s: %v", name, err)
				}
			}
			t.Setenv("PATH", bin)

			if got := NewSandboxManager(sandboxruntime.Boot{}).IsAvailable(); got != tc.want {
				t.Errorf("IsAvailable() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCreateAndStop: the container a task runs in exists by name after Create and is gone after
// Stop, with the capability grant the project's Sandbox Configuration can carry appended on the way —
// podman has to accept `AppendCapabilities`' output under runsc, or every capability grant becomes a
// refused task rather than a wider sandbox.
func TestCreateAndStop(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	// Create a temp directory to use as worktree
	dir := taskWorktreeDir(t)
	taskID := "test-sandbox-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{
		Capabilities: []string{"CAP_NET_RAW", "CAP_CHOWN"},
	})
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

	// The grant did not cost the container its ordinary work.
	output, err := sm.Exec(taskID, "echo hello")
	if err != nil {
		t.Fatalf("exec in the container: %v", err)
	}
	if output != "hello\n" {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestExec(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	dir := taskWorktreeDir(t)
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
	output, err := sm.Exec(taskID, "cat hello.txt")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	// cat does not add trailing newline
	if output != "hello from sandbox" && output != "hello from sandbox\n" {
		t.Fatalf("unexpected output: %q", output)
	}

	// Test that the sandbox can write files
	_, err = sm.Exec(taskID, "echo 'modified' > hello.txt")
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

func TestExecWithInput(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	dir := taskWorktreeDir(t)
	taskID := "test-exec-input-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		// Clean up files from inside the container (they're owned by root)
		_, _ = sm.Exec(taskID, "rm -rf test_input.txt sub")
		_ = sm.Stop(taskID)
	})

	// Write a file via stdin (cat > file)
	content := []byte("line1\nline2\nline3")
	output, err := sm.ExecWithInput(taskID, "cat > test_input.txt", content)
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
	_, err = sm.ExecWithInput(taskID, "mkdir -p sub && cat > sub/special.txt", specialContent)
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
	sandboxcheck.Require(t, BaseImage)

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
		t.Fatalf("cannot start a container for the sweep to leave alone: %v", err)
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

func TestCreate_OverridesImageEntrypoint(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	// GVisorCreateArgs answers `--entrypoint ""`, and only an image that ships one can tell it is
	// there: without it PID 1 is the image's own program with MIKE's keep-alive as its arguments, which
	// exits in milliseconds and takes every exec'd shell with it. The image is staged rather than pulled
	// so the hostile property is this file's declaration, not a registry's.
	image := stageFixtureImage(t, "entrypoint", `ENTRYPOINT ["/bin/false"]`)

	dir := taskWorktreeDir(t)
	taskID := "test-entrypoint-task"

	_, err := sm.Create(taskID, dir, &types.Project{Name: "test-proj"}, SandboxOpts{
		BaseImage: image,
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
		t.Error("container is not running after Create from an image that declares an ENTRYPOINT — GVisorCreateArgs stopped clearing it")
	}
}

// TestATaskContainerFromAnImageThatDeclaresAVolumeLeaksNone fails if a task container leaves a podman volume behind:
// leftovers consume podman's fixed table of 2048 volume locks, and once they are gone the host refuses every create.
func TestATaskContainerFromAnImageThatDeclaresAVolumeLeaksNone(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)
	// `VOLUME` is the shape that leaks, and it is the shape GVisorCreateArgs' `--image-volume ignore`
	// exists for, so the image declares one here instead of borrowing a registry image that happens to.
	image := stageFixtureImage(t, "volume", `VOLUME ["/mike-fixture-volume"]`)

	before := volumeCount(t, sm)
	taskID := "test-volume-leak-task"
	if _, err := sm.Create(taskID, taskWorktreeDir(t), &types.Project{Name: "test-proj"}, SandboxOpts{
		BaseImage: image,
	}); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if err := sm.Stop(taskID); err != nil {
		t.Logf("stop reported (the volume is the subject here, not the stop): %v", err)
	}
	if after := volumeCount(t, sm); after != before {
		t.Errorf("podman volumes went %d → %d: a task container leaked one, and its lock never comes back", before, after)
	}
}

// volumeCount counts podman's volumes. MIKE never creates one — every mount it asks for is a bind — so a volume
// that appears during this test was left behind by the container it made.
func volumeCount(t *testing.T, sm *SandboxManager) int {
	t.Helper()
	out, err := sm.rt.PodmanExec("volume", "ls", "-q")
	if err != nil {
		t.Fatalf("list podman volumes: %v", err)
	}
	return len(strings.Fields(string(out)))
}

func TestCreateWithoutGVisor(t *testing.T) {
	// Simulate unavailable gVisor by creating a manager and forcing unavailable
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
		available: false,
	}

	_, err := sm.Create("task-1", taskWorktreeDir(t), &types.Project{Name: "test-proj"}, SandboxOpts{})
	if err == nil {
		t.Fatal("expected error when gVisor is unavailable")
	}
	if !strings.Contains(err.Error(), "gVisor not available") {
		t.Fatalf("expected gVisor unavailable error, got: %v", err)
	}
}

// TestCreateChecksTheImageRefBeforeTheRuntime: a ref podman could read as a flag is refused by name,
// and a well-formed one is not. Both refusals land before the availability probe, so a host without
// runsc reports the configuration error instead of hiding it under "gVisor not available".
func TestCreateChecksTheImageRefBeforeTheRuntime(t *testing.T) {
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
		available: false,
	}

	for _, tc := range []struct {
		image string
		want  string
	}{
		{"-v", "invalid sandbox image"},
		{"--format=json", "invalid sandbox image"},
		{"-t something", "invalid sandbox image"},
		{"docker.io/library/ubuntu:latest", "gVisor not available"},
	} {
		_, err := sm.Create("test-image-ref", taskWorktreeDir(t), &types.Project{Name: "test-proj"}, SandboxOpts{
			BaseImage: tc.image,
		})
		if err == nil {
			t.Fatalf("Create accepted image ref %q", tc.image)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Create(image %q) error = %v, want it to name %q", tc.image, err, tc.want)
		}
	}
}

// A missing sandbox image is named plainly and fast: qualified refs plus --pull=never mean no retries
// against a guessed registry.
func TestCreate_MissingImageIsActionable(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	taskID := "test-missing-image-task"
	missing := "localhost/kanban-does-not-exist-image:1"

	started := time.Now()
	_, err := sm.Create(taskID, taskWorktreeDir(t), &types.Project{Name: "test-proj"}, SandboxOpts{BaseImage: missing})
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

// TestContainerIsRunningCachesItsLivenessAnswer: the cache exists because a status display polls, and
// an inspect per read is what it would otherwise cost. A fresh entry is the answer; an expired one
// asks podman again, and an answer that found no container is not left cached.
func TestContainerIsRunningCachesItsLivenessAnswer(t *testing.T) {
	newManager := func(key, container string, stamp time.Time) *SandboxManager {
		sm := &SandboxManager{
			active:        map[string]string{},
			mountPaths:    map[string]string{},
			agents:        map[string]*agentConn{},
			livenessCache: map[string]time.Time{},
			available:     true,
		}
		if key != "" {
			sm.active[key] = container
			sm.livenessCache[key] = stamp
		}
		return sm
	}

	// A key with no container behind it is answered without asking podman.
	if newManager("", "", time.Time{}).ContainerIsRunning("nonexistent") {
		t.Error("expected ContainerIsRunning to return false for unknown task")
	}

	for _, tc := range []struct {
		name    string
		key     string
		stamp   time.Time
		want    bool
		tracked bool
	}{
		{"a fresh entry is the answer, with no inspect", "task-cached", time.Now(), true, true},
		// The container does not exist, so the re-ask answers false and clears the entry.
		{"an expired entry asks again", "task-expired", time.Now().Add(-10 * time.Minute), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := newManager(tc.key, "sandbox-"+tc.key, tc.stamp)
			if got := sm.ContainerIsRunning(tc.key); got != tc.want {
				t.Errorf("ContainerIsRunning(%q) = %v, want %v", tc.key, got, tc.want)
			}
			_, cached := sm.livenessCache[tc.key]
			if cached != tc.tracked {
				t.Errorf("liveness cache entry present = %v, want %v", cached, tc.tracked)
			}
		})
	}

	// Stop invalidates what it stopped.
	sm := newManager("task-stopped", "sandbox-task-stopped", time.Now())
	_ = sm.Stop("task-stopped")
	if _, cached := sm.livenessCache["task-stopped"]; cached {
		t.Error("expected liveness cache to be invalidated by Stop")
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
	sandboxcheck.Require(t, BaseImage)

	dir := taskWorktreeDir(t)
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
	sandboxcheck.Require(t, BaseImage)

	dir := taskWorktreeDir(t)
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

// TestStartProcess_NonexistentTask: a key with no container behind it is refused by name, before
// anything is asked of podman — so this test needs no container and is not gated on one.
func TestStartProcess_NonexistentTask(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))

	// Try to start a process in a non-existent container
	_, err := sm.StartProcess("nonexistent-task", "echo", []string{"hello"})
	if err == nil {
		t.Fatal("expected error for non-existent task")
	}
}

// TestMemoryLimitEnforced verifies the sandbox memory limit is a hard cap:
// a process that allocates past the limit is OOM-killed by the kernel cgroup
// OOM killer (exit 137) while the sandbox container itself survives.
func TestMemoryLimitEnforced(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	dir := taskWorktreeDir(t)
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
	out, err := sm.Exec(taskID, memhog)
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
	sandboxcheck.Require(t, BaseImage)

	dir := taskWorktreeDir(t)
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
	out, err := sm.Exec(taskID, hog)
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

// TestExecEntryPointsCarryTheWholeSandboxConfig: each exec entry point creates the container itself
// when none is running, so each one has to hand `Create` the whole Sandbox Configuration — the memory
// limit, the declared mounts and the environment. An entry point that dropped a field would run its
// commands in a container missing configuration nobody told it about.
func TestExecEntryPointsCarryTheWholeSandboxConfig(t *testing.T) {
	sandboxcheck.Require(t, BaseImage)

	for _, tc := range []struct {
		name string
		// run creates the container through one entry point and answers with the guest's output and
		// the key its container is tracked under, so the subtest can stop it.
		run  func(t *testing.T, sm *SandboxManager) (out, key string, err error)
		want []string
	}{
		{
			name: "ExecProject carries the mounts and the envs",
			run: func(t *testing.T, sm *SandboxManager) (string, string, error) {
				dir := t.TempDir()
				project := &types.Project{ID: "test-execproj-fullcfg", Name: "test-execproj", Path: dir}
				mountDir := filepath.Join(dir, "custom-mount")
				if err := os.MkdirAll(mountDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(mountDir, "marker"), []byte("on-the-mount\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				opts := SandboxOpts{
					MemoryLimit:   512,
					SandboxMounts: []types.SandboxMount{{HostPath: mountDir, ContainerPath: "/mnt/custom", Mode: "rw"}},
					SandboxEnvs:   []types.SandboxEnv{{Key: "TEST_SANDBOX_VAR", Value: "hello"}},
				}
				out, err := sm.ExecProject(project, "echo $TEST_SANDBOX_VAR && cat /mnt/custom/marker", opts)
				return out, "proj:" + project.ID, err
			},
			want: []string{"hello", "on-the-mount"},
		},
		{
			name: "ExecProjectWithInput carries the envs alongside the bytes it pipes in",
			run: func(t *testing.T, sm *SandboxManager) (string, string, error) {
				project := &types.Project{ID: "test-execproj-input-fullcfg", Name: "test-execproj-input", Path: t.TempDir()}
				opts := SandboxOpts{
					MemoryLimit: 256,
					SandboxEnvs: []types.SandboxEnv{{Key: "INPUT_TEST_VAR", Value: "works"}},
				}
				out, err := sm.ExecProjectWithInput(project, "cat && echo $INPUT_TEST_VAR", []byte("stdin-data\n"), opts)
				return out, "proj:" + project.ID, err
			},
			want: []string{"stdin-data", "works"},
		},
		{
			name: "ExecTask carries the envs of a task container",
			run: func(t *testing.T, sm *SandboxManager) (string, string, error) {
				const taskID = "test-exectask-fullcfg"
				opts := SandboxOpts{
					MemoryLimit: 128,
					SandboxEnvs: []types.SandboxEnv{{Key: "TASK_TEST_VAR", Value: "task-value"}},
				}
				out, err := sm.ExecTask(taskID, taskWorktreeDir(t), &types.Project{Name: "test-exectask"}, "echo $TASK_TEST_VAR", opts)
				return out, taskID, err
			},
			want: []string{"task-value"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewSandboxManager(bootWithBridge(t))
			out, key, err := tc.run(t, sm)
			t.Cleanup(func() { _ = sm.Stop(key) })
			if err != nil {
				t.Fatalf("%v (output: %s)", err, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("the guest answered %q, which is missing %q — the configuration never reached the container",
						out, want)
				}
			}
		})
	}
}

// TestEnsureProjectContainerRecreatesAContainerThatNoLongerMatches: the running project container
// holds the configuration of the row as it was when it was created, so a container that no longer
// matches is recreated rather than reused — whether it went away between calls or the checkout it
// mounts moved out from under it.
func TestEnsureProjectContainerRecreatesAContainerThatNoLongerMatches(t *testing.T) {
	sandboxcheck.Require(t, BaseImage)

	t.Run("a stopped container comes back with the new config", func(t *testing.T) {
		sm := NewSandboxManager(bootWithBridge(t))
		project := &types.Project{ID: "test-recreate-proj", Name: "test-recreate", Path: t.TempDir()}

		if _, err := sm.ExecProject(project, "echo ok", SandboxOpts{SandboxEnvs: []types.SandboxEnv{{Key: "RECREATE_VAR", Value: "v1"}}}); err != nil {
			t.Fatalf("first ExecProject: %v", err)
		}
		_ = sm.Stop("proj:" + project.ID)

		output, err := sm.ExecProject(project, "echo $RECREATE_VAR",
			SandboxOpts{SandboxEnvs: []types.SandboxEnv{{Key: "RECREATE_VAR", Value: "v2"}}})
		t.Cleanup(func() { _ = sm.Stop("proj:" + project.ID) })
		if err != nil {
			t.Fatalf("second ExecProject (recreate): %v", err)
		}
		if !strings.Contains(output, "v2") {
			t.Fatalf("expected recreated container to have new env var v2, got: %q", output)
		}
	})

	t.Run("a moved checkout is remounted", func(t *testing.T) {
		sm := NewSandboxManager(bootWithBridge(t))
		projectID := "test-stale-mount-proj"
		project := &types.Project{ID: projectID, Name: "test-stale-mount"}
		taskID := "proj:" + projectID
		t.Cleanup(func() { _ = sm.Stop(taskID) })

		// Create the project container at dir1, then "move" the project to dir2.
		dir1, dir2 := t.TempDir(), t.TempDir()
		project.Path = dir1

		if err := sm.ensureProjectContainer(taskID, project, SandboxOpts{}); err != nil {
			t.Fatalf("initial ensureProjectContainer: %v", err)
		}

		mountPath := "/home/sandbox/" + project.Name
		source := func() string {
			sm.mu.Lock()
			containerName := sm.active[taskID]
			sm.mu.Unlock()
			got, err := sm.rt.GetContainerMountSource(containerName, mountPath)
			if err != nil {
				t.Fatalf("GetContainerMountSource: %v", err)
			}
			return got
		}

		if got := source(); filepath.Clean(got) != filepath.Clean(dir1) {
			t.Fatalf("expected mount source %q, got %q", dir1, got)
		}

		// ensureProjectContainer with the new path must recreate the container
		// (the old one is still running) so its mount points at dir2.
		project.Path = dir2
		if err := sm.ensureProjectContainer(taskID, project, SandboxOpts{}); err != nil {
			t.Fatalf("ensureProjectContainer after move: %v", err)
		}
		if got := source(); filepath.Clean(got) != filepath.Clean(dir2) {
			t.Fatalf("expected recreated mount source %q, got %q", dir2, got)
		}
	})
}

func TestCreate_CustomMountCreatesHostDir(t *testing.T) {

	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	baseDir := taskWorktreeDir(t)
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
	sandboxcheck.Require(t, BaseImage)

	// Create a temp directory to use as worktree
	dir := taskWorktreeDir(t)
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
	out, err := sm.Exec(taskID, "touch /home/sandbox/test-proj/test-file")
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
	out, err = sm.Exec(taskID, "cat /home/sandbox/test-proj/existing-file.txt")
	if err != nil {
		t.Fatalf("read should work in plan mode: %v", err)
	}
	if out != "test content" {
		t.Errorf("expected 'test content', got %q", out)
	}
}

// The bind address reaches podman as given, since podman derives the forward target from
// it. Every-interface is spelled with no address at all, and a loopback address maps a port
// a gVisor guest cannot answer — both are the user's choice, not ours to rewrite. An explicit
// 0.0.0.0 is the same publish as no address, and anything prefixed changes the forward target,
// so the every-interface spellings are folded away before the spec is built.
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
		{"a stored 0.0.0.0", "0.0.0.0", 41234, 80, "41234:80"},
		{"a stored ::", "::", 41234, 80, "41234:80"},
		{"padded whitespace", " 0.0.0.0 ", 41234, 80, "41234:80"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostIP, err := types.NormalizeSandboxHostIP(tt.hostIP)
			if err != nil {
				t.Fatalf("NormalizeSandboxHostIP(%q): %v", tt.hostIP, err)
			}
			if got := forwardPortSpec(hostIP, tt.hostPort, tt.containerPort); got != tt.want {
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
	sandboxcheck.Require(t, BaseImage)
	hostIP := firstGlobalIPv4(t)

	taskID := "test-sandbox-iface"
	dir := taskWorktreeDir(t)
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

// TestCreate_AcceptsAgentSandboxMount checks the gate refuses only what it is about.
func TestCreate_AcceptsAgentSandboxMount(t *testing.T) {
	sm := &SandboxManager{
		active:    make(map[string]string),
		rt:        sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
		available: false,
	}
	_, err := sm.Create("task-1", taskWorktreeDir(t), &types.Project{Name: "test-proj"}, SandboxOpts{
		AgentSandbox: &types.AgentSandbox{Mounts: []types.SandboxMount{
			{HostPath: t.TempDir(), ContainerPath: "/mnt/agent", Mode: "ro"},
			{},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "gVisor not available") {
		t.Fatalf("Create error = %v, want the unavailable-runtime refusal that follows the gate", err)
	}
}
