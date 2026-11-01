package stream

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	"mike/pkg/logging"
)

// branchProject tracks state for a watched project.
type branchProject struct {
	path       string
	lastBranch string
}

// branchBroadcastFn broadcasts a branch change to clients.
type branchBroadcastFn func(projectID, branch string)

// BranchWatcher detects branch switches by watching .git/HEAD via fsnotify.
type BranchWatcher struct {
	mu        sync.Mutex
	watcher   *fsnotify.Watcher
	projects  map[string]branchProject // projectID -> info
	broadcast branchBroadcastFn
}

// NewBranchWatcher creates a new BranchWatcher.
func NewBranchWatcher(broadcast branchBroadcastFn) (*BranchWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &BranchWatcher{
		watcher:   w,
		projects:  make(map[string]branchProject),
		broadcast: broadcast,
	}, nil
}

// Watch starts monitoring a project's .git/HEAD for branch switches.
func (bw *BranchWatcher) Watch(projectID, path string) error {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	headPath := headPathOf(path)

	// A re-watch replaces the watch, so the previous directory leaves by the name it was added
	// under — the .git directory. Removing HEAD would name a path never registered: it
	// answers nothing and leaves the old directory watched after the row moved.
	if existing, exists := bw.projects[projectID]; exists {
		_ = bw.watcher.Remove(gitDirOf(existing.path))
	}

	branch := readBranchFile(headPath)
	if branch == "" {
		branch = "unknown"
	}

	bw.projects[projectID] = branchProject{
		path:       path,
		lastBranch: branch,
	}

	// Watch .git dir (not HEAD directly) — git replaces HEAD via temp+rename.
	gitDir := gitDirOf(path)
	if err := bw.watcher.Add(gitDir); err != nil {
		delete(bw.projects, projectID)
		return err
	}

	logging.For(logging.Watcher).With(logging.ProjectID, projectID).Infof("watching %s (initial branch: %s)", path, branch)
	return nil
}

// Unwatch stops monitoring a project's branch.
func (bw *BranchWatcher) Unwatch(projectID string) {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	info, exists := bw.projects[projectID]
	if !exists {
		return
	}

	// The .git directory is what was added; HEAD is only where its events point.
	_ = bw.watcher.Remove(gitDirOf(info.path))
	delete(bw.projects, projectID)

	logging.For(logging.Watcher).With(logging.ProjectID, projectID).Infof("stopped watching")
}

// Close stops the watcher and releases resources.
func (bw *BranchWatcher) Close() {
	bw.mu.Lock()
	defer bw.mu.Unlock()

	_ = bw.watcher.Close()
	logging.For(logging.Watcher).Info("closed")
}

// StartEventLoop runs the fsnotify event loop. Call in a goroutine.
func (bw *BranchWatcher) StartEventLoop() {
	runWatcherEventLoop(bw.watcher, "branch-watcher", bw.handleEvent)
}

func (bw *BranchWatcher) handleEvent(event fsnotify.Event) {
	if filepath.Base(event.Name) != "HEAD" {
		return
	}

	bw.mu.Lock()

	var projectID string
	var info branchProject
	for id, proj := range bw.projects {
		if event.Name == headPathOf(proj.path) {
			projectID = id
			info = proj
			break
		}
	}

	if projectID == "" {
		bw.mu.Unlock()
		return
	}

	branch := readBranchFile(event.Name)
	if branch == "" || branch == info.lastBranch {
		bw.mu.Unlock()
		return
	}

	info.lastBranch = branch
	bw.projects[projectID] = info
	bw.mu.Unlock()

	logging.For(logging.Watcher).With(logging.ProjectID, projectID).Infof("branch changed: %s", branch)
	bw.broadcast(projectID, branch)
}

// gitDirOf is the directory a project's branch watch is registered on. fsnotify reports the file that
// changed inside a watched directory, so the watch is the directory's while every event names HEAD.
func gitDirOf(projectPath string) string {
	return filepath.Join(projectPath, ".git")
}

// headPathOf is the file whose rewrite changes the branch a project is on.
func headPathOf(projectPath string) string {
	return filepath.Join(gitDirOf(projectPath), "HEAD")
}

// readBranchFile extracts the branch name from a .git/HEAD file.
// Returns short SHA for detached HEAD, or empty string on error.
func readBranchFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))

	if strings.HasPrefix(line, "ref: refs/heads/") {
		return strings.TrimPrefix(line, "ref: refs/heads/")
	}
	if len(line) == 40 && isHex(line) {
		return line[:7]
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}
