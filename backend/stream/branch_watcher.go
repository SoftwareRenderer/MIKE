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
// Replaces the polling-based branch poller with event-driven detection.
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

	headPath := filepath.Join(path, ".git", "HEAD")

	// Remove existing watcher if re-watching
	if existing, exists := bw.projects[projectID]; exists {
		_ = bw.watcher.Remove(filepath.Join(existing.path, ".git", "HEAD"))
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
	gitDir := filepath.Join(path, ".git")
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

	_ = bw.watcher.Remove(filepath.Join(info.path, ".git", "HEAD"))
	_ = bw.watcher.Remove(filepath.Join(info.path, ".git"))
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
		if event.Name == filepath.Join(proj.path, ".git", "HEAD") {
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
