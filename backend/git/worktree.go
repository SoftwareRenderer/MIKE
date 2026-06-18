package git

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kanban/backend/config"
	"kanban/backend/store"
)

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	store        *store.Store
	configStore  *config.ConfigStore
	worktreeMu   sync.Map // per-project mutex for worktree creation
	stashMu      sync.Map // per-project mutex for stash operations

}

// NewWorktreeManager creates a new WorktreeManager.
func NewWorktreeManager(store *store.Store, configStore *config.ConfigStore) *WorktreeManager {
	return &WorktreeManager{store: store, configStore: configStore}
}

// getProjectWorktreeMu returns a per-project mutex for serializing worktree creation.
func (wm *WorktreeManager) getProjectWorktreeMu(projectID string) *sync.Mutex {
	mu, _ := wm.worktreeMu.LoadOrStore(projectID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// GetProjectStashMu returns a per-project mutex for serializing stash operations.
// Stash is repo-wide, so concurrent stash operations from different worktrees must be serialized.
func (wm *WorktreeManager) GetProjectStashMu(projectID string) *sync.Mutex {
	mu, _ := wm.stashMu.LoadOrStore(projectID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// WorktreePath returns the filesystem path for a task's worktree.
// Respects WORKTREES_DIR env var override (same as CreateWorktree).
func (wm *WorktreeManager) WorktreePath(taskID string) (string, error) {
	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return "", err
	}
	worktreesDir := cfg.WorkspaceDir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		worktreesDir = wtDir
	}
	return filepath.Join(worktreesDir, taskID), nil
}

// CreateWorktree creates a git worktree for a task, capturing the current working tree state.
// Returns (worktreePath, originalBranch, error).
func (wm *WorktreeManager) CreateWorktree(projectID, taskID, branchName string) (string, string, error) {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return "", "", fmt.Errorf("get project: %w", err)
	}

	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return "", "", fmt.Errorf("get config: %w", err)
	}

	worktreesDir := cfg.WorkspaceDir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		worktreesDir = wtDir
	}
	if !filepath.IsAbs(worktreesDir) {
		abs, err := filepath.Abs(worktreesDir)
		if err == nil {
			worktreesDir = abs
		}
	}
	worktreePath := filepath.Join(worktreesDir, taskID)

	// Acquire per-project mutex with timeout
	mu := wm.getProjectWorktreeMu(projectID)
	done := make(chan struct{})
	go func() {
		mu.Lock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		return "", "", fmt.Errorf("timeout waiting for worktree lock (another task is initializing)")
	}
	defer mu.Unlock()

	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create worktrees dir: %w", err)
	}

	baselineBranch := fmt.Sprintf("__kanban_baseline_%s", taskID)
	originalBranch, _ := wm.gitCurrentBranch(project.Path)

	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, originalBranch, nil
	}

	// Save the originating branch to the project (best-effort)
	if originalBranch != "" && !strings.HasPrefix(originalBranch, "fatal:") {
		_ = wm.store.UpdateProjectOriginBranch(projectID, originalBranch)
	}

	cleanup := func() {
		if originalBranch != "" {
			wm.gitRun(project.Path, "checkout", originalBranch)
		}
		wm.gitRun(project.Path, "branch", "-D", baselineBranch)
	}

	hasChanges, err := wm.hasWorkingTreeChanges(project.Path)
	if err != nil {
		return "", "", fmt.Errorf("check working tree: %w", err)
	}

	wm.gitRun(project.Path, "branch", "-D", baselineBranch)

	if hasChanges {
		// Create baseline branch from current HEAD
		if err := wm.gitRun(project.Path, "branch", baselineBranch); err != nil {
			return "", "", fmt.Errorf("create baseline branch: %w", err)
		}
		defer cleanup()

		// Stash changes, checkout baseline, restore changes, commit
		stashErr := wm.gitRun(project.Path, "stash", "push", "-u", "-m", "kanban baseline stash")
		if stashErr != nil {
			// Can't stash — create baseline from HEAD without working tree changes
			log.Printf("[worktree] task=%s stash failed during baseline creation, using HEAD: %v", taskID, stashErr)
		} else {
			wm.gitRun(project.Path, "checkout", "-f", baselineBranch)
			if err := wm.gitRun(project.Path, "stash", "pop"); err != nil {
				wm.gitRun(project.Path, "stash", "drop")
			}
			wm.gitRun(project.Path, "add", "-A")
			wm.gitRun(project.Path, "commit", "--allow-empty", "-m", fmt.Sprintf("Baseline for task %s", taskID))
		}

		if originalBranch != "" {
			wm.gitRun(project.Path, "checkout", "-f", originalBranch)
		}
	}

	sourceRef := baselineBranch
	if !hasChanges {
		sourceRef = "HEAD"
		defer cleanup()
	}

	cmd := exec.Command("git", "worktree", "add", "--detach", worktreePath, sourceRef)
	cmd.Dir = project.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("create worktree: %s: %w", string(out), err)
	}

	if out, err := wm.gitRunOutput(worktreePath, "checkout", "-B", branchName); err != nil {
		wm.gitRun(project.Path, "worktree", "remove", "--force", worktreePath)
		return "", "", fmt.Errorf("create branch: %s: %w", out, err)
	}

	wm.gitRun(project.Path, "branch", "-D", baselineBranch)

	// Resolve sourceRef to a commit hash so the base ref doesn't move after merges
	baseCommit, _ := wm.gitRunOutput(worktreePath, "rev-parse", "--verify", sourceRef)
	baseCommit = strings.TrimSpace(baseCommit)

	return worktreePath, baseCommit, nil
}

// RemoveWorktree removes a task's worktree and its branch.
func (wm *WorktreeManager) RemoveWorktree(projectID, taskID, branchName string) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree dir: %w", err)
	}

	wm.gitRun(project.Path, "branch", "-D", branchName)
	wm.gitRun(project.Path, "worktree", "prune")

	return nil
}

// RemoveWorktreeDir removes a task's worktree directory and prunes git worktree tracking.
func (wm *WorktreeManager) RemoveWorktreeDir(projectID, taskID string) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree dir: %w", err)
	}

	wm.gitRun(project.Path, "worktree", "prune")

	return nil
}

// GarbageCollect removes worktree directories that have no matching task in the database.
func (wm *WorktreeManager) GarbageCollect(dryRun bool) (int, error) {
	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return 0, fmt.Errorf("get config: %w", err)
	}

	worktreesDir := cfg.WorkspaceDir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		worktreesDir = wtDir
	}
	if !filepath.IsAbs(worktreesDir) {
		if abs, err := filepath.Abs(worktreesDir); err == nil {
			worktreesDir = abs
		}
	}

	if _, err := os.Stat(worktreesDir); os.IsNotExist(err) {
		return 0, nil
	}

	activeTaskIDs, err := wm.store.GetAllTaskIDs()
	if err != nil {
		return 0, fmt.Errorf("get task IDs: %w", err)
	}
	activeSet := make(map[string]bool, len(activeTaskIDs))
	for _, id := range activeTaskIDs {
		activeSet[id] = true
	}

	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return 0, fmt.Errorf("read worktrees dir: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name[0] == '.' {
			continue
		}
		if !activeSet[name] {
			orphanPath := filepath.Join(worktreesDir, name)
			if dryRun {
				log.Printf("[gc] would remove orphaned worktree: %s", orphanPath)
				removed++
			} else {
				log.Printf("[gc] removing orphaned worktree: %s", orphanPath)
				if err := os.RemoveAll(orphanPath); err != nil {
					log.Printf("[gc] error removing %s: %v", orphanPath, err)
				} else {
					removed++
				}
			}
		}
	}

	projects, err := wm.store.ListProjects()
	if err != nil {
		log.Printf("[gc] error listing projects for prune: %v", err)
		return removed, nil
	}
	for _, p := range projects {
		wm.gitRun(p.Path, "worktree", "prune")
		// Prune orphaned task branches (task/<id> with no matching task in DB)
		branchesOut, err := wm.gitRunOutput(p.Path, "branch", "--list", "task/*")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(branchesOut, "\n") {
			branch := strings.TrimSpace(line)
			if branch == "" || !strings.HasPrefix(branch, "task/") {
				continue
			}
			taskID := strings.TrimPrefix(branch, "task/")
			if !activeSet[taskID] {
				if dryRun {
					log.Printf("[gc] would delete orphaned branch %s in project %s", branch, p.Name)
				} else {
					log.Printf("[gc] deleting orphaned branch %s in project %s", branch, p.Name)
					wm.gitRun(p.Path, "branch", "-D", branch)
				}
			}
		}
	}

	if dryRun {
		log.Printf("[gc] dry-run complete: %d orphaned worktrees would be removed", removed)
	} else {
		log.Printf("[gc] complete: %d orphaned worktrees removed", removed)
	}

	return removed, nil
}

// GitShortstat returns a snapshot of the worktree's change state for polling.
// Combines tracked file changes (git diff <baseRef> --shortstat) with a hash of untracked
// file paths + sizes. This detects both content changes to existing files AND
// new/modified/deleted untracked files.
func (wm *WorktreeManager) GitShortstat(taskID, baseRef string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree not found")
	}

	target := "HEAD"
	if baseRef != "" {
		target = baseRef
	}

	// Tracked file changes (line counts — changes when content changes)
	trackedOut, err := wm.gitRunOutput(worktreePath, "diff", target, "--shortstat")
	if err != nil {
		return "", fmt.Errorf("git diff --shortstat: %w", err)
	}

	// Untracked files: list path + size to detect new/modified/deleted untracked files.
	// git ls-files gives us the paths; we append file size as a content-change proxy.
	untrackedOut, err := wm.gitRunOutput(worktreePath, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}
	untrackedSnap := wm.buildUntrackedSnapshot(worktreePath, untrackedOut)

	// Combine into a single snapshot string
	return fmt.Sprintf("%s|%s", strings.TrimSpace(trackedOut), untrackedSnap), nil
}

// buildUntrackedSnapshot creates a deterministic string from untracked file paths and sizes.
// When any untracked file is created, deleted, or modified (size changes), this string changes.
func (wm *WorktreeManager) buildUntrackedSnapshot(worktreePath, untrackedList string) string {
	var parts []string
	for _, line := range strings.Split(untrackedList, "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		fullPath := filepath.Join(worktreePath, path)
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d", path, info.Size()))
	}
	return strings.Join(parts, "\n")
}

// GitStatus returns the git status --porcelain output for a task's worktree.
func (wm *WorktreeManager) GitStatus(taskID string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree not found")
	}

	out, err := wm.gitRunOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return out, nil
}

// UntrackedFile holds the path and unified diff content for an untracked file.
type UntrackedFile struct {
	Path     string `json:"path"`
	Diff     string `json:"diff"`
	FileSize int64  `json:"file_size,omitempty"` // set when file is binary or >1MB (diff omitted)
}

// TaskDiff holds the diff output and list of untracked files for a task.
type TaskDiff struct {
	Diff      string          `json:"diff"`
	Untracked []UntrackedFile `json:"untracked"`
	ShortStat string          `json:"short_stat"`
}

// GetDiff returns the git diff for a task's worktree relative to baseRef (or HEAD if empty).
func (wm *WorktreeManager) GetDiff(taskID, baseRef string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree not found")
	}

	target := "HEAD"
	if baseRef != "" {
		target = baseRef
	}
	out, err := wm.gitRunOutput(worktreePath, "diff", target)
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return out, nil
}

// GetTaskDiff returns the git diff and untracked files for a task's worktree.
// DropStashEntriesForBranch removes stash entries that belong to the given branch.
// Stash entries are labeled with the branch name (e.g., "On task/abc-def-ghi: label").
func (wm *WorktreeManager) DropStashEntriesForBranch(projectID string, branchName string) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	// List stash entries with their subjects
	listOut, err := wm.gitRunOutput(project.Path, "stash", "list", "--format=%gd%x00%s")
	if err != nil {
		return nil // no stash, nothing to clean
	}

	branchPrefix := "On " + branchName + ":"
	// Find stash indices that belong to this branch (newest first)
	var dropIndices []int
	for _, line := range strings.Split(listOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 2)
		if len(parts) < 2 {
			continue
		}
		subject := parts[1]
		if !strings.HasPrefix(subject, branchPrefix) {
			continue
		}
		// Extract index from stash@{N}
		if match := stashIndexRe.FindStringSubmatch(parts[0]); match != nil {
			if idx, err := strconv.Atoi(match[1]); err == nil {
				dropIndices = append(dropIndices, idx)
			}
		}
	}

	// Drop from highest index to lowest (git renumbers after each drop)
	for i := len(dropIndices) - 1; i >= 0; i-- {
		_ = wm.gitRun(project.Path, "stash", "drop", fmt.Sprintf("stash@{%d}", dropIndices[i]))
	}

	return nil
}

// GetTaskDiff returns the git diff for a task's worktree.
// baseRef is the branch/ref to diff against (e.g., "master"). If empty, uses HEAD.
// headRef is an optional second ref for comparing two commits (git diff baseRef..headRef).
// When headRef is set, untracked files are omitted (commit-to-commit diff).
func (wm *WorktreeManager) GetTaskDiff(taskID, baseRef, headRef string) (*TaskDiff, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree not found")
	}

	var diffOut, shortStat string
	var untracked []UntrackedFile

	if headRef != "" {
		// Two-ref diff: compare baseRef..headRef (commit-to-commit)
		target := baseRef
		if target == "" {
			target = "HEAD"
		}
		diffOut, err = wm.gitRunOutput(worktreePath, "diff", fmt.Sprintf("%s..%s", target, headRef))
		if err != nil {
			return nil, fmt.Errorf("git diff: %w", err)
		}
		diffOut = wm.filterLargeBinaryDiffs(worktreePath, diffOut)
		shortStat, _ = wm.gitRunOutput(worktreePath, "diff", "--shortstat", fmt.Sprintf("%s..%s", target, headRef))
		// No untracked files for commit-to-commit diff
	} else {
		// Single-ref diff: working tree vs baseRef.
		// Checkpoints use stash create+store, so changes stay visible in the working tree.
		target := "HEAD"
		if baseRef != "" {
			target = baseRef
		}

		// Get tracked file diff, filtering out large/binary files
		diffOut, err = wm.gitRunOutput(worktreePath, "diff", target)
		if err != nil {
			return nil, fmt.Errorf("git diff: %w", err)
		}

		// If working tree is clean, fall back to committed changes (HEAD vs target)
		// This handles forked threads where changes are committed as snapshots
		if len(strings.TrimSpace(diffOut)) == 0 {
			diffOut, err = wm.gitRunOutput(worktreePath, "diff", fmt.Sprintf("%s..HEAD", target))
			if err != nil {
				return nil, fmt.Errorf("git diff: %w", err)
			}
			shortStat, _ = wm.gitRunOutput(worktreePath, "diff", "--shortstat", fmt.Sprintf("%s..HEAD", target))
		} else {
			shortStat, _ = wm.gitRunOutput(worktreePath, "diff", "--shortstat", target)
		}

		diffOut = wm.filterLargeBinaryDiffs(worktreePath, diffOut)

		// Get untracked files from git status --porcelain (lines starting with "?? ")
		statusOut, err := wm.gitRunOutput(worktreePath, "status", "--porcelain")
		if err != nil {
			return nil, fmt.Errorf("git status: %w", err)
		}

		const maxDiffSize = 1 << 20 // 1MB
		for _, line := range strings.Split(statusOut, "\n") {
			if len(line) >= 4 && strings.HasPrefix(line, "?? ") {
				path := strings.TrimSpace(line[3:])
				if path == "" {
					continue
				}
				fullPath := filepath.Join(worktreePath, path)
				info, err := os.Stat(fullPath)
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				fileSize := info.Size()
				// Skip diff for large files or binary files
				if fileSize > maxDiffSize || isBinaryFile(fullPath, fileSize) {
					untracked = append(untracked, UntrackedFile{Path: path, FileSize: fileSize})
					continue
				}
				// Generate unified diff for the untracked file (compare against empty)
				// git diff --no-index returns exit code 1 when files differ, which is expected
				fileDiff, _ := wm.gitRunOutput(worktreePath, "diff", "--no-index", "/dev/null", path)
				untracked = append(untracked, UntrackedFile{Path: path, Diff: fileDiff})
			}
		}
	}

	return &TaskDiff{Diff: diffOut, Untracked: untracked, ShortStat: shortStat}, nil
}

// GetDiffStats returns the git diff shortstat, untracked file count, and untracked line count for a task.
// baseRef is the branch/ref to diff against (e.g., "master"). If empty, uses HEAD.
// Checkpoints commit to the branch, so changes are always visible in the working tree.
func (wm *WorktreeManager) GetDiffStats(taskID, baseRef string) (shortStat string, untrackedN int, untrackedLines int, err error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", 0, 0, err
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", 0, 0, fmt.Errorf("worktree not found")
	}
	target := "HEAD"
	if baseRef != "" {
		target = baseRef
	}
	shortStat, _ = wm.gitRunOutput(worktreePath, "diff", "--shortstat", target)
	// If working tree is clean, fall back to committed changes
	if shortStat == "" {
		shortStat, _ = wm.gitRunOutput(worktreePath, "diff", "--shortstat", fmt.Sprintf("%s..HEAD", target))
	}
	statusOut, _ := wm.gitRunOutput(worktreePath, "status", "--porcelain")
	for _, line := range strings.Split(statusOut, "\n") {
		if len(line) >= 4 && strings.HasPrefix(line, "?? ") {
			path := strings.TrimSpace(line[3:])
			if path != "" {
				untrackedN++
				// Count lines in new (untracked) files
				fullPath := filepath.Join(worktreePath, path)
				if data, err := os.ReadFile(fullPath); err == nil {
					untrackedLines += strings.Count(string(data), "\n")
				}
			}
		}
	}
	return shortStat, untrackedN, untrackedLines, nil
}

// -- Helper functions --

// filterLargeBinaryDiffs replaces diff hunks for binary or >1MB files with a size annotation.
func (wm *WorktreeManager) filterLargeBinaryDiffs(worktreePath, diffText string) string {
	if diffText == "" {
		return diffText
	}
	const maxDiffSize = 1 << 20 // 1MB
	var result strings.Builder
	lines := strings.Split(diffText, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		// Detect file header: "diff --git a/path b/path"
		if strings.HasPrefix(line, "diff --git ") {
			// Extract file path from "diff --git a/X b/Y"
			path := extractDiffPath(line)
			fullPath := filepath.Join(worktreePath, path)
			info, err := os.Stat(fullPath)
			isLarge := err == nil && info.Mode().IsRegular() && (info.Size() > maxDiffSize || isBinaryFile(fullPath, info.Size()))

			// Copy header lines until we hit the hunk or next file
			for i < len(lines) {
				l := lines[i]
				if strings.HasPrefix(l, "@@ ") || strings.HasPrefix(l, "diff --git ") {
					break
				}
				result.WriteString(l + "\n")
				i++
			}

			// Replace hunk(s) for this file if large/binary
			if isLarge {
				fileSize := info.Size()
				result.WriteString(fmt.Sprintf("# binary or large file: %d bytes\n", fileSize))
				// Skip all hunk lines for this file
				for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
					i++
				}
				continue
			}
		}
		result.WriteString(line + "\n")
		i++
	}
	// Remove trailing newline that Split+Join adds
	out := result.String()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out
}

// extractDiffPath pulls the file path from "diff --git a/X b/Y" header.
func extractDiffPath(line string) string {
	// Format: "diff --git a/path b/path" or "diff --git a/path b/path\n"
	line = strings.TrimPrefix(line, "diff --git ")
	// Take the "a/..." part
	parts := strings.SplitN(line, " ", 2)
	if len(parts) >= 1 && strings.HasPrefix(parts[0], "a/") {
		return strings.TrimPrefix(parts[0], "a/")
	}
	return ""
}

// isBinaryFile checks if a file is binary by scanning the first 8KB for null bytes.
func isBinaryFile(path string, size int64) bool {
	if size == 0 {
		return false
	}
	limit := int64(8192)
	if size < limit {
		limit = size
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

func (wm *WorktreeManager) hasWorkingTreeChanges(dir string) (bool, error) {
	out, err := wm.gitRunOutput(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(out)) > 0, nil
}

func (wm *WorktreeManager) gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(dir, ".git"))
	_, err := cmd.CombinedOutput()
	return err
}

func (wm *WorktreeManager) gitRunOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (wm *WorktreeManager) gitCurrentBranch(dir string) (string, error) {
	out, err := wm.gitRunOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out), err
}

// GitRunInWorktree runs a git command in the task's worktree directory.
func (wm *WorktreeManager) GitRunInWorktree(taskID string, args ...string) error {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return fmt.Errorf("worktree not found")
	}
	return wm.gitRun(worktreePath, args...)
}

// GitRunInWorktreeOutput runs a git command in the task's worktree directory and returns its output.
func (wm *WorktreeManager) GitRunInWorktreeOutput(taskID string, args ...string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree not found")
	}
	return wm.gitRunOutput(worktreePath, args...)
}

// GitRunInProject runs a git command in the project's main directory.
func (wm *WorktreeManager) GitRunInProject(projectID string, args ...string) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	return wm.gitRun(project.Path, args...)
}

// GitRunInProjectOutput runs a git command in the project's main directory and returns its output.
func (wm *WorktreeManager) GitRunInProjectOutput(projectID string, args ...string) (string, error) {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return "", fmt.Errorf("get project: %w", err)
	}
	return wm.gitRunOutput(project.Path, args...)
}

// TaskCommit represents a single commit in the task's worktree history.
type TaskCommit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
}

// TaskCommitsResult holds the base commit info and list of task-specific commits.
type TaskCommitsResult struct {
	BaseCommit *TaskCommit    `json:"base_commit,omitempty"` // commit at the divergence point
	Commits    []TaskCommit   `json:"commits"`
}

// GetTaskCommits returns the commit log for a task's worktree branch.
// If baseRef is provided, only commits after the divergence from baseRef are returned (baseRef..HEAD).
// Also returns the commit at baseRef as BaseCommit for display purposes.
func (wm *WorktreeManager) GetTaskCommits(taskID, baseRef string) (*TaskCommitsResult, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree not found")
	}

	result := &TaskCommitsResult{Commits: []TaskCommit{}}

	// Get the base commit info (the commit at the divergence point)
	if baseRef != "" {
		baseOut, err := wm.gitRunOutput(worktreePath, "log", "--format=%H|%h|%s|%aI", "-1", baseRef)
		if err == nil {
			line := strings.TrimSpace(baseOut)
			parts := strings.SplitN(line, "|", 4)
			if len(parts) == 4 {
				result.BaseCommit = &TaskCommit{
					Hash:    parts[0],
					Short:   parts[1],
					Subject: parts[2],
					Date:    parts[3],
				}
			}
		}
	}

	// Format: hash|short|subject|date (ISO 8601)
	// Use baseRef..HEAD to only show commits made since the original branch
	ref := "HEAD"
	if baseRef != "" {
		ref = baseRef + "..HEAD"
	}
	out, err := wm.gitRunOutput(worktreePath, "log", "--format=%H|%h|%s|%aI", ref)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		// Skip auto-generated bookkeeping commits (not meaningful task work)
		subject := parts[2]
		if subject == "Thread switch snapshot" || subject == "Fork snapshot" {
			continue
		}
		result.Commits = append(result.Commits, TaskCommit{
			Hash:    parts[0],
			Short:   parts[1],
			Subject: parts[2],
			Date:    parts[3],
		})
	}
	return result, nil
}

// ResolveDiffBaseline computes the effective git ref to diff against for a task.
// For the original thread, returns baseRef (e.g., "master").
// For a forked thread, returns the merge-base between forkBranch and taskBranch.
// If forkBranch == taskBranch (task branch was switched to thread branch),
// returns baseRef (the original baseline) since all changes are on the thread branch.
func (wm *WorktreeManager) ResolveDiffBaseline(taskID, baseRef, taskBranch, forkBranch string) (string, error) {
	if forkBranch == "" {
		// Original thread — diff against base_ref
		return baseRef, nil
	}
	if forkBranch == taskBranch {
		// Task branch was switched to thread branch — diff against original baseline
		return baseRef, nil
	}
	// Forked thread — find the divergence point via merge-base
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return baseRef, err
	}
	mb, err := wm.gitRunOutput(worktreePath, "merge-base", forkBranch, taskBranch)
	if err != nil {
		return baseRef, fmt.Errorf("git merge-base: %w", err)
	}
	return strings.TrimSpace(mb), nil
}

// ResetWorktreeTo resets the worktree to a specific commit or stash hash.
// For branch commits, uses git reset --hard.
// For stash hashes, cleans the working tree and applies the stash patch.
func (wm *WorktreeManager) ResetWorktreeTo(taskID, hash string) error {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}

	// Try regular reset first (works for branch commits)
	if err := wm.gitRun(worktreePath, "reset", "--hard", hash); err == nil {
		return nil
	}

	// Fall back to stash approach: clean working tree + apply stash patch
	// Clean the working tree
	_ = wm.gitRun(worktreePath, "checkout", "-f", "HEAD")
	_ = wm.gitRun(worktreePath, "clean", "-fd")

	// Apply the stash patch
	stashPatch, err := wm.gitRunOutput(worktreePath, "stash", "show", "-p", hash)
	if err != nil {
		return fmt.Errorf("stash show failed: %w", err)
	}
	if len(strings.TrimSpace(stashPatch)) == 0 {
		return nil // empty stash, nothing to apply
	}

	// Use stdin to pipe the patch into git apply
	cmd := exec.Command("git", "apply")
	cmd.Dir = worktreePath
	cmd.Stdin = strings.NewReader(stashPatch)
	_, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git apply stash failed: %w", err)
	}
	return nil
}

// ResolveStashParent finds the state BEFORE a stash checkpoint's changes.
// Uses the previous stash entry (stash@{N+1} in list order) which contains
// accumulated changes from all prior checkpoints. Falls back to stash^
// (HEAD at stash time) if no previous entry exists.
// Only considers stash entries belonging to the same branch (task).
// Returns the original headRef if not found in stash list.
func (wm *WorktreeManager) ResolveStashParent(taskID, baseRef, headRef string) (string, error) {
	if headRef == "" {
		return "", nil
	}
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return headRef, err
	}

	// Get the task's branch name to filter stash entries
	task, err := wm.store.GetTaskByTaskID(taskID)
	if err != nil {
		return headRef, nil
	}
	// Stash entries from `git stash create` + `git stash store` have subject:
	// "WIP on <branch>: <hash> <message>" (commit subject)
	// Stash entries from `git stash push` have subject:
	// "On <branch>: <message>" (stash message)
	// Match either format.
	wipPrefix := "WIP on " + task.Branch + ":"
	onPrefix := "On " + task.Branch + ":"

	// List stash entries with subject: newest first (stash@{0}, stash@{1}, ...)
	listOut, err := wm.gitRunOutput(worktreePath, "stash", "list", "--format=%gd%x00%H%x00%s")
	if err != nil {
		return headRef, nil
	}

	// Parse stash list and find entries matching this branch.
	// Use null byte as separator between gd, H, and subject.
	var branchEntries []string // hashes belonging to this branch, newest first
	for _, line := range strings.Split(listOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) < 2 {
			continue
		}
		subject := ""
		if len(parts) > 2 {
			subject = parts[2]
		}
		if !strings.HasPrefix(subject, wipPrefix) && !strings.HasPrefix(subject, onPrefix) {
			continue // skip entries from other branches
		}
		branchEntries = append(branchEntries, strings.TrimSpace(parts[1]))
	}

	// Find headRef in branch entries and return the next one (previous checkpoint)
	for i, hash := range branchEntries {
		if hash == headRef {
			if i+1 < len(branchEntries) {
				return branchEntries[i+1], nil
			}
			// No previous entry — fall back to HEAD at stash time
			parent, err := wm.gitRunOutput(worktreePath, "rev-parse", headRef+"^")
			if err == nil {
				return strings.TrimSpace(parent), nil
			}
			break
		}
	}

	return headRef, nil
}

var stashIndexRe = regexp.MustCompile(`stash@\{(\d+)\}`)

