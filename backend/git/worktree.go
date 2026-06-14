package git

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
func (wm *WorktreeManager) CreateWorktree(projectID, taskID, branchName string) (string, error) {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return "", fmt.Errorf("get project: %w", err)
	}

	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return "", fmt.Errorf("get config: %w", err)
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
		return "", fmt.Errorf("timeout waiting for worktree lock (another task is initializing)")
	}
	defer mu.Unlock()

	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create worktrees dir: %w", err)
	}

	if _, err := os.Stat(worktreePath); err == nil {
		return worktreePath, nil
	}

	baselineBranch := fmt.Sprintf("__kanban_baseline_%s", taskID)
	originalBranch, _ := wm.gitCurrentBranch(project.Path)

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
		return "", fmt.Errorf("check working tree: %w", err)
	}

	wm.gitRun(project.Path, "branch", "-D", baselineBranch)

	if hasChanges {
		if err := wm.gitRun(project.Path, "stash", "push", "-m", "kanban baseline stash"); err != nil {
			return "", fmt.Errorf("stash changes: %w", err)
		}
		defer func() {
			wm.gitRun(project.Path, "stash", "pop")
		}()

		if err := wm.gitRun(project.Path, "branch", baselineBranch); err != nil {
			return "", fmt.Errorf("create baseline branch: %w", err)
		}
		defer cleanup()

		if err := wm.gitRun(project.Path, "checkout", baselineBranch); err != nil {
			return "", fmt.Errorf("checkout baseline: %w", err)
		}

		if err := wm.gitRun(project.Path, "stash", "pop"); err != nil {
			wm.gitRun(project.Path, "stash", "drop")
		}

		wm.gitRun(project.Path, "add", "-A")
		wm.gitRun(project.Path, "commit", "-m", fmt.Sprintf("Baseline for task %s", taskID))

		if originalBranch != "" {
			wm.gitRun(project.Path, "checkout", originalBranch)
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
		return "", fmt.Errorf("create worktree: %s: %w", string(out), err)
	}

	if out, err := wm.gitRunOutput(worktreePath, "checkout", "-B", branchName); err != nil {
		wm.gitRun(project.Path, "worktree", "remove", "--force", worktreePath)
		return "", fmt.Errorf("create branch: %s: %w", out, err)
	}

	wm.gitRun(worktreePath, "add", "-A")
	wm.gitRun(worktreePath, "commit", "-m", "Initial baseline snapshot", "--allow-empty")

	wm.gitRun(project.Path, "branch", "-D", baselineBranch)

	return worktreePath, nil
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
	}

	if dryRun {
		log.Printf("[gc] dry-run complete: %d orphaned worktrees would be removed", removed)
	} else {
		log.Printf("[gc] complete: %d orphaned worktrees removed", removed)
	}

	return removed, nil
}

// GitShortstat returns a snapshot of the worktree's change state for polling.
// Combines tracked file changes (git diff HEAD --shortstat) with a hash of untracked
// file paths + sizes. This detects both content changes to existing files AND
// new/modified/deleted untracked files.
func (wm *WorktreeManager) GitShortstat(taskID string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree not found")
	}

	// Tracked file changes (line counts — changes when content changes)
	trackedOut, err := wm.gitRunOutput(worktreePath, "diff", "HEAD", "--shortstat")
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

// GetDiff returns the git diff for a task's worktree.
func (wm *WorktreeManager) GetDiff(taskID string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", fmt.Errorf("worktree not found")
	}

	out, err := wm.gitRunOutput(worktreePath, "diff", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return out, nil
}

// GetTaskDiff returns the git diff and untracked files for a task's worktree.
func (wm *WorktreeManager) GetTaskDiff(taskID string) (*TaskDiff, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree not found")
	}

	// Get tracked file diff, filtering out large/binary files
	diffOut, err := wm.gitRunOutput(worktreePath, "diff", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	diffOut = wm.filterLargeBinaryDiffs(worktreePath, diffOut)

	// Get shortstat summary
	shortStat, _ := wm.gitRunOutput(worktreePath, "diff", "--shortstat", "HEAD")

	// Get untracked files from git status --porcelain (lines starting with "?? ")
	statusOut, err := wm.gitRunOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}

	var untracked []UntrackedFile
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

	return &TaskDiff{Diff: diffOut, Untracked: untracked, ShortStat: shortStat}, nil
}

// GetDiffStats returns the git diff shortstat, untracked file count, and untracked line count for a task.
func (wm *WorktreeManager) GetDiffStats(taskID string) (shortStat string, untrackedN int, untrackedLines int, err error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", 0, 0, err
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", 0, 0, fmt.Errorf("worktree not found")
	}
	shortStat, _ = wm.gitRunOutput(worktreePath, "diff", "--shortstat", "HEAD")
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
