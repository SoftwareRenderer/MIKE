package stream

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// fsBroadcastFn is a callback to broadcast fs events to clients.
type fsBroadcastFn func(projectID, op, path string)

// ProjectWatcher uses fsnotify to detect file create/delete events in project directories.
// A single shared fsnotify.Watcher monitors all project directories.
type ProjectWatcher struct {
	mu        sync.Mutex
	watcher   *fsnotify.Watcher
	projects  map[string]string // projectID -> path
	debounces map[string]*time.Timer
	broadcast fsBroadcastFn
}

// NewProjectWatcher creates a new ProjectWatcher.
func NewProjectWatcher(broadcast fsBroadcastFn) (*ProjectWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &ProjectWatcher{
		watcher:     w,
		projects:    make(map[string]string),
		debounces:   make(map[string]*time.Timer),
		broadcast:   broadcast,
	}, nil
}

// Watch starts monitoring a project directory for file create/delete events.
func (pw *ProjectWatcher) Watch(projectID, path string) error {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	// Remove existing watcher if re-watching
	if existingPath, exists := pw.projects[projectID]; exists {
		_ = pw.watcher.Remove(existingPath)
	}

	pw.projects[projectID] = path

	// Add directory to watcher (recursive)
	if err := pw.addRecursive(path); err != nil {
		delete(pw.projects, projectID)
		return err
	}

	log.Printf("[project-watcher] project=%s watching %s", projectID, path)
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

	// Cancel any pending debounce
	if timer, ok := pw.debounces[projectID]; ok {
		timer.Stop()
		delete(pw.debounces, projectID)
	}

	log.Printf("[project-watcher] project=%s stopped watching", projectID)
}

// Close stops the watcher and releases resources.
func (pw *ProjectWatcher) Close() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	for _, timer := range pw.debounces {
		timer.Stop()
	}

	_ = pw.watcher.Close()
	log.Println("[project-watcher] closed")
}

// StartEventLoop runs the fsnotify event loop. Should be called in a goroutine.
func (pw *ProjectWatcher) StartEventLoop() {
	log.Println("[project-watcher] event loop started")
	for {
		select {
		case event, ok := <-pw.watcher.Events:
			if !ok {
				return
			}
			pw.handleEvent(event)
		case err, ok := <-pw.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[project-watcher] error: %v", err)
		}
	}
}

func (pw *ProjectWatcher) handleEvent(event fsnotify.Event) {
	pw.mu.Lock()

	// Find which project this event belongs to
	var projectID string
	var projectPath string
	for id, path := range pw.projects {
		prefix := path
		if !strings.HasSuffix(prefix, string(filepath.Separator)) {
			prefix = prefix + string(filepath.Separator)
		}
		if event.Name == path || strings.HasPrefix(event.Name, prefix) {
			projectID = id
			projectPath = path
			break
		}
	}

	if projectID == "" {
		pw.mu.Unlock()
		return
	}

	// If a new directory was created, start watching it recursively
	if event.Op&(fsnotify.Create) != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			_ = pw.addRecursive(event.Name)
		}
	}

	// Determine operation
	var op string
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		// Check if it's actually a file (not a directory being watched)
		if info, err := os.Stat(event.Name); err == nil && !info.IsDir() {
			op = "create"
		}
	} else if event.Op&fsnotify.Remove != 0 {
		op = "delete"
	} else if event.Op&fsnotify.Rename != 0 {
		op = "delete" // rename removes the old path
	}

	if op == "" {
		pw.mu.Unlock()
		return
	}

	// Debounce per project to batch rapid events
	if timer, exists := pw.debounces[projectID]; exists {
		timer.Stop()
	}

	relPath, _ := filepath.Rel(projectPath, event.Name)
	timer := time.AfterFunc(500*time.Millisecond, func() {
		pw.broadcast(projectID, op, relPath)
	})
	pw.debounces[projectID] = timer
	pw.mu.Unlock()
}

// addRecursive watches a directory and all its existing subdirectories.
func (pw *ProjectWatcher) addRecursive(dir string) error {
	if err := pw.watcher.Add(dir); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != dir {
			_ = pw.watcher.Add(path)
		}
		return nil
	})
}
