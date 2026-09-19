package stream

// Which projects the branch and filesystem watchers watch follows one rule: a project's
// watchers stop when no live connection holds it. They are shared by every connection
// subscribed to the project, so one connection leaving is not evidence about the others.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/types"
)

// watcherTestManager returns a manager whose project watchers are running, backed by a
// store holding one project per name, each with a real directory to watch. It skips where
// fsnotify cannot take a watch, which is what the watcher tests do too.
func watcherTestManager(t *testing.T, projectIDs ...string) *AppStreamManager {
	t.Helper()

	s, err := store.NewStore(filepath.Join(t.TempDir(), "subscriptions.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, id := range projectIDs {
		root := filepath.Join(t.TempDir(), id)
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", root, err)
		}
		writeTestFile(t, root, filepath.Join(".git", "HEAD"), "ref: refs/heads/main\n")
		if err := s.CreateProject(&types.Project{ID: id, Name: id, Path: root}); err != nil {
			t.Fatalf("create project %s: %v", id, err)
		}
	}

	// No worktree manager: a subscription then starts no git work, only the watchers.
	asm := NewAppStreamManager(s, nil)
	t.Cleanup(asm.Close)
	if asm.branchWatcher == nil || asm.projectWatcher == nil {
		t.Skip("the manager's project watchers are unavailable")
	}

	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(probe, 0o755); err != nil {
		t.Fatalf("mkdir probe: %v", err)
	}
	if err := asm.projectWatcher.Watch("inotify-probe", probe); err != nil {
		t.Skipf("cannot take a watch: %v", err)
	}
	asm.projectWatcher.Unwatch("inotify-probe")

	return asm
}

// watchersHolding reports which of a project's watchers hold it. A watcher's own project
// map is what Unwatch deletes, so it is the record of watched-ness.
func watchersHolding(asm *AppStreamManager, projectID string) (branch, files bool) {
	asm.branchWatcher.mu.Lock()
	_, branch = asm.branchWatcher.projects[projectID]
	asm.branchWatcher.mu.Unlock()
	asm.projectWatcher.mu.Lock()
	_, files = asm.projectWatcher.projects[projectID]
	asm.projectWatcher.mu.Unlock()
	return branch, files
}

// awaitWatched waits until both of a project's watchers agree with want.
func awaitWatched(t *testing.T, asm *AppStreamManager, projectID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		branch, files := watchersHolding(asm, projectID)
		if branch == want && files == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("project %s: branch watcher holds=%v fs watcher holds=%v, want both %v", projectID, branch, files, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertSubscriberCount asserts the rule the watchers are keyed on: live connections hold
// the project, or none of them does.
func assertSubscriberCount(t *testing.T, asm *AppStreamManager, projectID string, want bool) {
	t.Helper()
	if got := asm.hasProjectSubscribers(projectID); got != want {
		t.Fatalf("hasProjectSubscribers(%s) = %v, want %v", projectID, got, want)
	}
}

// warmProbe buffers a task event no client is subscribed to yet, so subscribing to that
// task replays it — the arrival of the replay proves the server applied every frame this
// connection sent before the subscribe, which is what makes a negative assertion about an
// unsubscribe mean something.
func warmProbe(t *testing.T, asm *AppStreamManager, projectID, taskID string) {
	t.Helper()
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{
		Type: types.WSTypeToken,
		Data: map[string]string{"text": "warm-" + taskID},
	})
}

// subscribeProven subscribes the client to a project and waits until the server has
// applied that subscription.
func subscribeProven(t *testing.T, fs *frameStream, projectID, probeTaskID string) {
	t.Helper()
	fs.sendSubscription(t, "subscribe_project", projectID, "")
	fs.sendSubscription(t, "subscribe_task", projectID, probeTaskID)
	fs.await(t, 5*time.Second, isTaskEventFor(probeTaskID, "warm-"+probeTaskID), 1,
		"project subscription applied for "+projectID)
}

// TestLeavingOneProjectOfManyKeepsTheSharedWatchersAlive: two connections on one project.
// The second one leaving stops neither connection's events, and the last one leaving stops
// them for everyone.
func TestLeavingOneProjectOfManyKeepsTheSharedWatchersAlive(t *testing.T) {
	const projectID = "proj-shared"
	asm := watcherTestManager(t, projectID)
	wsURL := serveStream(t, asm)

	warmProbe(t, asm, projectID, "probe-a")
	warmProbe(t, asm, projectID, "probe-b")

	a := dialFrameStream(t, wsURL)
	subscribeProven(t, a, projectID, "probe-a")
	b := dialFrameStream(t, wsURL)
	subscribeProven(t, b, projectID, "probe-b")
	awaitWatched(t, asm, projectID, true)

	b.sendSubscription(t, "unsubscribe_project", projectID, "")
	b.flushSubscriptions(t, projectID, "probe-b") // applied: it preceded this subscribe

	assertSubscriberCount(t, asm, projectID, true)
	awaitWatched(t, asm, projectID, true)

	a.sendSubscription(t, "unsubscribe_project", projectID, "")
	a.flushSubscriptions(t, projectID, "probe-a")

	assertSubscriberCount(t, asm, projectID, false)
	awaitWatched(t, asm, projectID, false)
}

// TestUnsubscribingOneProjectStopsOnlyThatProjectsWatchers: a connection holding two
// projects leaves one of them, which is not its last subscription.
func TestUnsubscribingOneProjectStopsOnlyThatProjectsWatchers(t *testing.T) {
	const held, left = "proj-held", "proj-left"
	asm := watcherTestManager(t, held, left)
	wsURL := serveStream(t, asm)

	warmProbe(t, asm, left, "probe-left")
	warmProbe(t, asm, held, "probe-held")

	a := dialFrameStream(t, wsURL)
	subscribeProven(t, a, left, "probe-left")
	a.sendSubscription(t, "subscribe_project", held, "")

	// B is the connection that takes held's watchers, so its watched-ness is assertable
	// whichever order the two subscriptions land in.
	b := dialFrameStream(t, wsURL)
	subscribeProven(t, b, held, "probe-held")

	awaitWatched(t, asm, left, true)
	awaitWatched(t, asm, held, true)

	a.sendSubscription(t, "unsubscribe_project", left, "")
	a.flushSubscriptions(t, left, "probe-left")

	assertSubscriberCount(t, asm, left, false)
	awaitWatched(t, asm, left, false)
	assertSubscriberCount(t, asm, held, true)
	awaitWatched(t, asm, held, true)
}
