package stream

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"mike/backend/fsutil"
	"mike/backend/types"

	"mike/backend/git"
	"mike/pkg/logging"

	"github.com/fsnotify/fsnotify"
)

// DiffStatsEvent holds data for a diff stats broadcast to clients.
type DiffStatsEvent struct {
	ProjectID           string
	TaskID              string
	DiffHash            string
	ShortStat           string // full diff shortstat (for logging); do NOT use for card display
	FilesN              int
	UntrackedN          int
	UntrackedLines      int // additions living in untracked files, never folded into TrackedAdd
	TrackedAdd          int // additions in tracked files only (for card display)
	TrackedDel          int // deletions in tracked files only (for card display)
	ChangedHashes       map[string]string
	RemovedFiles        []string
	StagedPaths         []string
	HasCommittedChanges bool // committed on branch since baseRef, not yet merged
	AlreadyMerged       bool // task branch commits already merged into target
}

type diffBroadcastFn func(event DiffStatsEvent)
type diffCacheUpdateFn func(taskID string, taskDiff *git.TaskDiff)

// diffJob is a unit of work for the diff worker pool.
type diffJob struct {
	taskID    string
	projectID string
}

type worktreeInfo struct {
	projectID   string
	path        string
	gitIndexDir string              // git index dir (in main repo for worktrees)
	ignoredDirs map[string]struct{} // hardcoded + git-ignored directory names
}

// WorktreeWatcher detects file changes in git worktrees via fsnotify.
// Events are throttled per-task: at most one diff compute/broadcast every 1s.
type WorktreeWatcher struct {
	mu          sync.Mutex
	watcher     *fsnotify.Watcher
	worktrees   map[string]worktreeInfo
	throttles   map[string]*time.Timer
	diffCache   sync.Map
	broadcast   diffBroadcastFn
	updateCache diffCacheUpdateFn
	worktreeMgr *git.WorktreeManager

	pathCache      map[string]string // dir path -> taskID for O(1) lookup
	indexDirToTask map[string]string // git index dir -> taskID

	jobCh    chan diffJob // bounded worker pool
	doneCh   chan struct{}
	workerWg sync.WaitGroup
	inFlight map[string]bool // taskID in pool or being processed

	// FS event broadcasting (mirrors ProjectWatcher for task worktrees)
	fsBroadcast  fsBroadcastFn
	fsDebounces  map[string]*time.Timer
	fsEventQueue map[string]string // taskID -> "op:path" (debounced, coalesced)
}

// NewWorktreeWatcher creates a new WorktreeWatcher with a bounded worker pool.
func NewWorktreeWatcher(worktreeMgr *git.WorktreeManager, broadcast diffBroadcastFn, updateCache diffCacheUpdateFn, fsBroadcast fsBroadcastFn) (*WorktreeWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	poolSize := runtime.NumCPU() - 1
	if poolSize < 1 {
		poolSize = 1
	}
	ww := &WorktreeWatcher{
		watcher:        w,
		worktrees:      make(map[string]worktreeInfo),
		throttles:      make(map[string]*time.Timer),
		broadcast:      broadcast,
		updateCache:    updateCache,
		worktreeMgr:    worktreeMgr,
		pathCache:      make(map[string]string),
		indexDirToTask: make(map[string]string),
		jobCh:          make(chan diffJob, poolSize),
		doneCh:         make(chan struct{}),
		inFlight:       make(map[string]bool),
		fsBroadcast:    fsBroadcast,
		fsDebounces:    make(map[string]*time.Timer),
		fsEventQueue:   make(map[string]string),
	}
	for range poolSize {
		ww.workerWg.Add(1)
		go ww.worker()
	}
	return ww, nil
}

// Watch starts monitoring a task's worktree directory for file changes. The diff baseline is not part of a
// watch: a thread can fork, or the target branch move, while the watch is live.
func (ww *WorktreeWatcher) Watch(taskID, projectID string) error {
	path, err := ww.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		return err
	}

	ww.mu.Lock()
	defer ww.mu.Unlock()

	// Remove existing watcher if re-watching
	if info, exists := ww.worktrees[taskID]; exists {
		_ = ww.watcher.Remove(info.path)
		delete(ww.pathCache, info.path+string(filepath.Separator))
		if info.gitIndexDir != "" {
			_ = ww.watcher.Remove(info.gitIndexDir)
			delete(ww.indexDirToTask, info.gitIndexDir)
		}
		dropWatchedTree(ww.pathCache, info.path)
	}

	ignored := loadIgnoredDirs(path)

	ww.worktrees[taskID] = worktreeInfo{
		projectID:   projectID,
		path:        path,
		ignoredDirs: ignored,
	}

	if err := ww.addRecursive(path, ignored, taskID); err != nil {
		delete(ww.worktrees, taskID)
		delete(ww.pathCache, path+string(filepath.Separator))
		return err
	}

	// Watch git index dir to detect stage/unstage operations.
	wtInfo := ww.worktrees[taskID]
	indexDir := resolveGitIndexDir(path)
	if indexDir != "" {
		_ = ww.watcher.Add(indexDir)
		wtInfo.gitIndexDir = indexDir
		ww.indexDirToTask[indexDir] = taskID
	} else {
		gitDir := filepath.Join(path, ".git")
		if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() {
			_ = ww.watcher.Add(gitDir)
			wtInfo.gitIndexDir = gitDir
			ww.indexDirToTask[gitDir] = taskID
		}
	}
	ww.worktrees[taskID] = wtInfo

	logging.For(logging.Watcher).With(logging.TaskID, taskID).Infof("watching %s (ignored dirs: %d)", path, len(ignored))
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
	dropWatchedTree(ww.pathCache, info.path)
	if info.gitIndexDir != "" {
		_ = ww.watcher.Remove(info.gitIndexDir)
		delete(ww.indexDirToTask, info.gitIndexDir)
	}
	delete(ww.worktrees, taskID)

	if timer, ok := ww.throttles[taskID]; ok {
		timer.Stop()
		delete(ww.throttles, taskID)
	}

	logging.For(logging.Watcher).With(logging.TaskID, taskID).Infof("stopped watching")
}

// Close stops the watcher and its worker pool.
//
// The mutex is released before workerWg.Wait, never held across it: a worker in
// computeAndBroadcast takes ww.mu in its deferred cleanup, so waiting for the pool while
// holding it deadlocks whenever a compute is in flight.
func (ww *WorktreeWatcher) Close() {
	ww.mu.Lock()
	for _, timer := range ww.throttles {
		timer.Stop()
	}
	for _, timer := range ww.fsDebounces {
		timer.Stop()
	}
	close(ww.doneCh)
	ww.mu.Unlock()

	ww.workerWg.Wait()

	ww.mu.Lock()
	ww.pathCache = nil
	ww.indexDirToTask = nil
	ww.inFlight = nil
	ww.fsDebounces = nil
	ww.fsEventQueue = nil
	ww.mu.Unlock()

	_ = ww.watcher.Close()
	logging.For(logging.Watcher).Info("closed")
}

// isClosed reports whether Close has begun. Close stops the timers it holds, but one that already fired
// waits on ww.mu and then writes to maps Close released; doneCh closes first, so the answer holds for
// anything running under the lock.
func (ww *WorktreeWatcher) isClosed() bool {
	select {
	case <-ww.doneCh:
		return true
	default:
		return false
	}
}

// scheduleDiff queues a diff compute for the task. It re-arms while a compute is in flight rather than
// drop the pending change, which would leave the diff panel stale until the next filesystem event.
func (ww *WorktreeWatcher) scheduleDiff(taskID, projectID string) {
	ww.mu.Lock()
	if ww.isClosed() {
		ww.mu.Unlock()
		return
	}
	if ww.inFlight[taskID] {
		ww.throttles[taskID] = time.AfterFunc(250*time.Millisecond, func() {
			ww.scheduleDiff(taskID, projectID)
		})
		ww.mu.Unlock()
		return
	}
	delete(ww.throttles, taskID)
	ww.inFlight[taskID] = true
	ww.mu.Unlock()

	select {
	case ww.jobCh <- diffJob{taskID: taskID, projectID: projectID}:
	default:
		ww.mu.Lock()
		delete(ww.inFlight, taskID)
		ww.mu.Unlock()
	}
}

// StartEventLoop runs the fsnotify event loop. Call in a goroutine.
func (ww *WorktreeWatcher) StartEventLoop() {
	runWatcherEventLoop(ww.watcher, "watcher", ww.handleEvent)
}

func (ww *WorktreeWatcher) handleEvent(event fsnotify.Event) {
	ww.mu.Lock()
	if ww.isClosed() {
		ww.mu.Unlock()
		return
	}

	var taskID string
	if tid, ok := ww.pathCache[event.Name]; ok {
		taskID = tid
	} else if tid, ok := ww.pathCache[event.Name+string(filepath.Separator)]; ok {
		taskID = tid
	} else {
		// Git index dir (outside worktree): events fire on the dir or files
		// within it (e.g. <gitdir>/index), so prefix-match the gitdir.
		for dir, tid := range ww.indexDirToTask {
			if event.Name == dir || strings.HasPrefix(event.Name, dir+string(filepath.Separator)) {
				taskID = tid
				break
			}
		}
		if taskID == "" {
			// Fall back to a cached ancestor directory of the event.
			taskID = lookupWatchedOwner(ww.pathCache, event.Name)
		}
	}

	if taskID == "" {
		ww.mu.Unlock()
		return
	}

	info, exists := ww.worktrees[taskID]
	if !exists {
		ww.mu.Unlock()
		return
	}

	// For git index events, only react to index file changes (stage/unstage).
	if info.gitIndexDir != "" && (event.Name == info.gitIndexDir || strings.HasPrefix(event.Name, info.gitIndexDir+string(filepath.Separator))) {
		base := filepath.Base(event.Name)
		parent := filepath.Dir(event.Name)
		if base == "index" {
			// proceed
		} else if parent == info.gitIndexDir && !isGitMetadataFile(base) {
			// temp file in index dir — proceed
		} else {
			ww.mu.Unlock()
			return
		}
	}

	if git.ShouldIgnoreFile(event.Name) {
		ww.mu.Unlock()
		return
	}

	relPath, _ := filepath.Rel(info.path, event.Name)
	if fsutil.RelInsideRoot(relPath) && git.ShouldIgnorePath(relPath) {
		ww.mu.Unlock()
		return
	}

	// Watch new directories created inside the worktree.
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 && fsutil.RelInsideRoot(relPath) {
		if absPath, err := filepath.Abs(event.Name); err == nil {
			if dirInfo, err := filepath.EvalSymlinks(absPath); err == nil {
				if fi, err := os.Stat(dirInfo); err == nil && fi.IsDir() {
					basename := filepath.Base(event.Name)
					if _, ok := info.ignoredDirs[basename]; !ok && !git.ShouldIgnorePath(basename) {
						_ = ww.watcher.Add(event.Name)
						_ = ww.addRecursive(event.Name, info.ignoredDirs, taskID)
					}
				}
			}
		}
	}

	// Broadcast fs events (debounced, like ProjectWatcher)
	var fsOp string
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if _, err := os.Stat(event.Name); err == nil {
			fsOp = "create"
		}
	} else if event.Op&fsnotify.Remove != 0 {
		fsOp = "delete"
	} else if event.Op&fsnotify.Rename != 0 {
		fsOp = "delete"
	}
	if fsOp != "" && ww.fsBroadcast != nil && fsutil.RelInsideRoot(relPath) {
		if timer, exists := ww.fsDebounces[taskID]; exists {
			timer.Stop()
		}
		ww.fsEventQueue[taskID] = fsOp + ":" + relPath
		timer := time.AfterFunc(500*time.Millisecond, func() {
			ww.mu.Lock()
			if ww.isClosed() {
				ww.mu.Unlock()
				return
			}
			if t, exists := ww.fsDebounces[taskID]; exists {
				t.Stop()
				delete(ww.fsDebounces, taskID)
			}
			if evt, ok := ww.fsEventQueue[taskID]; ok {
				colonIdx := strings.IndexByte(evt, ':')
				if colonIdx > 0 {
					op := evt[:colonIdx]
					path := evt[colonIdx+1:]
					ww.fsBroadcast(info.projectID, info.path, op, path)
				}
				delete(ww.fsEventQueue, taskID)
			}
			ww.mu.Unlock()
		})
		ww.fsDebounces[taskID] = timer
	}

	// Throttle: at most one diff compute every 1s per task.
	if _, exists := ww.throttles[taskID]; exists {
		ww.mu.Unlock()
		return
	}

	projectID := info.projectID

	ww.throttles[taskID] = time.AfterFunc(1000*time.Millisecond, func() {
		ww.scheduleDiff(taskID, projectID)
	})
	ww.mu.Unlock()
}

func (ww *WorktreeWatcher) worker() {
	defer ww.workerWg.Done()
	for {
		select {
		case job, ok := <-ww.jobCh:
			if !ok {
				return
			}
			ww.computeAndBroadcast(job.taskID, job.projectID)
		case <-ww.doneCh:
			return
		}
	}
}

func (ww *WorktreeWatcher) computeAndBroadcast(taskID, projectID string) {
	defer func() {
		ww.mu.Lock()
		delete(ww.inFlight, taskID)
		ww.mu.Unlock()
	}()

	// A job can outlive its task: deletion unwatches the worktree, but a job queued before that is
	// already in the channel. A nil task then reads as "no baseline, no merge check".
	task, err := ww.worktreeMgr.GetTaskByID(taskID)
	if err != nil {
		task = nil
	}
	baseRef := ww.worktreeMgr.TaskDiffBaseline(task)
	headRef := ""
	if task != nil && task.Status == types.StatusDone {
		headRef = task.BranchTip
		if headRef == "" {
			headRef = task.Branch
		}
	}
	diffResult, err := ww.worktreeMgr.GetTaskDiff(taskID, baseRef, headRef)
	if err != nil {
		return
	}

	stats := ComputeDiffStats(diffResult)
	untrackedN := stats.VisibleUntracked
	filesN := stats.VisibleTracked + untrackedN

	hash := ComputeDiffHash(diffResult.Diff, diffResult.CommittedDiff, diffResult.Untracked, diffResult.StagedHash)
	if hash != "" {
		if prevRaw, ok := ww.diffCache.Load(taskID); ok {
			prevDiff := prevRaw.(*git.TaskDiff)
			prevHash := ComputeDiffHash(prevDiff.Diff, prevDiff.CommittedDiff, prevDiff.Untracked, prevDiff.StagedHash)
			if hash == prevHash {
				return
			}
		}
	}

	newHashes := diffResult.GetPerFileHashes()
	changedHashes, removedFiles := computeDiffDelta(taskID, newHashes, diffResult, &ww.diffCache)
	// A done task's diff is BaseCommit..BranchTip, not the working tree, so it is not the answer the
	// per-file route should give.
	if ww.updateCache != nil && headRef == "" {
		ww.updateCache(taskID, diffResult)
	}

	// The merge check runs against the target branch name (BaseRef), not the fixed BaseCommit SHA, so
	// it reflects the current state of the target branch.
	if task != nil {
		ww.mu.Lock()
		worktreePath := ww.worktrees[taskID].path
		ww.mu.Unlock()
		if targetBranch, ok := CheckAlreadyMerged(*task); ok {
			stats.AlreadyMerged = ww.worktreeMgr.IsMergedInto(worktreePath, task.Branch, targetBranch)
		}
	}

	if diffResult.Diff != "" || untrackedN > 0 || diffResult.CommittedDiff != "" {
		ww.broadcast(DiffStatsEvent{
			ProjectID:           projectID,
			TaskID:              taskID,
			DiffHash:            hash,
			ShortStat:           diffResult.ShortStat,
			FilesN:              filesN,
			UntrackedN:          untrackedN,
			UntrackedLines:      stats.UntrackedLines,
			TrackedAdd:          stats.TrackedAdd,
			TrackedDel:          stats.TrackedDel,
			ChangedHashes:       changedHashes,
			RemovedFiles:        removedFiles,
			StagedPaths:         diffResult.StagedPaths,
			HasCommittedChanges: diffResult.CommittedDiff != "",
			AlreadyMerged:       stats.AlreadyMerged,
		})
		logging.For(logging.Watcher).With(logging.TaskID, taskID).Infof("diff changed: %s (untracked: %d files)", diffResult.ShortStat, untrackedN)
	} else if len(changedHashes) > 0 || len(removedFiles) > 0 {
		ww.broadcast(DiffStatsEvent{
			ProjectID:           projectID,
			TaskID:              taskID,
			ChangedHashes:       changedHashes,
			RemovedFiles:        removedFiles,
			StagedPaths:         diffResult.StagedPaths,
			HasCommittedChanges: diffResult.CommittedDiff != "",
			AlreadyMerged:       stats.AlreadyMerged,
		})
	}
}

// DiffDigest reports the diff the last broadcast described: the hash `task_diff_stats` carries and how many
// paths are staged. ok=false means this process computed none, which is an unknown and never a clean answer.
func (ww *WorktreeWatcher) DiffDigest(taskID string) (hash string, staged int, ok bool) {
	raw, found := ww.diffCache.Load(taskID)
	if !found {
		return "", 0, false
	}
	d, isDiff := raw.(*git.TaskDiff)
	if !isDiff {
		return "", 0, false
	}
	return ComputeDiffHash(d.Diff, d.CommittedDiff, d.Untracked, d.StagedHash), len(d.StagedPaths), true
}

// computeDiffDelta compares new file hashes with cached previous state.
// Internal files are excluded.
func computeDiffDelta(taskID string, newHashes map[string]string, newDiff *git.TaskDiff, cache *sync.Map) (changedHashes map[string]string, removedFiles []string) {
	prevRaw, hasPrev := cache.Load(taskID)
	prevHashes := map[string]string{}
	if hasPrev {
		prevHashes = prevRaw.(*git.TaskDiff).GetPerFileHashes()
	}

	filteredNew := make(map[string]string, len(newHashes))
	for path, hash := range newHashes {
		if !git.IsInternalFile(path) {
			filteredNew[path] = hash
		}
	}
	filteredPrev := make(map[string]string, len(prevHashes))
	for path, hash := range prevHashes {
		if !git.IsInternalFile(path) {
			filteredPrev[path] = hash
		}
	}

	changedHashes = make(map[string]string)
	for path, newHash := range filteredNew {
		prevHash, exists := filteredPrev[path]
		if !exists || prevHash != newHash {
			changedHashes[path] = newHash
		}
	}
	for path := range filteredPrev {
		if _, exists := filteredNew[path]; !exists {
			removedFiles = append(removedFiles, path)
		}
	}

	cache.Store(taskID, newDiff)
	return
}

// loadIgnoredDirs merges hardcoded dirs with .gitignore-discovered dirs.
func loadIgnoredDirs(repoRoot string) map[string]struct{} {
	result := make(map[string]struct{})
	for name := range git.IgnoredDirs() {
		result[name] = struct{}{}
	}

	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return result
	}

	var dirNames []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Name()[0] != '.' && entry.Name() != "git" {
			if _, ok := result[entry.Name()]; !ok {
				dirNames = append(dirNames, entry.Name())
			}
		}
	}
	if len(dirNames) == 0 {
		return result
	}

	cmd := git.Command(repoRoot, "check-ignore", "--stdin")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result
	}
	var stdout strings.Builder
	cmd.Stdout = &stdout

	for _, name := range dirNames {
		fmt.Fprintln(stdin, name)
	}
	_ = stdin.Close()

	if err := cmd.Run(); err != nil {
		return result
	}

	for line := range strings.SplitSeq(stdout.String(), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			result[name] = struct{}{}
		}
	}

	return result
}

// resolveGitIndexDir finds the git index dir from a worktree's .git file.
// Returns empty string for regular repos.
func resolveGitIndexDir(worktreePath string) string {
	gitFile := filepath.Join(worktreePath, ".git")
	info, err := os.Stat(gitFile)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gitdir: ") {
			return strings.TrimPrefix(line, "gitdir: ")
		}
	}
	return ""
}

func isGitMetadataFile(name string) bool {
	switch name {
	case "HEAD", "commondir", "gitdir", "config", "options", "logs", "refs", "objects", "index.lock":
		return true
	}
	return false
}

func (ww *WorktreeWatcher) addRecursive(dir string, ignoredDirs map[string]struct{}, taskID string) error {
	return addRecursiveWatcher(ww.watcher, ww.pathCache, dir, taskID, func(name string) bool {
		_, ok := ignoredDirs[name]
		return ok
	})
}
