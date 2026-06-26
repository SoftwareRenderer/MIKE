package stream

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"kanban/backend/git"
)

// diffBroadcastFn is a callback to broadcast diff stats to clients.
type diffBroadcastFn func(projectID, taskID, diffHash, shortStat string, untrackedN, untrackedLines int)

// worktreeInfo tracks the state of a watched worktree.
type worktreeInfo struct {
	projectID string
	path      string
	baseRef   string
}

// WorktreeWatcher uses fsnotify to detect file changes in git worktrees.
// A single shared fsnotify.Watcher monitors all worktree directories.
// Events are debounced per-task before computing and broadcasting diff stats.
type WorktreeWatcher struct {
	mu          sync.Mutex
	watcher     *fsnotify.Watcher
	worktrees   map[string]worktreeInfo // taskID -> info
	debounces   map[string]*time.Timer  // taskID -> debounce timer
	broadcast   diffBroadcastFn
	worktreeMgr *git.WorktreeManager
}

// NewWorktreeWatcher creates a new WorktreeWatcher.
func NewWorktreeWatcher(worktreeMgr *git.WorktreeManager, broadcast diffBroadcastFn) (*WorktreeWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &WorktreeWatcher{
		watcher:     w,
		worktrees:   make(map[string]worktreeInfo),
		debounces:   make(map[string]*time.Timer),
		broadcast:   broadcast,
		worktreeMgr: worktreeMgr,
	}, nil
}

// Watch starts monitoring a task's worktree directory for file changes.
func (ww *WorktreeWatcher) Watch(taskID, projectID, baseRef string) error {
	path, err := ww.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		return err
	}

	ww.mu.Lock()
	defer ww.mu.Unlock()

	// Remove existing watcher if re-watching
	if info, exists := ww.worktrees[taskID]; exists {
		_ = ww.watcher.Remove(info.path)
	}

	// Track worktree info
	ww.worktrees[taskID] = worktreeInfo{
		projectID: projectID,
		path:      path,
		baseRef:   baseRef,
	}

	// Add directory to fsnotify watcher (recursive)
	if err := ww.addRecursive(path); err != nil {
		delete(ww.worktrees, taskID)
		return err
	}

	log.Printf("[watcher] task=%s watching %s", taskID, path)
	return nil
}

// Unwatch stops monitoring a task's worktree directory.
func (ww *WorktreeWatcher) Unwatch(taskID string) {
	ww.mu.Lock()
	defer ww.mu.Unlock()

	info, exists := ww.worktrees[taskID]
	if !exists {
		return
	}

	_ = ww.watcher.Remove(info.path)
	delete(ww.worktrees, taskID)

	// Cancel any pending debounce
	if timer, ok := ww.debounces[taskID]; ok {
		timer.Stop()
		delete(ww.debounces, taskID)
	}

	log.Printf("[watcher] task=%s stopped watching", taskID)
}

// Close stops the watcher and releases resources.
func (ww *WorktreeWatcher) Close() {
	ww.mu.Lock()
	defer ww.mu.Unlock()

	// Cancel all pending debounces
	for _, timer := range ww.debounces {
		timer.Stop()
	}

	_ = ww.watcher.Close()
	log.Println("[watcher] closed")
}

// StartEventLoop runs the fsnotify event loop. Should be called in a goroutine.
func (ww *WorktreeWatcher) StartEventLoop() {
	log.Println("[watcher] event loop started")
	for {
		select {
		case event, ok := <-ww.watcher.Events:
			if !ok {
				return
			}
			ww.handleEvent(event)
		case err, ok := <-ww.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[watcher] error: %v", err)
		}
	}
}

func (ww *WorktreeWatcher) handleEvent(event fsnotify.Event) {
	// If a new directory was created, start watching it recursively
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if info, err := filepath.Abs(event.Name); err == nil {
			if dirInfo, err := filepath.EvalSymlinks(info); err == nil {
				if fi, err := os.Stat(dirInfo); err == nil && fi.IsDir() {
					_ = ww.watcher.Add(event.Name)
					_ = ww.addRecursive(event.Name)
				}
			}
		}
	}

	// Find which task's worktree this event belongs to
	ww.mu.Lock()

	var taskID string
	var info worktreeInfo
	for id, winfo := range ww.worktrees {
		// Ensure path ends with / for clean prefix matching
		prefix := winfo.path
		if !strings.HasSuffix(prefix, string(filepath.Separator)) {
			prefix = prefix + string(filepath.Separator)
		}
		// Check if the event path is inside this worktree directory
		if event.Name == winfo.path || strings.HasPrefix(event.Name, prefix) {
			taskID = id
			info = winfo
			break
		}
	}

	if taskID == "" {
		ww.mu.Unlock()
		return
	}

	// Skip remove/rename events for debounce trigger — they still count as changes
	// but we only need one event to trigger the debounce

	// Debounce: reset timer for this task
	if timer, exists := ww.debounces[taskID]; exists {
		timer.Stop()
	}

	projectID := info.projectID
	baseRef := info.baseRef
	timer := time.AfterFunc(2000*time.Millisecond, func() {
		ww.computeAndBroadcast(taskID, projectID, baseRef)
	})
	ww.debounces[taskID] = timer
	ww.mu.Unlock()
}

func (ww *WorktreeWatcher) computeAndBroadcast(taskID, projectID, baseRef string) {
	// Remove debounce timer
	ww.mu.Lock()
	if timer, exists := ww.debounces[taskID]; exists {
		timer.Stop()
	}
	delete(ww.debounces, taskID)
	ww.mu.Unlock()

	// Compute diff
	diffResult, err := ww.worktreeMgr.GetTaskDiff(taskID, baseRef, "")
	if err != nil {
		return
	}

	untrackedN := len(diffResult.Untracked)
	if diffResult.Diff != "" || untrackedN > 0 {
		untrackedLines := countUntrackedLines(diffResult.Untracked)
		// Compute hash from tracked diff + untracked file contents
		hashInput := diffResult.Diff
		for _, u := range diffResult.Untracked {
			hashInput += u.Path + u.Diff
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput)))
		ww.broadcast(projectID, taskID, hash, diffResult.ShortStat, untrackedN, untrackedLines)
		log.Printf("[watcher] task=%s diff changed: %s (untracked: %d files)", taskID, diffResult.ShortStat, untrackedN)
	} else {
		// No changes — broadcast empty hash to clear
		ww.broadcast(projectID, taskID, "", "", 0, 0)
	}
}

// countUntrackedLines sums the pre-computed line counts from untracked file diffs.
func countUntrackedLines(untracked []git.UntrackedFile) int {
	total := 0
	for _, u := range untracked {
		total += u.Lines
	}
	return total
}

// addRecursive watches a directory and all its existing subdirectories.
func (ww *WorktreeWatcher) addRecursive(dir string) error {
	// Watch the directory itself
	if err := ww.watcher.Add(dir); err != nil {
		return err
	}
	// Walk existing subdirectories and watch them too
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors, keep watching what we can
		}
		if d.IsDir() && path != dir {
			_ = ww.watcher.Add(path)
		}
		return nil
	})
}


