package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kanban/backend/types"
	"kanban/pkg/sandboxruntime"
)

// TestResolveProjectImage pins the one image rule every sandbox start shares.
func TestResolveProjectImage(t *testing.T) {
	tests := []struct {
		name         string
		project      *types.Project
		wantImage    string
		wantSource   string
		wantDockerfi string
	}{
		{
			name:       "nil project falls back to the base image",
			project:    nil,
			wantImage:  BaseImage,
			wantSource: ImageSourceDefault,
		},
		{
			name:       "no configuration uses the base image",
			project:    &types.Project{Name: "plain"},
			wantImage:  BaseImage,
			wantSource: ImageSourceDefault,
		},
		{
			name: "configured image wins over the default",
			project: &types.Project{
				Name:         "img-proj",
				SandboxImage: "docker.io/library/ubuntu:24.04",
			},
			wantImage:  "docker.io/library/ubuntu:24.04",
			wantSource: ImageSourceConfigured,
		},
		{
			// A Dockerfile wins over a configured image — the precedence callers already used.
			name: "dockerfile takes precedence",
			project: &types.Project{
				Name:                  "getmike.dev",
				SandboxImage:          "docker.io/library/ubuntu:24.04",
				SandboxDockerfilePath: "/srv/Dockerfile",
			},
			wantImage:    "localhost/sandbox-getmike.dev",
			wantSource:   ImageSourceDockerfile,
			wantDockerfi: "/srv/Dockerfile",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveProjectImage(tc.project)
			if got.Image != tc.wantImage {
				t.Errorf("Image = %q, want %q", got.Image, tc.wantImage)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.Dockerfile != tc.wantDockerfi {
				t.Errorf("Dockerfile = %q, want %q", got.Dockerfile, tc.wantDockerfi)
			}
			if got.NeedsBuild() != (tc.wantSource == ImageSourceDockerfile) {
				t.Errorf("NeedsBuild() = %v, want %v", got.NeedsBuild(), tc.wantSource == ImageSourceDockerfile)
			}
			// Whatever the source, the result must not be a short name.
			if !sandboxruntime.IsQualifiedImageRef(got.Image) && got.Source != ImageSourceConfigured {
				t.Errorf("Image %q is not fully qualified", got.Image)
			}
		})
	}
}

// TestDoOnceJoinersShareTheInFlightResult: concurrent callers share one build instead of each
// starting their own. The join is deterministic — doOnce registers the key before calling fn, so a
// caller that arrives after fn started must find the key and wait on it.
func TestDoOnceJoinersShareTheInFlightResult(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}

	const key = "localhost/sandbox-getmike.dev"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	runs := 0
	fn := func() error {
		mu.Lock()
		runs++
		mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return fmt.Errorf("boom")
	}

	first := make(chan error, 1)
	go func() { first <- sm.doOnce(key, fn) }()
	<-started // fn is running, so the key is registered

	// Five more callers arrive while it is still running.
	var wg sync.WaitGroup
	joined := make([]error, 5)
	for i := range joined {
		wg.Add(1)
		go func(i int) { defer wg.Done(); joined[i] = sm.doOnce(key, fn) }(i)
	}
	// Give the joiners a moment to reach the map, then let the build finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	err1 := <-first
	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Fatalf("prepare ran %d times, want 1 — concurrent callers must share one build", runs)
	}
	if err1 == nil || !strings.Contains(err1.Error(), "boom") {
		t.Errorf("the caller that ran the build got %v, want its own error", err1)
	}
	for i, err := range joined {
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("joiner %d got %v, want the in-flight call's error", i, err)
		}
	}
}

// Nothing is cached once the call returns: a failed build has to be retryable.
func TestDoOnceAllowsRetryAfterFailure(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}

	runs := 0
	failing := func() error { runs++; return fmt.Errorf("boom") }
	if err := sm.doOnce("img", failing); err == nil {
		t.Fatal("first doOnce = nil, want the failure")
	}
	if err := sm.doOnce("img", failing); err == nil {
		t.Fatal("second doOnce = nil, want the failure again (nothing may be cached)")
	}
	if runs != 2 {
		t.Fatalf("prepare ran %d times, want 2", runs)
	}
	if err := sm.doOnce("img", func() error { return nil }); err != nil {
		t.Errorf("doOnce after success = %v, want nil", err)
	}
	if _, ok := sm.imageOps["img"]; ok {
		t.Error("the key is still marked in flight after the call completed")
	}
}

// An unreadable Dockerfile is a configuration mistake, named as one before any build starts.
func TestEnsureImageForProjectUnreadableDockerfile(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	missing := filepath.Join(t.TempDir(), "Dockerfile")

	_, err := sm.EnsureImageForProject(&types.Project{
		Name:                  "getmike.dev",
		SandboxDockerfilePath: missing,
	}, nil)
	if err == nil {
		t.Fatal("expected an error for an unreadable Dockerfile")
	}
	if !strings.Contains(err.Error(), "Dockerfile") || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("error = %q, want it to name the unreadable Dockerfile", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %q, want it to name the path %q", err, missing)
	}
}

// TestEnsureImageForProjectBuildsProjectImage is the reported bug end to end: a Dockerfile-backed
// project touched by a caller that used to assume the image existed.
func TestEnsureImageForProjectBuildsProjectImage(t *testing.T) {
	sm := NewSandboxManager(sandboxruntime.Boot{})
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not installed, skipping integration test")
	}

	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM docker.io/library/alpine:latest\nRUN echo prepared\n"), 0o644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}
	project := &types.Project{Name: "ensure-image-proj", SandboxDockerfilePath: dockerfile}
	image := ProjectImageName(project.Name)
	t.Cleanup(func() { _, _ = sm.podmanExec("rmi", image) })

	if sm.ImageReady(image) {
		_, _ = sm.podmanExec("rmi", image)
	}

	var phases []string
	var lines int
	got, err := sm.EnsureImageForProject(project, func(status types.SandboxImageStatus) {
		if status.Image != image {
			t.Errorf("progress reported image %q, want %q", status.Image, image)
		}
		phases = append(phases, status.Phase)
		if status.Line != "" {
			lines++
		}
	})
	if err != nil {
		t.Fatalf("EnsureImageForProject: %v", err)
	}
	if got != image {
		t.Fatalf("image = %q, want %q", got, image)
	}
	if !sm.ImageReady(image) {
		t.Fatalf("image %s was not made available", image)
	}
	// The browser needs the phase markers and the build output itself.
	if len(phases) == 0 || phases[0] != types.SandboxImagePhaseBuilding {
		t.Fatalf("phases = %v, want the first to be %q", phases, types.SandboxImagePhaseBuilding)
	}
	if phases[len(phases)-1] != types.SandboxImagePhaseDone {
		t.Fatalf("phases = %v, want the last to be %q", phases, types.SandboxImagePhaseDone)
	}
	if lines == 0 {
		t.Error("no build output was reported through onProgress")
	}

	// A second call is a no-op: the image exists, so nothing is announced and nothing is rebuilt.
	phases = nil
	if _, err := sm.EnsureImageForProject(project, func(status types.SandboxImageStatus) {
		phases = append(phases, status.Phase)
	}); err != nil {
		t.Fatalf("second EnsureImageForProject: %v", err)
	}
	if len(phases) != 0 {
		t.Errorf("an already-ready image reported %v, want no progress events", phases)
	}
}
