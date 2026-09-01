package stream

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"kanban/backend/types"

	"github.com/fsnotify/fsnotify"
	"kanban/backend/git"
)

// DiffStatsEvent holds data for a diff stats broadcast to clients.
type DiffStatsEvent struct {
	ProjectID           string
	TaskID              string
	DiffHash            string
	ShortStat           string // full diff shortstat (for logging); do NOT use for card display
	FilesN              int
	UntrackedN          int
	UntrackedLines      int
	TrackedAdd          int // additions from truncated manifest (for card display)
	TrackedDel          int // deletions from truncated manifest (for card display)
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
	baseRef   string
}

type worktreeInfo struct {
	projectID   string
	path        string
	baseRef     string
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
	for i := 0; i < poolSize; i++ {
		ww.workerWg.Add(1)
		go ww.worker()
	}
	return ww, nil
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
		delete(ww.pathCache, info.path+string(filepath.Separator))
		if info.gitIndexDir != "" {
			_ = ww.watcher.Remove(info.gitIndexDir)
			delete(ww.indexDirToTask, info.gitIndexDir)
		}
		prefix := info.path + string(filepath.Separator)
		for p := range ww.pathCache {
			if p == info.path || strings.HasPrefix(p, prefix) {
				delete(ww.pathCache, p)
			}
		}
	}

	ignored := loadIgnoredDirs(path)

	ww.worktrees[taskID] = worktreeInfo{
		projectID:   projectID,
		path:        path,
		baseRef:     baseRef,
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
	delete(ww.pathCache, info.path+string(filepath.Separator))
	if info.gitIndexDir != "" {
		_ = ww.watcher.Remove(info.gitIndexDir)
		delete(ww.indexDirToTask, info.gitIndexDir)
	}
	prefix := info.path + string(filepath.Separator)
	for p := range ww.pathCache {
		if p == info.path || strings.HasPrefix(p, prefix) {
			delete(ww.pathCache, p)
		}
	}
	delete(ww.worktrees, taskID)

	if timer, ok := ww.throttles[taskID]; ok {
		timer.Stop()
		delete(ww.throttles, taskID)
	}

	log.Printf("[watcher] task=%s stopped watching", taskID)
}

// Close stops the watcher, worker pool, and releases resources.
func (ww *WorktreeWatcher) Close() {
	ww.mu.Lock()
	defer ww.mu.Unlock()

	for _, timer := range ww.throttles {
		timer.Stop()
	}
	for _, timer := range ww.fsDebounces {
		timer.Stop()
	}

	close(ww.doneCh)
	ww.workerWg.Wait()

	ww.pathCache = nil
	ww.indexDirToTask = nil
	ww.inFlight = nil
	ww.fsDebounces = nil
	ww.fsEventQueue = nil

	_ = ww.watcher.Close()
	log.Println("[watcher] closed")
}

// StartEventLoop runs the fsnotify event loop. Call in a goroutine.
func (ww *WorktreeWatcher) StartEventLoop() {
	runWatcherEventLoop(ww.watcher, "watcher", ww.handleEvent)
}

func (ww *WorktreeWatcher) handleEvent(event fsnotify.Event) {
	ww.mu.Lock()

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
			taskID, _ = lookupWatchedOwner(ww.pathCache, event.Name)
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
	if !strings.HasPrefix(relPath, "..") && git.ShouldIgnorePath(relPath) {
		ww.mu.Unlock()
		return
	}

	// Watch new directories created inside the worktree.
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 && !strings.HasPrefix(relPath, "..") {
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
	if fsOp != "" && ww.fsBroadcast != nil && !strings.HasPrefix(relPath, "..") {
		if timer, exists := ww.fsDebounces[taskID]; exists {
			timer.Stop()
		}
		ww.fsEventQueue[taskID] = fsOp + ":" + relPath
		timer := time.AfterFunc(500*time.Millisecond, func() {
			ww.mu.Lock()
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
	baseRef := info.baseRef

	// scheduleDiff arms a diff-compute timer for the task. If a compute is already
	// in flight when the timer fires, it re-arms with a short delay instead of
	// dropping the pending change — otherwise a burst of writes during a slow
	// compute (git diff can exceed the 1s throttle on large repos) would leave the
	// diff panel stale until the next filesystem event or a page refresh.
	var scheduleDiff func()
	scheduleDiff = func() {
		ww.mu.Lock()
		if ww.inFlight[taskID] {
			ww.throttles[taskID] = time.AfterFunc(250*time.Millisecond, scheduleDiff)
			ww.mu.Unlock()
			return
		}
		delete(ww.throttles, taskID)
		ww.inFlight[taskID] = true
		ww.mu.Unlock()

		select {
		case ww.jobCh <- diffJob{taskID: taskID, projectID: projectID, baseRef: baseRef}:
		default:
			ww.mu.Lock()
			delete(ww.inFlight, taskID)
			ww.mu.Unlock()
		}
	}
	ww.throttles[taskID] = time.AfterFunc(1000*time.Millisecond, scheduleDiff)
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
			ww.computeAndBroadcast(job.taskID, job.projectID, job.baseRef)
		case <-ww.doneCh:
			return
		}
	}
}

func (ww *WorktreeWatcher) computeAndBroadcast(taskID, projectID, baseRef string) {
	defer func() {
		ww.mu.Lock()
		delete(ww.inFlight, taskID)
		ww.mu.Unlock()
	}()

	task, err := ww.worktreeMgr.GetTaskByID(taskID)
	headRef := ""
	if err == nil && task.Status == types.StatusDone {
		headRef = task.BranchTip
		if headRef == "" {
			headRef = task.Branch
		}
	}
	diffResult, err := ww.worktreeMgr.GetTaskDiff(taskID, baseRef, headRef)
	if err != nil {
		return
	}

	trackedN, untrackedN := countManifestFiles(diffResult)
	filesN := trackedN + untrackedN

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
	if ww.updateCache != nil {
		ww.updateCache(taskID, diffResult)
	}

	// Check if task branch commits are already merged into the target branch.
	// Uses the target branch name (BaseRef), not the fixed BaseCommit SHA,
	// so the check reflects the current state of the target branch.
	ww.mu.Lock()
	worktreePath := ww.worktrees[taskID].path
	ww.mu.Unlock()
	targetBranch := task.BaseRef
	if strings.HasPrefix(targetBranch, "fatal:") {
		targetBranch = ""
	}
	alreadyMerged := targetBranch != "" && task.Branch != "" &&
		ww.worktreeMgr.IsMergedInto(worktreePath, task.Branch, targetBranch)

	if diffResult.Diff != "" || untrackedN > 0 || diffResult.CommittedDiff != "" {
		untrackedLines := countVisibleUntrackedLines(diffResult.Untracked)
		trackedAdd, trackedDel := countManifestChanges(diffResult)
		ww.broadcast(DiffStatsEvent{
			ProjectID:           projectID,
			TaskID:              taskID,
			DiffHash:            hash,
			ShortStat:           diffResult.ShortStat,
			FilesN:              filesN,
			UntrackedN:          untrackedN,
			UntrackedLines:      untrackedLines,
			TrackedAdd:          trackedAdd,
			TrackedDel:          trackedDel,
			ChangedHashes:       changedHashes,
			RemovedFiles:        removedFiles,
			StagedPaths:         diffResult.StagedPaths,
			HasCommittedChanges: diffResult.CommittedDiff != "",
			AlreadyMerged:       alreadyMerged,
		})
		log.Printf("[watcher] task=%s diff changed: %s (untracked: %d files)", taskID, diffResult.ShortStat, untrackedN)
	} else if len(changedHashes) > 0 || len(removedFiles) > 0 {
		ww.broadcast(DiffStatsEvent{
			ProjectID:           projectID,
			TaskID:              taskID,
			ChangedHashes:       changedHashes,
			RemovedFiles:        removedFiles,
			StagedPaths:         diffResult.StagedPaths,
			HasCommittedChanges: diffResult.CommittedDiff != "",
			AlreadyMerged:       alreadyMerged,
		})
	}
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

// countManifestChanges computes total additions/deletions from the truncated manifest,
// excluding internal files. This matches what the diff panel displays.
func countManifestChanges(d *git.TaskDiff) (add, del int) {
	for path, f := range d.Tracked {
		if git.IsInternalFile(path) {
			continue
		}
		add += f.Added
		del += f.Removed
	}
	for _, u := range d.Untracked {
		if git.IsInternalFile(u.Path) {
			continue
		}
		if u.OrigAdded > 0 {
			add += u.OrigAdded
		} else {
			add += len(u.Lines)
		}
	}
	return
}

// countManifestFiles counts visible files in the manifest (excluding internal files).
// This matches the file count shown in the diff panel.
func countManifestFiles(d *git.TaskDiff) (tracked, untracked int) {
	for path := range d.Tracked {
		if !git.IsInternalFile(path) {
			tracked++
		}
	}
	for _, u := range d.Untracked {
		if !git.IsInternalFile(u.Path) {
			untracked++
		}
	}
	return
}

// countVisibleUntrackedLines counts lines in untracked files, excluding internal files.
func countVisibleUntrackedLines(untracked []git.UntrackedFile) int {
	total := 0
	for _, u := range untracked {
		if git.IsInternalFile(u.Path) {
			continue
		}
		if u.OrigAdded > 0 {
			total += u.OrigAdded
		} else {
			total += len(u.Lines)
		}
	}
	return total
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

	cmd := exec.Command("git", "check-ignore", "--stdin")
	cmd.Dir = repoRoot
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
	case "HEAD", "commondir", "gitdir", "config", "options", "logs", "refs", "objects":
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
