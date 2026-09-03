package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kanban/backend/types"
	"kanban/pkg/sandboxruntime"
)

func TestSanitizeContainerID(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"ext-1", "ext-1", false},
		{"my_extension", "my_extension", false},
		{"my.extension", "my.extension", false},
		{"my.extension-1", "my.extension-1", false},
		{"my@extension", "my-extension", false}, // @ replaced with -
		{"my extension", "my-extension", false}, // space replaced with -
		{"---invalid---", "invalid", false},     // leading/trailing special chars trimmed
		{"-start-with-dash", "start-with-dash", false},
		{"_start-with-underscore", "start-with-underscore", false},
		{".start-with-dot", "start-with-dot", false},
		{"", "", true},
		{"---", "", true},
		{"___", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := sandboxruntime.NewRuntime().SanitizeID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("sandboxruntime.NewRuntime().SanitizeID(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("sandboxruntime.NewRuntime().SanitizeID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtensionRuntimeStatus_MarshalJSON(t *testing.T) {
	now := time.Now()
	status := ExtensionRuntimeStatus{
		Running:   true,
		Container: "kanban-ext-test",
		RefCount:  2,
		CreatedAt: now,
	}

	data, err := status.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	// Verify the JSON contains expected fields
	jsonStr := string(data)
	if !contains(jsonStr, `"running":true`) {
		t.Errorf("JSON missing running:true: %s", jsonStr)
	}
	if !contains(jsonStr, `"container":"kanban-ext-test"`) {
		t.Errorf("JSON missing container name: %s", jsonStr)
	}
	if !contains(jsonStr, `"ref_count":2`) {
		t.Errorf("JSON missing ref_count: %s", jsonStr)
	}
	if !contains(jsonStr, `"created_at"`) {
		t.Errorf("JSON missing created_at: %s", jsonStr)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestExtensionContainerPrefix(t *testing.T) {
	if ExtensionContainerPrefix != "kanban-ext" {
		t.Errorf("ExtensionContainerPrefix = %q, want kanban-ext", ExtensionContainerPrefix)
	}
}

func TestApplyMemoryLimit_NotRunningIsNoOp(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil) // nil store is ok for this test
	// No sandbox was created — applying or clearing a limit is a silent no-op:
	// the next Create applies the persisted limit from scratch.
	if err := sm.ApplyMemoryLimit("no-such-ext", 512); err != nil {
		t.Errorf("ApplyMemoryLimit(512) = %v, want nil (no running sandbox)", err)
	}
	if err := sm.ApplyMemoryLimit("no-such-ext", 0); err != nil {
		t.Errorf("ApplyMemoryLimit(0) = %v, want nil (no running sandbox)", err)
	}
}

// TestApplyMemoryLimit_RunningSandbox verifies against a real gVisor container that the limit
// written at Create can be raised and cleared in place, with no container restart.
func TestApplyMemoryLimit_RunningSandbox(t *testing.T) {
	if !checkExtensionSandboxAvailable() {
		t.Skip("gVisor (runsc) or podman not available")
	}
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	sm.available = true
	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	t.Setenv("MIKE_AGENT_PATH", filepath.Join(projectRoot, "sandbox", "sandbox-bridge"))

	const extID = "test-live-mem"
	containerName := ExtensionContainerPrefix + "-" + extID
	defer func() {
		_ = sm.Stop(extID)
		_ = sm.rt.RemoveContainer(containerName)
	}()

	ext := &Extension{
		ID:              extID,
		ContainerConfig: ContainerConfig{Image: "docker.io/library/ubuntu:latest"},
		SandboxConfig:   SandboxConfig{MemoryLimit: 256},
	}
	if _, err := sm.Create(ext, nil); err != nil {
		t.Skipf("cannot create sandbox container: %v", err)
	}

	readMax := func() string {
		dir, err := sm.rt.ContainerCgroupDir(containerName)
		if err != nil {
			t.Fatalf("ContainerCgroupDir: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "memory.max"))
		if err != nil {
			t.Fatalf("read memory.max: %v", err)
		}
		return strings.TrimSpace(string(data))
	}

	if got, want := readMax(), fmt.Sprintf("%d", 256<<20); got != want {
		t.Fatalf("after Create(256): memory.max = %q, want %s", got, want)
	}

	// Raise the limit live.
	if err := sm.ApplyMemoryLimit(extID, 512); err != nil {
		t.Fatalf("ApplyMemoryLimit(512) = %v", err)
	}
	if got, want := readMax(), fmt.Sprintf("%d", 512<<20); got != want {
		t.Errorf("after ApplyMemoryLimit(512): memory.max = %q, want %s", got, want)
	}

	// Clear the limit live (0 = no limit).
	if err := sm.ApplyMemoryLimit(extID, 0); err != nil {
		t.Fatalf("ApplyMemoryLimit(0) = %v", err)
	}
	if got := readMax(); got != "max" {
		t.Errorf("after ApplyMemoryLimit(0): memory.max = %q, want max", got)
	}
}

func TestNewExtensionSandboxManager(t *testing.T) {
	// Test that manager can be created (without actual podman)
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil) // nil store is ok for this test
	if sm == nil {
		t.Fatal("NewExtensionSandboxManager() = nil")
	}
	// Manager should be created even if gVisor is not available
	// (it just won't be able to create sandboxes)
}

func TestIsAvailable(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	// Just verify it doesn't panic
	_ = sm.IsAvailable()
}

func TestListRunning_Empty(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	result := sm.ListRunning()
	if result == nil {
		t.Fatal("ListRunning() = nil")
	}
	if len(result) != 0 {
		t.Errorf("ListRunning() count = %d, want 0", len(result))
	}
}

func TestIsRunning_NotCreated(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	if sm.IsRunning("nonexistent") {
		t.Error("IsRunning(nonexistent) = true, want false")
	}
}

func TestStop_NotCreated(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	err := sm.Stop("nonexistent")
	if err != nil {
		t.Errorf("Stop(nonexistent) = %v, want nil", err)
	}
}

func TestDestroy_NotCreated(t *testing.T) {
	reg := NewRegistryClient()
	store := testStore(t)
	sm := NewExtensionSandboxManager(reg, store)
	err := sm.Destroy("nonexistent")
	if err != nil {
		t.Errorf("Destroy(nonexistent) = %v, want nil", err)
	}
}

func TestStartSession_UnknownSandbox(t *testing.T) {
	sm := NewExtensionSandboxManager(NewRegistryClient(), nil)
	client, err := sm.StartSession("nonexistent")
	if err == nil {
		t.Error("StartSession(nonexistent) = nil, want error — a session needs a container to exec into")
	}
	if client != nil {
		t.Error("StartSession(nonexistent) returned non-nil client")
	}
}

func TestReleaseSession_NotCreated(t *testing.T) {
	sm := NewExtensionSandboxManager(NewRegistryClient(), nil)
	// Must not panic, and must not credit a hand-back for a sandbox that was never created.
	if sm.ReleaseSession("nonexistent", testBridgeClient(t)) {
		t.Error("ReleaseSession credited a hand-back for a sandbox with no state")
	}
}

func TestGracePeriod_TimerSetOnLastSession(t *testing.T) {
	sm := NewExtensionSandboxManager(NewRegistryClient(), nil)

	extID := "test-grace-period"
	sm.mu.Lock()
	sm.states[extID] = &extensionSandboxState{containerName: "kanban-ext-test"}
	sm.mu.Unlock()

	client := testBridgeClient(t)
	registerSession(t, sm, extID, client)

	// The last hand-back should arm the delayed stop.
	if !sm.ReleaseSession(extID, client) {
		t.Fatal("ReleaseSession refused the session it had registered")
	}
	sm.mu.Lock()
	state := sm.states[extID]
	timer := (*time.Timer)(nil)
	if state != nil {
		timer = state.stopTimer
	}
	sm.mu.Unlock()

	if state == nil {
		t.Fatal("state should still exist during the grace period")
	}
	if timer == nil {
		t.Error("stopTimer should be set once the last session is handed back")
	}
	cancelGrace(t, sm, extID)
}

func TestGracePeriod_CancelledOnReconnect(t *testing.T) {
	sm := NewExtensionSandboxManager(NewRegistryClient(), nil)
	extID := "test-grace-reconnect"

	sm.mu.Lock()
	sm.states[extID] = &extensionSandboxState{containerName: "kanban-ext-test"}
	sm.mu.Unlock()

	done := make(chan struct{}, 1)
	sm.mu.Lock()
	sm.states[extID].stopTimer = time.AfterFunc(100*time.Millisecond, func() {
		done <- struct{}{}
	})
	// What StartSession does when a connection arrives during the grace period.
	sm.cancelStopLocked(extID, sm.states[extID], "session connected")
	sm.mu.Unlock()

	select {
	case <-done:
		t.Error("timer should have been cancelled, but it fired")
	case <-time.After(200 * time.Millisecond):
		// Good — timer did not fire
	}
}

func TestGracePeriod_TimerFiresAndStops(t *testing.T) {
	sm := NewExtensionSandboxManager(NewRegistryClient(), nil)
	extID := "test-grace-fires"
	stopped := make(chan string, 1)

	sm.mu.Lock()
	sm.states[extID] = &extensionSandboxState{containerName: "kanban-ext-test"}
	sm.mu.Unlock()

	client := testBridgeClient(t)
	registerSession(t, sm, extID, client)
	sm.ReleaseSession(extID, client)

	// Replace the 15-minute timer with a short one carrying the same body: stop only while the
	// refcount is still zero.
	sm.mu.Lock()
	state := sm.states[extID]
	if state != nil && state.stopTimer != nil {
		state.stopTimer.Stop()
		state.stopTimer = time.AfterFunc(50*time.Millisecond, func() {
			sm.mu.Lock()
			defer sm.mu.Unlock()
			if sm.refs[extID] <= 0 {
				stopped <- extID
				delete(sm.states, extID)
				delete(sm.refs, extID)
			}
		})
	}
	sm.mu.Unlock()

	select {
	case id := <-stopped:
		if id != extID {
			t.Errorf("expected %s to be stopped, got %s", extID, id)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("grace period timer did not fire")
	}
}

func TestCreate_RejectsLeadingDashImage(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	sm.available = true
	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	bridgePath := filepath.Join(projectRoot, "sandbox", "sandbox-bridge")
	t.Setenv("MIKE_AGENT_PATH", bridgePath)

	ext := &Extension{
		ID: "test-dash-image",
		ContainerConfig: ContainerConfig{
			Image: "-v /etc/passwd:/tmp/host",
		},
	}

	_, err := sm.Create(ext, nil)
	if err == nil {
		t.Fatal("Create() with leading-dash image should return error")
	}
	if !strings.Contains(err.Error(), "invalid extension image") {
		t.Errorf("Create() error = %v, want error containing 'invalid extension image'", err)
	}
}

func TestCreate_AcceptsValidImage(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	sm.available = true
	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	bridgePath := filepath.Join(projectRoot, "sandbox", "sandbox-bridge")
	t.Setenv("MIKE_AGENT_PATH", bridgePath)

	ext := &Extension{
		ID: "test-valid-image",
		ContainerConfig: ContainerConfig{
			Image: "docker.io/library/ubuntu:latest",
		},
	}
	_, err := sm.Create(ext, nil)
	if err != nil && strings.Contains(err.Error(), "invalid extension image") {
		t.Errorf("Create() with valid image should not fail at image validation, got: %v", err)
	}
}

func TestCreate_DefaultImagePassesValidation(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	sm.available = true
	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	bridgePath := filepath.Join(projectRoot, "sandbox", "sandbox-bridge")
	t.Setenv("MIKE_AGENT_PATH", bridgePath)

	ext := &Extension{
		ID: "test-default-image",
	}
	_, err := sm.Create(ext, nil)
	if err != nil && strings.Contains(err.Error(), "invalid extension image") {
		t.Errorf("Create() with empty (defaulted) image should not fail at image validation, got: %v", err)
	}
}

func TestCreate_RejectsShellInjectionImage(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	sm.available = true
	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Join(filepath.Dir(filename), "..", "..")
	bridgePath := filepath.Join(projectRoot, "sandbox", "sandbox-bridge")
	t.Setenv("MIKE_AGENT_PATH", bridgePath)

	tests := []struct {
		name  string
		image string
	}{
		{"shell substitution", "$(rm -rf /)"},
		{"semicolon injection", "ubuntu:latest; rm -rf /"},
		{"pipe injection", "ubuntu | cat /etc/passwd"},
		{"backtick injection", "ubun`whoami`t"},
		{"space in image", "ubuntu latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext := &Extension{
				ID: "test-injection-image",
				ContainerConfig: ContainerConfig{
					Image: tt.image,
				},
			}
			_, err := sm.Create(ext, nil)
			if err == nil {
				t.Fatalf("Create() with image %q should return error", tt.image)
			}
			if !strings.Contains(err.Error(), "invalid extension image") {
				t.Errorf("Create() error = %v, want error containing 'invalid extension image'", err)
			}
		})
	}
}

// hasArg reports whether the given flag (with its value as a following arg) is
// present in the create args. Returns false if the flag is the last element or
// the value does not immediately follow.
func hasArgPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestBuildExtensionCreateArgs_AlwaysNetworkIsolated(t *testing.T) {
	// Extension sandboxes must NEVER have network access, regardless of the
	// network_isolation preference in the config.
	for _, name := range []string{"isolated", "not_isolated"} {
		t.Run(name, func(t *testing.T) {
			ext := &Extension{
				ID:   "ext-network-test",
				Name: name,
				SandboxConfig: SandboxConfig{
					NetworkIsolation: name == "isolated",
				},
			}
			args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-network-test", "ubuntu:latest", nil, nil)
			if !hasArgPair(args, "--network", "none") {
				t.Errorf("args missing --network none (NetworkIsolation=%v): %v", ext.SandboxConfig.NetworkIsolation, args)
			}
		})
	}
}

func TestBuildExtensionCreateArgs_KeepsHardeningFlags(t *testing.T) {
	ext := &Extension{ID: "ext-hardening-test"}
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-hardening-test", "ubuntu:latest", nil, nil)

	if !hasArgPair(args, "--runtime", "runsc") {
		t.Error("args missing --runtime runsc")
	}
	if !hasArgPair(args, "--cap-drop", "ALL") {
		t.Error("args missing --cap-drop ALL")
	}
	if !hasArgPair(args, "--security-opt", "no-new-privileges") {
		t.Error("args missing --security-opt no-new-privileges")
	}
}

// An extension sandbox must never pull during `podman create`. `--pull=never` is a create flag, so
// it comes after `create` and before the image reference.
func TestBuildExtensionCreateArgs_NeverPullsImplicitly(t *testing.T) {
	ext := &Extension{ID: "ext-pull-test"}
	image := ExtensionImageRef("gopls", "1.0.0")
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-pull-test", image, nil, nil)

	if !hasArgPair(args, "--pull", "never") {
		t.Fatalf("args missing --pull never: %v", args)
	}
	createAt, pullAt, imageAt := -1, -1, -1
	for i, a := range args {
		switch a {
		case "create":
			if createAt == -1 {
				createAt = i
			}
		case "--pull":
			pullAt = i
		case image:
			imageAt = i
		}
	}
	if createAt == -1 || pullAt == -1 || imageAt == -1 {
		t.Fatalf("args missing create/--pull/image: %v", args)
	}
	if pullAt < createAt {
		t.Errorf("--pull at %d precedes `create` at %d — podman would read it as a global flag: %v", pullAt, createAt, args)
	}
	if imageAt < pullAt {
		t.Errorf("image ref at %d precedes --pull at %d: %v", imageAt, pullAt, args)
	}
}

func TestBuildExtensionCreateArgs_MountsProjectsReadOnly(t *testing.T) {
	ext := &Extension{ID: "ext-mount-test"}
	projectMounts := map[string]string{
		"/host/proj-a": "/mnt/proj-a",
		"/host/proj-b": "/mnt/proj-b",
	}
	args, mounted := buildExtensionCreateArgs(ext, "kanban-ext-ext-mount-test", "ubuntu:latest", projectMounts, nil)

	for _, m := range []string{"/host/proj-a:/mnt/proj-a:rprivate,ro", "/host/proj-b:/mnt/proj-b:rprivate,ro"} {
		if !hasArg(args, m) {
			t.Errorf("args missing read-only project mount %q: %v", m, args)
		}
	}

	// mountedProjects should be sorted for deterministic comparison.
	if len(mounted) != 2 || mounted[0] != "/host/proj-a" || mounted[1] != "/host/proj-b" {
		t.Errorf("mountedProjects = %v, want sorted [proj-a proj-b]", mounted)
	}
}

func TestBuildExtensionCreateArgs_EntrypointAndImage(t *testing.T) {
	ext := &Extension{ID: "ext-entry-test"}
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-entry-test", "my-custom-image:v1", nil, nil)

	if !hasArg(args, "my-custom-image:v1") {
		t.Error("args missing image reference")
	}
	if !hasArg(args, "chmod +x /sandbox-bridge && sleep infinity") {
		t.Error("args missing sleep infinity entrypoint")
	}
}

func TestBuildExtensionCreateArgs_ClearsImageEntrypoint(t *testing.T) {
	// An image with an ENTRYPOINT (e.g. alpine/git -> ["git"]) would treat the keep-alive command
	// as a subcommand and exit, so an empty --entrypoint clears it.
	ext := &Extension{ID: "ext-entrypoint-clear-test"}
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-entrypoint-clear-test", "docker.io/alpine/git:latest", nil, nil)

	if !hasArgPair(args, "--entrypoint", "") {
		t.Errorf(`args missing --entrypoint "" (must clear the image entrypoint): %v`, args)
	}
}

func TestBuildExtensionCreateArgs_MemoryLimit(t *testing.T) {
	t.Run("zero limit omits the memory flag", func(t *testing.T) {
		// Default (unset) sandbox config = 0 = no limit.
		ext := &Extension{ID: "ext-mem-zero"}
		args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-mem-zero", "ubuntu:latest", nil, nil)
		if hasArg(args, "--memory") {
			t.Errorf("args should not set --memory for a 0 (no-limit) sandbox: %v", args)
		}
	})

	t.Run("negative limit is clamped to no limit", func(t *testing.T) {
		ext := &Extension{ID: "ext-mem-neg", SandboxConfig: SandboxConfig{MemoryLimit: -64}}
		args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-mem-neg", "ubuntu:latest", nil, nil)
		if hasArg(args, "--memory") {
			t.Errorf("args should not set --memory for a negative limit: %v", args)
		}
	})

	t.Run("positive limit sets the memory flag", func(t *testing.T) {
		ext := &Extension{ID: "ext-mem-set", SandboxConfig: SandboxConfig{MemoryLimit: 512}}
		args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-mem-set", "ubuntu:latest", nil, nil)
		if !hasArgPair(args, "--memory", "512M") {
			t.Errorf("args missing --memory 512M: %v", args)
		}
	})
}

func TestFindLocalDockerfile(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)

	// Build a temp extensions directory with a nested Dockerfile.
	extRoot := t.TempDir()
	dfDir := filepath.Join(extRoot, "sveltejs", "svelte-language-server")
	if err := os.MkdirAll(dfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dfDir, "Dockerfile"), []byte("FROM node:22-alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sibling extension with no Dockerfile should not interfere.
	if err := os.MkdirAll(filepath.Join(extRoot, "some-author", "other-ext"), 0o755); err != nil {
		t.Fatal(err)
	}
	sm.extensionsDir = extRoot

	// Matches by extension name regardless of the author segment.
	got, err := sm.findLocalDockerfile("svelte-language-server")
	if err != nil {
		t.Fatalf("findLocalDockerfile error: %v", err)
	}
	want := filepath.Join(dfDir, "Dockerfile")
	if got != want {
		t.Errorf("findLocalDockerfile = %q, want %q", got, want)
	}

	// No Dockerfile for an unknown extension.
	got, err = sm.findLocalDockerfile("does-not-exist")
	if err != nil {
		t.Fatalf("findLocalDockerfile(unknown) error: %v", err)
	}
	if got != "" {
		t.Errorf("findLocalDockerfile(unknown) = %q, want empty", got)
	}

	// Missing extensions dir returns empty (no error).
	sm.extensionsDir = filepath.Join(t.TempDir(), "nope")
	got, err = sm.findLocalDockerfile("svelte-language-server")
	if err != nil {
		t.Fatalf("findLocalDockerfile(missing dir) error: %v", err)
	}
	if got != "" {
		t.Errorf("findLocalDockerfile(missing dir) = %q, want empty", got)
	}
}

// With --pull=never, a missing image is built, pulled, or reported — never fetched during creation.
// A "localhost/…" ref is a local build and must never be fetched.
func TestPullsMissingRemoteImage(t *testing.T) {
	tests := []struct {
		image string
		want  bool
	}{
		{"docker.io/library/ubuntu:24.04", true},
		{"quay.io/o/r", true},
		{"localhost:5000/team/img:1", true}, // a real registry on a port, not the local one
		{"localhost/sandbox-mike", false},
		{"localhost/kanban-ext-gopls:1.0.0", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := pullsMissingRemoteImage(tc.image); got != tc.want {
			t.Errorf("pullsMissingRemoteImage(%q) = %v, want %v", tc.image, got, tc.want)
		}
	}
}

// A local-build ref that has gone missing (pruned image, no Dockerfile) must not be pulled: the
// caller reports "image is not available locally" instead of podman retrying a registry.
func TestEnsureExtensionImage_LocalBuildIsNeverPulled(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	sm.extensionsDir = filepath.Join(t.TempDir(), "extensions")

	ext := &Extension{ID: "ext-no-df", Name: "some-local-build"}
	missing := ExtensionImageRef("some-local-build", "9.9.9")
	if err := sm.ensureExtensionImage(ext, missing); err != nil {
		t.Errorf("ensureExtensionImage(%s) = %v, want nil — a local build is never pulled", missing, err)
	}
}

func TestCustomMountSnapshot(t *testing.T) {
	// Empty mounts -> nil.
	if got := customMountSnapshot(nil); got != nil {
		t.Errorf("customMountSnapshot(nil) = %v, want nil", got)
	}

	// Single mount.
	mounts := []types.SandboxMount{
		{HostPath: "/a", ContainerPath: "/b", Mode: "ro"},
	}
	snap := customMountSnapshot(mounts)
	if len(snap) != 1 || snap[0] != "/a:/b:ro" {
		t.Errorf("customMountSnapshot = %v, want [\"/a:/b:ro\"]", snap)
	}

	// Multiple mounts are sorted.
	mounts2 := []types.SandboxMount{
		{HostPath: "/z", ContainerPath: "/y", Mode: "rw"},
		{HostPath: "/a", ContainerPath: "/b", Mode: "ro"},
	}
	snap2 := customMountSnapshot(mounts2)
	if len(snap2) != 2 || snap2[0] != "/a:/b:ro" || snap2[1] != "/z:/y:rw" {
		t.Errorf("customMountSnapshot = %v, want sorted", snap2)
	}
}

func TestMountsCompatible_CustomMounts(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)

	// Simulate a state with no custom mounts.
	sm.states["ext1"] = &extensionSandboxState{
		containerName:   "test",
		mountedProjects: []string{"/proj"},
		customMounts:    nil,
	}

	// Same project mounts, no custom mounts -> compatible.
	if !sm.MountsCompatible("ext1", map[string]string{"/proj": "/mnt/proj"}, nil) {
		t.Error("expected compatible with no custom mounts")
	}

	// Same project mounts, but new custom mount -> NOT compatible (needs recreation).
	newMounts := []types.SandboxMount{
		{HostPath: "/tmp/node_modules", ContainerPath: "/mnt/proj/frontend/node_modules", Mode: "ro"},
	}
	if sm.MountsCompatible("ext1", map[string]string{"/proj": "/mnt/proj"}, newMounts) {
		t.Error("expected incompatible when custom mounts are added")
	}

	// State with custom mounts, same custom mounts -> compatible.
	sm.states["ext2"] = &extensionSandboxState{
		containerName:   "test2",
		mountedProjects: []string{"/proj"},
		customMounts:    customMountSnapshot(newMounts),
	}
	if !sm.MountsCompatible("ext2", map[string]string{"/proj": "/mnt/proj"}, newMounts) {
		t.Error("expected compatible with same custom mounts")
	}

	// State with custom mounts, different custom mounts -> NOT compatible.
	diffMounts := []types.SandboxMount{
		{HostPath: "/other/node_modules", ContainerPath: "/mnt/proj/src/node_modules", Mode: "ro"},
	}
	if sm.MountsCompatible("ext2", map[string]string{"/proj": "/mnt/proj"}, diffMounts) {
		t.Error("expected incompatible when custom mounts differ")
	}
}

// ─── GetMemoryStats ──────────────────────────────────────────────────────────

func TestGetMemoryStats_NotCreated(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)

	stats := sm.GetMemoryStats("no-such-ext")
	if stats == nil {
		t.Fatal("GetMemoryStats() = nil, want zero stats")
	}
	if stats.Running {
		t.Error("Running = true, want false for unknown extension")
	}
	if stats.CurrentMB != 0 || stats.LimitMB != 0 || stats.PeakMB != 0 || stats.OOMKills != 0 {
		t.Errorf("GetMemoryStats() = %+v, want all-zero stats", stats)
	}
}

func TestGetMemoryStats_CreatedButNotRunning(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	// State exists but no container (never created / already stopped):
	// ContainerCgroupDir must fail → zero stats, no panic.
	sm.states["ext1"] = &extensionSandboxState{containerName: "kanban-ext-nonexistent"}

	stats := sm.GetMemoryStats("ext1")
	if stats == nil || stats.Running {
		t.Errorf("GetMemoryStats() = %+v, want Running=false", stats)
	}
}

// mountSpecs returns the value of every "-v" argument in a create arg list.
func mountSpecs(args []string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-v" {
			out = append(out, args[i+1])
		}
	}
	return out
}

// capAdds returns the value of every "--cap-add" argument.
func capAdds(args []string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--cap-add" {
			out = append(out, args[i+1])
		}
	}
	return out
}

func hasElem(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestBuildExtensionCreateArgs_RejectedMountsNeverReachPodman is the defence in
// depth half of the fix: a mount that the write paths would refuse — because it
// was persisted before validation existed, or came from a manifest — must never
// become a container argument.
func TestBuildExtensionCreateArgs_RejectedMountsNeverReachPodman(t *testing.T) {
	allowed := t.TempDir()
	inside := filepath.Join(allowed, "node_modules")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	policy := newMountPolicy([]string{allowed})

	ext := func(mounts ...types.SandboxMount) *Extension {
		return &Extension{ID: "ext-mount-reject", SandboxConfig: SandboxConfig{Mounts: mounts}}
	}

	t.Run("an allowed mount is emitted", func(t *testing.T) {
		args, _ := buildExtensionCreateArgs(ext(types.SandboxMount{
			HostPath: inside, ContainerPath: "/workspace/node_modules", Mode: "ro",
		}), "kanban-ext-ext-mount-reject", "ubuntu:latest", nil, policy)
		if !hasElem(mountSpecs(args), inside+":/workspace/node_modules:rprivate,ro") {
			t.Errorf("expected the valid mount in %v", mountSpecs(args))
		}
	})

	t.Run("an rw mount keeps its mode", func(t *testing.T) {
		args, _ := buildExtensionCreateArgs(ext(types.SandboxMount{
			HostPath: inside, ContainerPath: "/workspace/node_modules", Mode: "rw",
		}), "kanban-ext-ext-mount-reject", "ubuntu:latest", nil, policy)
		if !hasElem(mountSpecs(args), inside+":/workspace/node_modules:rprivate,rw") {
			t.Errorf("expected the rw mount in %v", mountSpecs(args))
		}
	})

	rejected := map[string][]types.SandboxMount{
		"host path outside the allowlist": {{HostPath: outside, ContainerPath: "/etc-passwd", Mode: "rw"}},
		"host root itself":                {{HostPath: "/", ContainerPath: "/host", Mode: "rw"}},
		"'..' in the host path":           {{HostPath: allowed + "/../etc", ContainerPath: "/etc", Mode: "ro"}},
		"symlink out of the allowlist":    {{HostPath: filepath.Join(link, "secret"), ContainerPath: "/secret", Mode: "ro"}},
		"relative host path":              {{HostPath: "node_modules", ContainerPath: "/nm", Mode: "ro"}},
		"':' in the container path":       {{HostPath: inside, ContainerPath: "/workspace:/host:rprivate,rw", Mode: "ro"}},
		"',' in the container path":       {{HostPath: inside, ContainerPath: "/workspace,rw", Mode: "ro"}},
		"'..' in the container path":      {{HostPath: inside, ContainerPath: "/workspace/../../etc", Mode: "ro"}},
	}
	for name, mounts := range rejected {
		t.Run(name, func(t *testing.T) {
			args, _ := buildExtensionCreateArgs(ext(mounts...), "kanban-ext-ext-mount-reject", "ubuntu:latest", nil, policy)
			if specs := mountSpecs(args); len(specs) != 0 {
				t.Errorf("rejected mount leaked into create args: %v", specs)
			}
		})
	}

	// A mode outside ro/rw can never reach podman either: the spec is assembled
	// from a literal, so an unexpected mode is clamped to read-only (least
	// privilege) instead of being appended verbatim.
	t.Run("an unexpected mount mode is clamped to read-only", func(t *testing.T) {
		args, _ := buildExtensionCreateArgs(ext(types.SandboxMount{
			HostPath: inside, ContainerPath: "/nm", Mode: "rw,noexec",
		}), "kanban-ext-ext-mount-reject", "ubuntu:latest", nil, policy)
		specs := mountSpecs(args)
		if len(specs) != 1 || specs[0] != inside+":/nm:rprivate,ro" {
			t.Errorf("specs = %v, want the mount clamped to rprivate,ro", specs)
		}
	})

	t.Run("one bad mount does not drop the good ones", func(t *testing.T) {
		args, _ := buildExtensionCreateArgs(ext(
			types.SandboxMount{HostPath: "/", ContainerPath: "/host", Mode: "rw"},
			types.SandboxMount{HostPath: inside, ContainerPath: "/workspace/node_modules", Mode: "ro"},
		), "kanban-ext-ext-mount-reject", "ubuntu:latest", nil, policy)
		specs := mountSpecs(args)
		if len(specs) != 1 || specs[0] != inside+":/workspace/node_modules:rprivate,ro" {
			t.Errorf("specs = %v, want only the valid mount", specs)
		}
	})

	t.Run("no policy means no custom mount", func(t *testing.T) {
		args, _ := buildExtensionCreateArgs(ext(types.SandboxMount{
			HostPath: inside, ContainerPath: "/workspace/node_modules", Mode: "ro",
		}), "kanban-ext-ext-mount-reject", "ubuntu:latest", nil, nil)
		if specs := mountSpecs(args); len(specs) != 0 {
			t.Errorf("a nil policy must permit nothing, got %v", specs)
		}
	})
}

// TestBuildExtensionCreateArgs_DeniedCapabilitiesAreSkipped asserts the
// `--cap-drop ALL` posture survives: denied (or malformed) capabilities are
// never re-added, requested-but-harmless ones still are.
func TestBuildExtensionCreateArgs_DeniedCapabilitiesAreSkipped(t *testing.T) {
	for cap := range deniedCapabilities {
		ext := &Extension{ID: "ext-cap-reject", SandboxConfig: SandboxConfig{Capabilities: []string{"CHOWN", cap}}}
		args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-cap-reject", "ubuntu:latest", nil, nil)

		if !hasArgPair(args, "--cap-drop", "ALL") {
			t.Errorf("args lost --cap-drop ALL: %v", args)
		}
		added := capAdds(args)
		if hasElem(added, cap) {
			t.Errorf("--cap-add %s reached the argument list", cap)
		}
		if !hasElem(added, "CHOWN") {
			t.Errorf("harmless capability CHOWN was dropped: %v", added)
		}
		if len(added) != 1 {
			t.Errorf("cap-add list = %v, want only CHOWN", added)
		}
	}

	// The CAP_ prefix and lowercase spellings are the same capability.
	ext := &Extension{ID: "ext-cap-reject", SandboxConfig: SandboxConfig{Capabilities: []string{"cap_sys_admin", "sys_ptrace", "-v"}}}
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-cap-reject", "ubuntu:latest", nil, nil)
	if added := capAdds(args); len(added) != 0 {
		t.Errorf("denied/malformed capabilities reached the argument list: %v", added)
	}
}
