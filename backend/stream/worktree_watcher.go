package stream

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"kanban/backend/git"
)

// diffBroadcastFn is a callback to broadcast diff stats to clients.
// changedHashes contains only files that are new or modified (path -> hash).
// removedFiles lists paths that were deleted.
// stagedPaths lists file paths currently staged in the index.
type diffBroadcastFn func(projectID, taskID, diffHash, shortStat string, untrackedN, untrackedLines int, changedHashes map[string]string, removedFiles []string, stagedPaths []string)

// diffCacheUpdateFn is a callback to update the GitHandler's diff cache.
type diffCacheUpdateFn func(taskID string, taskDiff *git.TaskDiff)

// worktreeInfo tracks the state of a watched worktree.
type worktreeInfo struct {
	projectID   string
	path        string
	baseRef     string
	ignoredDirs map[string]struct{} // merged set: hardcoded + git-ignored directory names
}

// WorktreeWatcher uses fsnotify to detect file changes in git worktrees.
// A single shared fsnotify.Watcher monitors all worktree directories.
// Events are debounced per-task before computing and broadcasting diff stats.
type WorktreeWatcher struct {
	mu          sync.Mutex
	watcher     *fsnotify.Watcher
	worktrees   map[string]worktreeInfo // taskID -> info
	debounces   map[string]*time.Timer  // taskID -> debounce timer
	diffCache   sync.Map                // taskID -> *git.TaskDiff (previous state for delta computation)
	broadcast   diffBroadcastFn
	updateCache diffCacheUpdateFn       // callback to update GitHandler's diffCache
	worktreeMgr *git.WorktreeManager
}

// NewWorktreeWatcher creates a new WorktreeWatcher.
func NewWorktreeWatcher(worktreeMgr *git.WorktreeManager, broadcast diffBroadcastFn, updateCache diffCacheUpdateFn) (*WorktreeWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &WorktreeWatcher{
		watcher:     w,
		worktrees:   make(map[string]worktreeInfo),
		debounces:   make(map[string]*time.Timer),
		broadcast:   broadcast,
		updateCache: updateCache,
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

	// Discover ignored directories (hardcoded + .gitignore)
	ignored := loadIgnoredDirs(path)

	// Track worktree info
	ww.worktrees[taskID] = worktreeInfo{
		projectID:   projectID,
		path:        path,
		baseRef:     baseRef,
		ignoredDirs: ignored,
	}

	// Add directory to fsnotify watcher (recursive, skipping ignored dirs)
	if err := ww.addRecursive(path, ignored); err != nil {
		delete(ww.worktrees, taskID)
		return err
	}

	log.Printf("[watcher] task=%s watching %s (ignored dirs: %d)", taskID, path, len(ignored))
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

	// Skip events from ignored directories
	if len(info.ignoredDirs) > 0 {
		relPath, err := filepath.Rel(info.path, event.Name)
		if err == nil && git.ShouldIgnorePath(relPath) {
			ww.mu.Unlock()
			return
		}
		// Also check against git-discovered ignored dirs (top-level only)
		if basename := filepath.Base(event.Name); basename != "." {
			if _, ok := info.ignoredDirs[basename]; ok {
				ww.mu.Unlock()
				return
			}
		}
	}
	// Skip temporary/backup files (*.swp, *~, .DS_Store, etc.)
	if git.ShouldIgnoreFile(event.Name) {
		ww.mu.Unlock()
		return
	}

	// If a new directory was created, start watching it recursively
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if absPath, err := filepath.Abs(event.Name); err == nil {
			if dirInfo, err := filepath.EvalSymlinks(absPath); err == nil {
				if fi, err := os.Stat(dirInfo); err == nil && fi.IsDir() {
					// Skip ignored directories
					basename := filepath.Base(event.Name)
					if _, ok := info.ignoredDirs[basename]; !ok && !git.ShouldIgnorePath(basename) {
						_ = ww.watcher.Add(event.Name)
						_ = ww.addRecursive(event.Name, info.ignoredDirs)
					}
				}
			}
		}
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
		hash := ComputeDiffHash(diffResult.Diff, diffResult.Untracked, "")
		newHashes := diffResult.GetPerFileHashes()

		// Compute delta against previous state
		changedHashes, removedFiles := computeDiffDelta(taskID, newHashes, diffResult, &ww.diffCache)
		// Update GitHandler's diffCache so on-demand file fetches work
		if ww.updateCache != nil {
			ww.updateCache(taskID, diffResult)
		}
		ww.broadcast(projectID, taskID, hash, diffResult.ShortStat, untrackedN, untrackedLines, changedHashes, removedFiles, diffResult.StagedPaths)
		log.Printf("[watcher] task=%s diff changed: %s (untracked: %d files)", taskID, diffResult.ShortStat, untrackedN)
	} else {
		// No changes — broadcast empty hash to clear
		ww.broadcast(projectID, taskID, "", "", 0, 0, nil, nil, nil)
	}
}

// computeDiffDelta compares new file hashes with the cached previous state.
// Returns changedHashes (new or modified files) and removedFiles (deleted paths).
// Updates the cache with the new state.
func computeDiffDelta(taskID string, newHashes map[string]string, newDiff *git.TaskDiff, cache *sync.Map) (changedHashes map[string]string, removedFiles []string) {
	// Get previous state
	prevRaw, hasPrev := cache.Load(taskID)
	prevHashes := map[string]string{}
	if hasPrev {
		prevDiff := prevRaw.(*git.TaskDiff)
		prevHashes = prevDiff.GetPerFileHashes()
	}

	// Find changed files (new or hash changed)
	changedHashes = make(map[string]string)
	for path, newHash := range newHashes {
		prevHash, exists := prevHashes[path]
		if !exists || prevHash != newHash {
			changedHashes[path] = newHash
		}
	}

	// Find removed files (in previous but not in new)
	for path := range prevHashes {
		if _, exists := newHashes[path]; !exists {
			removedFiles = append(removedFiles, path)
		}
	}

	// Update cache
	cache.Store(taskID, newDiff)
	return
}

// countUntrackedLines sums the line counts from untracked file entries.
func countUntrackedLines(untracked []git.UntrackedFile) int {
	total := 0
	for _, u := range untracked {
		total += len(u.Lines)
	}
	return total
}

// loadIgnoredDirs builds a merged set of directory names to ignore.
// Combines hardcoded well-known directories with directories discovered
// via git check-ignore (respects .gitignore rules).
func loadIgnoredDirs(repoRoot string) map[string]struct{} {
	// Start with hardcoded list
	result := make(map[string]struct{})
	for name := range git.IgnoredDirs() {
		result[name] = struct{}{}
	}

	// Discover additional ignored directories via git check-ignore.
	// Walk immediate subdirectories of the repo root and batch-check them.
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return result // not a directory, return hardcoded only
	}

	var dirNames []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Name()[0] != '.' && entry.Name() != "git" {
			// Skip hidden dirs (already handled by hardcoded) and the .git dir itself
			// Check dirs that aren't already in our hardcoded list
			if _, ok := result[entry.Name()]; !ok {
				dirNames = append(dirNames, entry.Name())
			}
		}
	}

	if len(dirNames) == 0 {
		return result
	}

	// Batch check via git check-ignore (stdin mode)
	cmd := exec.Command("git", "check-ignore", "--stdin")
	cmd.Dir = repoRoot
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result
	}
	var stdout strings.Builder
	cmd.Stdout = &stdout

	// Write directory names to stdin
	for _, name := range dirNames {
		fmt.Fprintln(stdin, name)
	}
	_ = stdin.Close()

	if err := cmd.Run(); err != nil {
		return result // git check-ignore returns non-zero if nothing is ignored
	}

	// Parse output: each line is an ignored path
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			result[name] = struct{}{}
		}
	}

	return result
}

// addRecursive watches a directory and all its existing subdirectories.
// Skips directories whose basename is in the ignoredDirs set.
func (ww *WorktreeWatcher) addRecursive(dir string, ignoredDirs map[string]struct{}) error {
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
			// Skip ignored directories
			if _, ok := ignoredDirs[d.Name()]; ok {
				return filepath.SkipDir
			}
			_ = ww.watcher.Add(path)
		}
		return nil
	})
}
