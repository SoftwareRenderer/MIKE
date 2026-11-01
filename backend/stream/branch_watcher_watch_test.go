package stream

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fsnotify/fsnotify"
)

// writeGitHEAD makes a directory look like a project checkout, with a branch to read.
func writeGitHEAD(t *testing.T, projectPath, branch string) {
	t.Helper()
	gitDir := gitDirOf(projectPath)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gitDir, err)
	}
	if err := os.WriteFile(headPathOf(projectPath), []byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", headPathOf(projectPath), err)
	}
}

// A project whose path moved must leave the directory it was watching. The watch is registered on the
// .git directory, so a release that names HEAD is a no-op and the old directory stays watched with no
// row to route its events to.
func TestBranchWatcherReWatchReleasesTheDirectoryItMovedAwayFrom(t *testing.T) {
	root := t.TempDir()
	before := filepath.Join(root, "before")
	after := filepath.Join(root, "after")
	writeGitHEAD(t, before, "main")
	writeGitHEAD(t, after, "main")

	bw, err := NewBranchWatcher(func(string, string) {})
	if err != nil {
		t.Fatalf("NewBranchWatcher: %v", err)
	}
	defer bw.Close()

	if err := bw.Watch("p1", before); err != nil {
		t.Fatalf("watch %s: %v", before, err)
	}
	if err := bw.Watch("p1", after); err != nil {
		t.Fatalf("watch %s: %v", after, err)
	}

	watched := bw.watcher.WatchList()
	if !slices.Contains(watched, gitDirOf(after)) {
		t.Fatalf("%s is not watched: %v", gitDirOf(after), watched)
	}
	if slices.Contains(watched, gitDirOf(before)) {
		t.Errorf("the directory this project moved away from is still watched: %v", watched)
	}

	bw.Unwatch("p1")
	if left := bw.watcher.WatchList(); slices.Contains(left, gitDirOf(after)) {
		t.Errorf("Unwatch left %s watched: %v", gitDirOf(after), left)
	}
}

// The event an editor produces names the file inside the watched directory, and only the project that
// owns that directory may be told about it.
func TestBranchWatcherRoutesAnEventByDirectoryOwnership(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one")
	other := filepath.Join(root, "other")
	writeGitHEAD(t, one, "main")
	writeGitHEAD(t, other, "main")

	type change struct{ projectID, branch string }
	seen := make(chan change, 4)
	bw, err := NewBranchWatcher(func(projectID, branch string) { seen <- change{projectID, branch} })
	if err != nil {
		t.Fatalf("NewBranchWatcher: %v", err)
	}
	defer bw.Close()

	for _, p := range []struct{ id, path string }{{"one", one}, {"other", other}} {
		if err := bw.Watch(p.id, p.path); err != nil {
			t.Fatalf("watch %s: %v", p.path, err)
		}
	}

	if err := os.WriteFile(headPathOf(other), []byte("ref: refs/heads/topic\n"), 0o644); err != nil {
		t.Fatalf("rewrite HEAD: %v", err)
	}
	bw.handleEvent(fsnotify.Event{Name: headPathOf(other), Op: fsnotify.Write})

	if len(seen) != 1 {
		t.Fatalf("broadcasts = %d, want exactly one", len(seen))
	}
	got := <-seen
	if got.projectID != "other" || got.branch != "topic" {
		t.Errorf("broadcast %v, want {other topic}", got)
	}
}
