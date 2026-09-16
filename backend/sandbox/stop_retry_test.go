package sandbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mike/pkg/bridge"
	"mike/pkg/sandboxruntime"
)

// A container that survives `podman rm -f` has to stay in the active map: Stop only looks at tasks in
// that map, and the startup sweep skips containers that are running, so a container missing from the
// map is never removed.

// failingPodman installs a `podman` that fails `rm` while the marker file exists and succeeds at
// everything else; removing the marker is how the test lets the container go.
func failingPodman(t *testing.T) (failMarker string) {
	t.Helper()
	dir := t.TempDir()
	failMarker = filepath.Join(dir, "refuse-rm")
	if err := os.WriteFile(failMarker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\nif [ \"$1\" = \"rm\" ] && [ -e \"" + failMarker + "\" ]; then exit 1; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return failMarker
}

func trackedSandbox(taskID string) *SandboxManager {
	return &SandboxManager{
		rt:            sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
		active:        map[string]string{taskID: ContainerNamePrefix + "-" + taskID},
		mountPaths:    map[string]string{},
		agents:        map[string]*agentConn{},
		clients:       map[string]*bridge.Client{},
		livenessCache: map[string]time.Time{},
		mem:           &fakeMem{},
	}
}

func TestStopKeepsTrackingAContainerThatRefusedRemoval(t *testing.T) {
	failMarker := failingPodman(t)
	const taskID = "task-keeps-running"
	sm := trackedSandbox(taskID)

	if err := sm.Stop(taskID); err == nil {
		t.Fatal("a container that could not be removed was reported as stopped")
	}
	name, tracked := sm.active[taskID]
	if !tracked {
		t.Fatal("the container was untracked by a stop that removed nothing; nothing will ever remove it now")
	}
	if name != ContainerNamePrefix+"-"+taskID {
		t.Errorf("tracking entry = %q, want the container that is still up", name)
	}

	if err := os.Remove(failMarker); err != nil {
		t.Fatal(err)
	}
	if err := sm.Stop(taskID); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if _, tracked := sm.active[taskID]; tracked {
		t.Error("the container is still tracked after it was removed")
	}
}
