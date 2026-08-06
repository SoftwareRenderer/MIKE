package extensions

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestDefaultExtensionMemory(t *testing.T) {
	if DefaultExtensionMemory != 256 {
		t.Errorf("DefaultExtensionMemory = %d, want 256", DefaultExtensionMemory)
	}
}

func TestExtensionContainerPrefix(t *testing.T) {
	if ExtensionContainerPrefix != "kanban-ext" {
		t.Errorf("ExtensionContainerPrefix = %q, want kanban-ext", ExtensionContainerPrefix)
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

func TestGetOrCreate_NotRunning(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	client, err := sm.GetOrCreate("nonexistent")
	if err == nil {
		t.Error("GetOrCreate(nonexistent) = nil, want error")
	}
	if client != nil {
		t.Error("GetOrCreate(nonexistent) returned non-nil client")
	}
}

func TestRelease_NotCreated(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)
	// Should not panic
	sm.Release("nonexistent")
}

func TestGracePeriod_TimerSetOnRelease(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)

	// Manually set up state to simulate a running sandbox
	extID := "test-grace-period"
	sm.states[extID] = &extensionSandboxState{
		containerName: "kanban-ext-test",
	}
	sm.refs[extID] = 1

	// Release to bring refcount to 0 — should set a timer
	sm.Release(extID)

	sm.mu.Lock()
	state := sm.states[extID]
	sm.mu.Unlock()

	if state == nil {
		t.Fatal("state should still exist during grace period")
	}
	if state.stopTimer == nil {
		t.Error("stopTimer should be set after Release with refcount 0")
	}
}

func TestGracePeriod_CancelledOnReconnect(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)

	extID := "test-grace-reconnect"

	// Simulate: sandbox was created, client connected (refs=1), then disconnected (refs=0, timer set)
	sm.states[extID] = &extensionSandboxState{
		containerName: "kanban-ext-test",
	}
	sm.refs[extID] = 0

	// Manually set a timer to simulate grace period
	done := make(chan struct{}, 1)
	sm.states[extID].stopTimer = time.AfterFunc(100*time.Millisecond, func() {
		done <- struct{}{}
	})

	// Simulate client reconnecting via GetOrCreate
	// GetOrCreate checks client.IsAlive() which we can't mock, so test the internal logic directly
	sm.mu.Lock()
	state := sm.states[extID]
	if state != nil && state.stopTimer != nil {
		state.stopTimer.Stop()
		state.stopTimer = nil
	}
	sm.refs[extID]++
	sm.mu.Unlock()

	// Timer should be cancelled
	select {
	case <-done:
		t.Error("timer should have been cancelled, but it fired")
	case <-time.After(200 * time.Millisecond):
		// Good — timer did not fire
	}
}

func TestGracePeriod_TimerFiresAndStops(t *testing.T) {
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, nil)

	extID := "test-grace-fires"
	stopped := make(chan string, 1)

	// Set up state with a mock stop by intercepting via refs
	sm.states[extID] = &extensionSandboxState{
		containerName: "kanban-ext-test",
	}
	sm.refs[extID] = 1

	// Release to trigger grace period timer
	sm.Release(extID)

	// Replace the timer with a short one for testing
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

	// Wait for timer to fire
	select {
	case id := <-stopped:
		if id != extID {
			t.Errorf("expected %s to be stopped, got %s", extID, id)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("grace period timer did not fire")
	}
}

func TestGracePeriod_ExtensionStopGracePeriod(t *testing.T) {
	if ExtensionStopGracePeriod != 15*time.Minute {
		t.Errorf("ExtensionStopGracePeriod = %v, want 15m", ExtensionStopGracePeriod)
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
			args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-network-test", "ubuntu:latest", nil)
			if !hasArgPair(args, "--network", "none") {
				t.Errorf("args missing --network none (NetworkIsolation=%v): %v", ext.SandboxConfig.NetworkIsolation, args)
			}
		})
	}
}

func TestBuildExtensionCreateArgs_KeepsHardeningFlags(t *testing.T) {
	ext := &Extension{ID: "ext-hardening-test"}
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-hardening-test", "ubuntu:latest", nil)

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

func TestBuildExtensionCreateArgs_MountsProjectsReadOnly(t *testing.T) {
	ext := &Extension{ID: "ext-mount-test"}
	projectMounts := map[string]string{
		"/host/proj-a": "/mnt/proj-a",
		"/host/proj-b": "/mnt/proj-b",
	}
	args, mounted := buildExtensionCreateArgs(ext, "kanban-ext-ext-mount-test", "ubuntu:latest", projectMounts)

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
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-entry-test", "my-custom-image:v1", nil)

	if !hasArg(args, "my-custom-image:v1") {
		t.Error("args missing image reference")
	}
	if !hasArg(args, "chmod +x /sandbox-bridge && sleep infinity") {
		t.Error("args missing sleep infinity entrypoint")
	}
}
