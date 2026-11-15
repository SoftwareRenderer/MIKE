package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/types"
	"mike/pkg/sandboxruntime"
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

// TestImageOpJoinersShareTheInFlightResult: concurrent callers share one build instead of each starting their
// own. claimImageOp registers the key before any work runs, so a caller arriving mid-build finds it and waits.
func TestImageOpJoinersShareTheInFlightResult(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}

	const key = "localhost/sandbox-getmike.dev"
	op, claimed := sm.claimImageOp(key, nil)
	if !claimed {
		t.Fatal("the first caller could not claim an idle key")
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	runs := 0
	boom := fmt.Errorf("boom")
	work := func() (*BuildResult, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return nil, boom
	}

	first := make(chan error, 1)
	go func() {
		_, err := sm.runImageOp(key, op, work)
		first <- err
	}()
	<-started // the work is running, so the key is registered

	// Five more callers arrive while it is still running.
	var wg sync.WaitGroup
	joined := make([]error, 5)
	for i := range joined {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			waiter, ok := sm.joinImageOp(key, nil)
			if !ok {
				joined[i] = fmt.Errorf("nothing was in flight to join")
				return
			}
			_, joined[i] = waiter.await()
		}(i)
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
	if err1 != boom {
		t.Errorf("the caller that ran the build got %v, want its own error", err1)
	}
	for i, err := range joined {
		if err != boom {
			t.Errorf("joiner %d got %v, want the in-flight call's error", i, err)
		}
	}
}

// Nothing is cached once a preparation returns: a failed build has to be retryable, and no key may stay
// claimed behind it.
func TestFinishedImageOpLeavesNothingInFlight(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}

	const key = "localhost/sandbox-getmike.dev"
	op, _ := sm.claimImageOp(key, nil)
	if second, ok := sm.claimImageOp(key, nil); ok {
		sm.finishImageOp(key, second, nil, nil)
		t.Fatal("a second caller claimed a key that was already in flight")
	}
	sm.finishImageOp(key, op, nil, fmt.Errorf("boom"))

	if _, joined := sm.joinImageOp(key, nil); joined {
		t.Error("a finished preparation is still joinable")
	}
	if _, ok := sm.imageOps[key]; ok {
		t.Error("the key is still marked in flight after the call completed")
	}
	retry, claimed := sm.claimImageOp(key, nil)
	if !claimed {
		t.Fatal("a failed preparation cannot be retried")
	}
	sm.finishImageOp(key, retry, nil, nil)
}

// A panicked preparation must not leave its key in flight for the rest of the process, and must hand whoever
// waited on it an answer rather than a permanent wait.
func TestRunImageOpReleasesTheKeyOnAPanic(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}

	const key = "localhost/sandbox-getmike.dev"
	op, _ := sm.claimImageOp(key, nil)
	waiter := make(chan error, 1)
	go func() {
		ready, ok := sm.joinImageOp(key, nil)
		if !ok {
			waiter <- fmt.Errorf("the panicked preparation was already gone")
			return
		}
		_, err := ready.await()
		waiter <- err
	}()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("the panic did not reach the caller")
			}
			if _, joined := sm.joinImageOp(key, nil); joined {
				t.Error("the panicked preparation left its key in flight")
			}
			if err := <-waiter; err == nil {
				t.Error("the waiter was released with no answer")
			}
		}()
		sm.runImageOp(key, op, func() (*BuildResult, error) { panic("boom") })
	}()
}

// dockerfileIn writes a project Dockerfile where EnsureImageForProject can read it.
func dockerfileIn(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(path, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestEnsureImageForProjectWaitsForTheImagePreparationAlreadyRunning: a caller joins the build in flight
// before asking the host whether the image exists, because a rebuild leaves the image it replaces in place.
// gVisor is not available to this manager, so a caller that did not wait would have answered that.
func TestEnsureImageForProjectWaitsForTheImagePreparationAlreadyRunning(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}
	image := ProjectImageName(project.Name)

	op, _ := sm.claimImageOp(image, nil)
	op.announce(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding})

	phase := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		_, err := sm.EnsureImageForProject(project, func(status types.SandboxImageStatus) {
			phase <- status.Phase
		})
		done <- err
	}()

	// It said what it is waiting for, and it is still waiting.
	if got := <-phase; got != types.SandboxImagePhaseBuilding {
		t.Fatalf("first phase = %q, want %q", got, types.SandboxImagePhaseBuilding)
	}
	select {
	case err := <-done:
		t.Fatalf("the caller returned %v while the preparation was still in flight", err)
	case <-time.After(50 * time.Millisecond):
	}

	boom := fmt.Errorf("boom")
	sm.finishImageOp(image, op, nil, boom)
	if err := <-done; err != boom {
		t.Errorf("error = %v, want the waiting build's own error", err)
	}
	if got := <-phase; got != types.SandboxImagePhaseFailed {
		t.Errorf("last phase = %q, want %q", got, types.SandboxImagePhaseFailed)
	}
}

// TestBuildProjectImageJoinsTheBuildAlreadyRunning: a build asked for while one runs reads that build's own
// lines and takes its result instead of building the same image twice. gVisor is not available to this
// manager, so a caller that started its own build would have said so.
func TestBuildProjectImageJoinsTheBuildAlreadyRunning(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}
	image := ProjectImageName(project.Name)

	op, _ := sm.claimImageOp(image, nil)

	lines := make(chan string, 8)
	done := make(chan *BuildResult, 1)
	go func() {
		result, err := sm.BuildProjectImage(context.Background(), project, func(line string) { lines <- line })
		if err != nil {
			t.Errorf("BuildProjectImage: %v", err)
		}
		done <- result
	}()

	waited := <-lines
	want := &BuildResult{Image: image, Commit: "abc"}
	for _, line := range []string{"step 1", "step 2"} {
		op.progress(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding, Line: line})
	}
	sm.finishImageOp(image, op, want, nil)

	if got := <-done; got != want {
		t.Errorf("result = %+v, want the in-flight build's %+v", got, want)
	}
	close(lines)
	var seen []string
	for line := range lines {
		seen = append(seen, line)
	}
	if !strings.Contains(waited, "waiting") {
		t.Errorf("the first line %q never said the caller is waiting on a build", waited)
	}
	if len(seen) != 2 || seen[0] != "step 1" || seen[1] != "step 2" {
		t.Errorf("lines = %q, want the running build's own lines", seen)
	}
	if idle, ok := sm.claimImageOp(image, nil); !ok {
		t.Error("the joining request left the image claimed")
	} else {
		sm.finishImageOp(image, idle, nil, nil)
	}
}

// TestMakeProjectImageAnnouncesThePhaseItWorksOn: the phase marker, a status carrying no line, is what a task
// card's state and a terminal's first line are built from, so the worker emits it and a late joiner hears it.
// gVisor is not available to this manager, so the work is refused just after announcing.
func TestMakeProjectImageAnnouncesThePhaseItWorksOn(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}
	image := ProjectImageName(project.Name)

	var mine []types.SandboxImageStatus
	op, _ := sm.claimImageOp(image, func(status types.SandboxImageStatus) { mine = append(mine, status) })
	if _, err := sm.makeProjectImage(op, image, ResolveProjectImage(project), project, types.SandboxImagePhaseBuilding); err == nil {
		t.Fatal("the work succeeded on a host with no gVisor")
	}
	if len(mine) == 0 {
		t.Fatal("the work announced nothing, so the card and the terminal would show no state")
	}
	if mine[0].Phase != types.SandboxImagePhaseBuilding || mine[0].Line != "" {
		t.Errorf("first event = %+v, want the %q phase carrying no line", mine[0], types.SandboxImagePhaseBuilding)
	}

	late := make(chan types.SandboxImageStatus, 1)
	if _, joined := sm.joinImageOp(image, func(status types.SandboxImageStatus) { late <- status }); !joined {
		t.Fatal("nothing was in flight to join")
	}
	if got := <-late; got.Phase != types.SandboxImagePhaseBuilding || got.Line != "" {
		t.Errorf("the late joiner's first event = %+v, want the phase already announced", got)
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

// TestEnsureImageForProjectBuildsProjectImage: a Dockerfile-backed project gets its image built, in the
// sandbox, from its commit, before anything runs in it — and the second call finds it already there.
func TestEnsureImageForProjectBuildsProjectImage(t *testing.T) {
	requireBuilder(t)
	sm := NewSandboxManager(bootWithBridge(t))
	useScratchRoot(t)

	repo := testRepo(t, map[string]string{
		"Dockerfile": "FROM scratch\nCOPY prepared /prepared\n",
		"prepared":   "prepared\n",
	})
	project := &types.Project{Name: "ensure-image-proj", Path: repo, SandboxDockerfilePath: filepath.Join(repo, "Dockerfile")}
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

	// A second call is a no-op: the image exists, so nothing is announced and nothing is rebuilt. The cache
	// key the build stamped on the image is what answers "already the one you want" without staging a byte.
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

// TestWatchImagePreparationAnswersNothingWhenNothingRuns: the sandbox dialog asks this when it opens, and a
// project with no build running has to be answered as idle rather than with a log that never fills.
func TestWatchImagePreparationAnswersNothingWhenNothingRuns(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}

	statuses, watching := sm.WatchImagePreparation(context.Background(), project)
	if watching {
		t.Fatal("a manager with nothing in flight claimed a viewer was watching a build")
	}
	if statuses != nil {
		t.Error("a viewer that joined nothing was handed a queue")
	}
}

// TestWatchImagePreparationCarriesTheRunningBuildToALateViewer: the phase the preparation announced before the
// viewer arrived comes first, then every line after it, then how it ended, and the queue closes.
func TestWatchImagePreparationCarriesTheRunningBuildToALateViewer(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}
	image := ProjectImageName(project.Name)

	op, _ := sm.claimImageOp(image, nil)
	op.announce(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding})

	statuses, watching := sm.WatchImagePreparation(context.Background(), project)
	if !watching {
		t.Fatal("a running preparation was not watchable")
	}
	op.progress(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding, Line: "step 1/3"})
	sm.finishImageOp(image, op, &BuildResult{Image: image}, nil)

	var got []types.SandboxImageStatus
	for status := range statuses {
		got = append(got, status)
	}
	if len(got) != 3 {
		t.Fatalf("frames = %+v, want the phase, the line and the ending", got)
	}
	if got[0].Phase != types.SandboxImagePhaseBuilding || got[0].Line != "" {
		t.Errorf("first frame = %+v, want the announced phase with no line", got[0])
	}
	if got[1].Line != "step 1/3" {
		t.Errorf("second frame = %+v, want the build's own line", got[1])
	}
	if got[2].Phase != types.SandboxImagePhaseDone || got[2].Image != image {
		t.Errorf("last frame = %+v, want %q for %s", got[2], types.SandboxImagePhaseDone, image)
	}
}

// TestWatchImagePreparationLeavesTheBuildAloneWhenItsViewerGoes: closing a dialog stops the watching and not the
// build. The preparation keeps its key and its starter, and still answers the callers waiting on it.
func TestWatchImagePreparationLeavesTheBuildAloneWhenItsViewerGoes(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}
	image := ProjectImageName(project.Name)

	op, _ := sm.claimImageOp(image, nil)
	op.announce(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding})

	ctx, leave := context.WithCancel(context.Background())
	statuses, watching := sm.WatchImagePreparation(ctx, project)
	if !watching {
		t.Fatal("a running preparation was not watchable")
	}
	leave()
	for closed := false; !closed; {
		select {
		case _, open := <-statuses:
			closed = !open
		case <-time.After(2 * time.Second):
			t.Fatal("the watcher's queue stayed open after its caller left")
		}
	}

	waiter, joined := sm.joinImageOp(image, nil)
	if !joined {
		t.Fatal("closing the watcher retired the in-flight preparation")
	}
	sm.finishImageOp(image, op, nil, fmt.Errorf("boom"))
	if _, err := waiter.await(); err == nil {
		t.Error("the preparation's own starter was released with no answer")
	}
}

// TestCancelImagePreparationStopsTheBuildAndReportsItCancelled: a person's Cancel button reaches the caller that
// started the build, and a watcher reads a cancellation instead of a fault.
func TestCancelImagePreparationStopsTheBuildAndReportsItCancelled(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}
	image := ProjectImageName(project.Name)

	// The caller that holds the key. The test bounds it the way buildProjectImageDirect bounds a build: one
	// context recorded on the preparation, and ending that context ends the work.
	work, stopWork := context.WithCancel(context.Background())
	op, _ := sm.claimImageOp(image, nil)
	op.setCancel(stopWork)
	starter := make(chan error, 1)
	go func() {
		<-work.Done()
		sm.finishImageOp(image, op, nil, work.Err())
		starter <- work.Err()
	}()

	statuses, watching := sm.WatchImagePreparation(context.Background(), project)
	if !watching {
		t.Fatal("a running preparation was not watchable")
	}
	if !sm.CancelImagePreparation(project) {
		t.Fatal("the cancel did not reach the running preparation")
	}
	if err := <-starter; err == nil {
		t.Error("the caller that started the build was never told it stopped")
	}

	var last types.SandboxImageStatus
	for status := range statuses {
		last = status
	}
	if last.Phase != types.SandboxImagePhaseCancelled {
		t.Errorf("the watcher's last frame = %+v, want %q", last, types.SandboxImagePhaseCancelled)
	}
	if _, joined := sm.joinImageOp(image, nil); joined {
		t.Error("the cancelled preparation left its key claimed, so no later build could run")
	}
	if sm.CancelImagePreparation(project) {
		t.Error("a cancel with nothing in flight reported stopping something")
	}
}

// A viewer that cannot keep up loses lines. It must not be the reason a build stops printing.
func TestWatchImagePreparationDropsRatherThanStallingTheBuild(t *testing.T) {
	sm := &SandboxManager{rt: sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}), imageOps: make(map[string]*imageOp)}
	project := &types.Project{Name: "getmike.dev", SandboxDockerfilePath: dockerfileIn(t)}
	image := ProjectImageName(project.Name)

	op, _ := sm.claimImageOp(image, nil)
	if _, watching := sm.WatchImagePreparation(context.Background(), project); !watching {
		t.Fatal("a running preparation was not watchable")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < watchQueueLines*3; i++ {
			op.progress(types.SandboxImageStatus{Phase: types.SandboxImagePhaseBuilding, Line: fmt.Sprintf("line %d", i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an unread watcher stalled the build emitting into it")
	}
	sm.finishImageOp(image, op, nil, nil)
}
