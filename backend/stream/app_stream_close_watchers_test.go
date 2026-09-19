package stream

// The "websocket watchers" shutdown stage runs AppStreamManager.Close and nothing else, so
// Close is where every watcher the manager owns has to stop.

import (
	"sync"
	"testing"
	"time"

	"mike/backend/types"
)

// isFsEventFor reports a frame predicate for the fs_event naming one project-relative path.
func isFsEventFor(path string) func(map[string]any) bool {
	return func(frame map[string]any) bool {
		return frame["type"] == string(types.WSTypeFsEvent) && frame["path"] == path
	}
}

// TestCloseStopsTheProjectWatcher: a project watcher that survives Close keeps its watches and
// keeps writing fs_event frames to the clients of the project it watches.
func TestCloseStopsTheProjectWatcher(t *testing.T) {
	const projectID = "proj-close"
	asm := newWatcherTestManager(t, projectID)
	wsURL := serveStream(t, asm)

	// One Close serves the test and its teardown: WorktreeWatcher.Close panics the second time.
	var once sync.Once
	shutdown := func() { once.Do(asm.Close) }
	t.Cleanup(shutdown)

	project, err := asm.store.GetProject(projectID)
	if err != nil || project == nil {
		t.Fatalf("project %s: %v", projectID, err)
	}

	warmProbe(t, asm, projectID, "probe-close")
	client := dialFrameStream(t, wsURL)
	subscribeProven(t, client, projectID, "probe-close")
	awaitWatched(t, asm, projectID, true)

	// Delivery works here, so the silence measured after Close means something.
	writeTestFile(t, project.Path, "before-close.txt", "before\n")
	client.await(t, 5*time.Second, isFsEventFor("before-close.txt"), 1, "fs_event for a watched project")
	// A project reports one event per debounce window, so the write above can still have a
	// later frame on its way; drain it before the quiet window starts.
	client.collect(1200*time.Millisecond, isType(string(types.WSTypeFsEvent)), 64)

	shutdown()

	writeTestFile(t, project.Path, "after-close.txt", "after\n")
	client.quietWindow(t, 3*time.Second, isFsEventFor("after-close.txt"), "fs_event after Close")
}
