package stream

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"mike/backend/fsutil"
	"mike/pkg/logging"
)

// isInternalPathInProject checks if absPath is .git or flight recorder relative to projectRoot.
func isInternalPathInProject(absPath, projectRoot string) bool {
	rel, err := filepath.Rel(projectRoot, absPath)
	if err != nil {
		return false
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	base := filepath.Base(absPath)
	if base == "kanban.trace" || base == "kanban.trace.old" {
		return true
	}
	return false
}

type fsBroadcastFn func(projectID, repoRoot, op, path string)

// ProjectWatcher detects file create/delete events in project directories via fsnotify.
// A single shared fsnotify.Watcher monitors all project directories.
type ProjectWatcher struct {
	mu        sync.Mutex
	watcher   *fsnotify.Watcher
	projects  map[string]string
	debounces map[string]*time.Timer
	broadcast fsBroadcastFn
	pathCache map[string]string // dir path -> projectID
}

// NewProjectWatcher creates a new ProjectWatcher.
func NewProjectWatcher(broadcast fsBroadcastFn) (*ProjectWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &ProjectWatcher{
		watcher:   w,
		projects:  make(map[string]string),
		debounces: make(map[string]*time.Timer),
		broadcast: broadcast,
		pathCache: make(map[string]string),
	}, nil
}

// Watch starts monitoring a project directory for file create/delete events.
func (pw *ProjectWatcher) Watch(projectID, path string) error {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	if existingPath, exists := pw.projects[projectID]; exists {
		_ = pw.watcher.Remove(existingPath)
		prefix := existingPath + string(filepath.Separator)
		for p := range pw.pathCache {
			if p == existingPath || strings.HasPrefix(p, prefix) {
				delete(pw.pathCache, p)
			}
		}
	}

	pw.projects[projectID] = path

	if err := pw.addRecursive(path, projectID); err != nil {
		delete(pw.projects, projectID)
		delete(pw.pathCache, path)
		delete(pw.pathCache, path+string(filepath.Separator))
		return err
	}

	logging.For(logging.Watcher).With(logging.ProjectID, projectID).Infof("watching %s", path)
	return nil
}

// Unwatch stops monitoring a project directory.
func (pw *ProjectWatcher) Unwatch(projectID string) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	path, exists := pw.projects[projectID]
	if !exists {
		return
	}

	_ = pw.watcher.Remove(path)
	delete(pw.projects, projectID)
	delete(pw.pathCache, path)
	delete(pw.pathCache, path+string(filepath.Separator))
	prefix := path + string(filepath.Separator)
	for p := range pw.pathCache {
		if strings.HasPrefix(p, prefix) {
			delete(pw.pathCache, p)
		}
	}

	if timer, ok := pw.debounces[projectID]; ok {
		timer.Stop()
		delete(pw.debounces, projectID)
	}

	logging.For(logging.Watcher).With(logging.ProjectID, projectID).Infof("stopped watching")
}

// Close stops the watcher and releases resources.
func (pw *ProjectWatcher) Close() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	for _, timer := range pw.debounces {
		timer.Stop()
	}

	pw.pathCache = nil

	_ = pw.watcher.Close()
	logging.For(logging.Watcher).Info("closed")
}

// StartEventLoop runs the fsnotify event loop. Call in a goroutine.
func (pw *ProjectWatcher) StartEventLoop() {
	runWatcherEventLoop(pw.watcher, "project-watcher", pw.handleEvent)
}

func (pw *ProjectWatcher) handleEvent(event fsnotify.Event) {
	pw.mu.Lock()

	projectID := lookupWatchedOwner(pw.pathCache, event.Name)

	if projectID == "" {
		pw.mu.Unlock()
		return
	}

	projectPath := pw.projects[projectID]

	if isInternalPathInProject(event.Name, projectPath) {
		pw.mu.Unlock()
		return
	}

	// The atomic-write temporary (fsutil.IsAtomicTemp) must not take this project's
	// debounce slot, or the browser hears about a file that has already been renamed
	// away. The worktree watcher skips the same names via git.ShouldIgnoreFile.
	if fsutil.IsAtomicTemp(event.Name) {
		pw.mu.Unlock()
		return
	}

	if event.Op&(fsnotify.Create) != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if !isInternalPathInProject(event.Name, projectPath) {
				_ = pw.addRecursive(event.Name, projectID)
			}
		}
	}

	var op string
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if _, err := os.Stat(event.Name); err == nil {
			op = "create"
		}
	} else if event.Op&fsnotify.Remove != 0 {
		op = "delete"
	} else if event.Op&fsnotify.Rename != 0 {
		op = "delete"
	}

	if op == "" {
		pw.mu.Unlock()
		return
	}

	if timer, exists := pw.debounces[projectID]; exists {
		timer.Stop()
	}

	relPath, _ := filepath.Rel(projectPath, event.Name)
	timer := time.AfterFunc(500*time.Millisecond, func() {
		pw.broadcast(projectID, projectPath, op, relPath)
	})
	pw.debounces[projectID] = timer
	pw.mu.Unlock()
}

func (pw *ProjectWatcher) addRecursive(dir string, projectID string) error {
	return addRecursiveWatcher(pw.watcher, pw.pathCache, dir, projectID, func(name string) bool {
		return name == ".git"
	})
}
