package extensions

import (
	"testing"
	"time"
)

func TestSanitizeContainerID(t *testing.T) {
	tests := []struct {
		input    string
		want     string
		wantErr  bool
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
			got, err := sanitizeContainerID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("sanitizeContainerID(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("sanitizeContainerID(%q) = %q, want %q", tt.input, got, tt.want)
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
