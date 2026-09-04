package stream

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kanban/backend/fsutil"
	"kanban/backend/git"
)

type fsEvent struct{ op, path string }

// watcherRecorder runs a ProjectWatcher over dir and reports what it would send.
func watcherRecorder(t *testing.T, dir string) chan fsEvent {
	t.Helper()
	events := make(chan fsEvent, 64)
	pw, err := NewProjectWatcher(func(_, _, op, path string) {
		events <- fsEvent{op, path}
	})
	if err != nil {
		t.Skipf("fsnotify unavailable: %v", err)
	}
	t.Cleanup(pw.Close)
	if err := pw.Watch("proj", dir); err != nil {
		t.Fatal(err)
	}
	go pw.StartEventLoop()
	// Let the watch settle before anything is measured.
	time.Sleep(800 * time.Millisecond)
	drainFsEvents(events)
	return events
}

// collect gathers events until nothing new arrives for longer than the debounce,
// or the deadline passes.
func collect(t *testing.T, events chan fsEvent) []fsEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	idle := time.After(1200 * time.Millisecond)
	var seen []fsEvent
	for {
		select {
		case e := <-events:
			seen = append(seen, e)
			idle = time.After(1200 * time.Millisecond)
		case <-idle:
			return seen
		case <-deadline:
			return seen
		}
	}
}

// TestProjectWatcherIgnoresAtomicTempFiles covers the window the filter exists for:
// a watcher reports one event per project per 500 ms, so while the temporary file
// exists its create/delete can be what gets broadcast — the browser would be told
// to create a file that has already been renamed away, then to delete it.
func TestProjectWatcherIgnoresAtomicTempFiles(t *testing.T) {
	dir := t.TempDir()
	events := watcherRecorder(t, dir)

	temp := filepath.Join(dir, fsutil.AtomicTempPrefix+"notes.txt-4028137")
	if err := os.WriteFile(temp, []byte("half-written"), 0644); err != nil {
		t.Fatal(err)
	}
	// Longer than the debounce: the temporary's event would otherwise be sent.
	time.Sleep(900 * time.Millisecond)
	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}

	for _, e := range collect(t, events) {
		t.Errorf("the atomic-write temporary reached the wire: %s %q", e.op, e.path)
	}
}

// TestProjectWatcherReportsAtomicSave: filtering the temporary must not swallow
// the save. A rename into place is not a create/delete of the target's name, so
// the browser must still hear that the file changed, or the editor never re-reads.
func TestProjectWatcherReportsAtomicSave(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	events := watcherRecorder(t, dir)

	if err := fsutil.WriteFileAtomic(target, []byte("saved by the editor\n")); err != nil {
		t.Fatal(err)
	}

	seen := collect(t, events)
	if len(seen) == 0 {
		t.Fatal("the save produced no fs_event — the watcher filtered the real file too")
	}
	var realReported bool
	for _, e := range seen {
		if fsutil.IsAtomicTemp(e.path) {
			t.Errorf("broadcast the atomic-write temporary: %s %q", e.op, e.path)
		}
		if e.path == "notes.txt" {
			realReported = true
		}
	}
	if !realReported {
		t.Errorf("the saved file was never reported; events = %+v", seen)
	}
}

// TestAtomicTempNameIsFilteredByEveryRule checks every filter against a name made
// by the same os.CreateTemp call and pattern the writer uses, not one guessed.
func TestAtomicTempNameIsFilteredByEveryRule(t *testing.T) {
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, fsutil.AtomicTempPrefix+"main.go-")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	_ = f.Close()

	if !fsutil.IsAtomicTemp(name) {
		t.Errorf("fsutil.IsAtomicTemp does not recognise the name it generates: %s", filepath.Base(name))
	}
	// The worktree watcher's gate.
	if !git.ShouldIgnoreFile(name) {
		t.Errorf("git.ShouldIgnoreFile does not filter %s", filepath.Base(name))
	}
	// The route every fs_event takes.
	if !shouldSkipFsEvent(dir, filepath.Base(name)) {
		t.Errorf("shouldSkipFsEvent does not filter %s", filepath.Base(name))
	}
	// Ordinary files must still pass.
	for _, filter := range []func(string) bool{fsutil.IsAtomicTemp, git.ShouldIgnoreFile} {
		if filter("main.go") {
			t.Error("an ordinary source file is being filtered")
		}
	}
	if shouldSkipFsEvent(dir, "main.go") {
		t.Error("shouldSkipFsEvent filters an ordinary source file")
	}
}

func drainFsEvents(ch chan fsEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
