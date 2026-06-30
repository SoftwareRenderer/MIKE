package git

import (
	"crypto/sha256"
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

// ignoredDirs is a set of well-known directory names that should never be
// watched or shown in diffs. Covers directories found in virtually every
// .gitignore file.
var ignoredDirs = map[string]struct{}{
	"node_modules":  {},
	".next":         {},
	"dist":          {},
	"build":         {},
	".cache":        {},
	"__pycache__":   {},
	".venv":         {},
	"target":        {},
	"vendor":        {},
	".gradle":       {},
	".mypy_cache":   {},
	".pytest_cache": {},
	"coverage":      {},
	".nuxt":         {},
	".output":       {},
	".svelte-kit":   {},
	".turbo":        {},
	".vercel":       {},
	".docker":       {},
}

// IgnoredDirs returns the set of well-known directory names to always skip.
func IgnoredDirs() map[string]struct{} {
	return ignoredDirs
}

// ShouldIgnorePath returns true if any path component (directory name) in the
// given path matches a known ignored directory. Uses full component match only
// (e.g., "node_modules" matches "a/node_modules/b.js" but NOT "my_node_modules/b.js").
func ShouldIgnorePath(path string) bool {
	for component := range strings.SplitSeq(path, string(filepath.Separator)) {
		if _, ok := ignoredDirs[component]; ok {
			return true
		}
	}
	return false
}

// ShouldIgnoreFile returns true if the filename matches a known temporary/backup
// pattern that should not trigger diff broadcasts.
func ShouldIgnoreFile(path string) bool {
	base := filepath.Base(path)
	// Vim swap/undo files: .file.swp, .file.swo, .file.ssn, .file.uno
	if strings.HasPrefix(base, ".") && (strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, ".swo") || strings.HasSuffix(base, ".ssn") || strings.HasSuffix(base, ".uno")) {
		return true
	}
	// Emacs auto-save: #file# or .#file
	if strings.HasPrefix(base, "#") && strings.HasSuffix(base, "#") {
		return true
	}
	if strings.HasPrefix(base, ".#") {
		return true
	}
	// Backup files: file~ or file.bak
	if strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".bak") {
		return true
	}
	// OS metadata
	if base == ".DS_Store" || base == "Thumbs.db" || base == "desktop.ini" {
		return true
	}
	return false
}

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	store       *store.Store
	configStore *config.ConfigStore
	worktreeMu  sync.Map // per-project mutex for worktree creation
	stashMu     sync.Map // per-project mutex for stash operations

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
// If sourceBranch is non-empty, the worktree is created from that branch instead of HEAD.
// Returns (worktreePath, originalBranch, error).
func (wm *WorktreeManager) CreateWorktree(projectID, taskID, branchName, sourceBranch string) (string, string, error) {
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
		// Sanitize: reject git error messages as branch names
		if strings.HasPrefix(originalBranch, "fatal:") {
			originalBranch = ""
		}
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
	// If a source branch is specified (e.g., parent branch for child tasks), use it instead
	if sourceBranch != "" {
		sourceRef = sourceBranch
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

	// Always delete the branch from the project repo, even if worktree dir is gone
	wm.gitRun(project.Path, "branch", "-D", branchName)
	wm.gitRun(project.Path, "worktree", "prune")

	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree dir: %w", err)
	}

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
		for line := range strings.SplitSeq(branchesOut, "\n") {
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

// GitDiffRaw returns a snapshot of the worktree's change state for polling.
// Uses git diff --raw which outputs per-file blob hashes — any content change
// (even same-line same-character-count edits) produces a new hash.
// Appends untracked file paths + sizes with | separator.
// Returns empty string when there are no changes at all.
func (wm *WorktreeManager) GitDiffRaw(taskID, baseRef string) (string, error) {
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

	// Tracked file changes: --raw outputs blob hashes, any content change = new hash.
	// git diff exits 0 = no changes (empty output), 1 = changes (output on stdout).
	trackedOut, trackedErr := wm.gitRunOutput(worktreePath, "diff", "--raw", target)
	if trackedErr != nil {
		// Exit code 1 means files differ — output is valid, ignore the error.
		// Any other error (e.g., missing repo) is a real failure.
		if exitErr, ok := trackedErr.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				// Changes exist, trackedOut has the raw output — continue
			} else {
				return "", fmt.Errorf("git diff --raw: %w", trackedErr)
			}
		} else {
			return "", fmt.Errorf("git diff --raw: %w", trackedErr)
		}
	}
	trackedOut = strings.TrimSpace(trackedOut)

	// Untracked files
	untrackedOut, err := wm.gitRunOutput(worktreePath, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}
	untrackedSnap := wm.buildUntrackedSnapshot(worktreePath, untrackedOut)

	// Combine: "raw_output|untracked_snapshot"
	// Empty string when nothing changed (no tracked diffs, no untracked files).
	if trackedOut == "" && untrackedSnap == "" {
		return "", nil
	}
	return fmt.Sprintf("%s|%s", trackedOut, untrackedSnap), nil
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
	for line := range strings.SplitSeq(untrackedList, "\n") {
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

// walkUntrackedEntries expands git status --porcelain "?? " entries into individual file paths.
// When a whole directory is untracked, git status returns "?? dir/" — this function walks
// the directory and returns all regular file paths (relative to worktreePath).
func walkUntrackedEntries(worktreePath, path string) []string {
	fullPath := filepath.Join(worktreePath, path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil
	}
	if info.Mode().IsRegular() {
		if ShouldIgnoreFile(path) {
			return nil
		}
		return []string{path}
	}
	// It's a directory — walk it recursively
	var files []string
	_ = filepath.Walk(fullPath, func(fp string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(worktreePath, fp)
		if err != nil {
			return nil
		}
		if ShouldIgnoreFile(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files
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

// UntrackedFile holds the path and content lines for an untracked (new) file.
// Since untracked files are always new, we store raw lines instead of a unified diff.
type UntrackedFile struct {
	Path      string   `json:"path"`
	Lines     []string `json:"lines,omitempty"`      // file content lines (set for text files)
	LineCount int      `json:"line_count,omitempty"` // total line count (set when Lines is omitted)
	FileSize  int64    `json:"file_size,omitempty"`  // set when file is binary or >1MB (lines omitted)
	Truncated bool     `json:"truncated,omitempty"`  // true when lines were truncated to maxChangeLines
	OrigAdded int      `json:"orig_added,omitempty"` // original line count before truncation
}

// DiffRow represents a single line in a parsed diff.
type DiffRow struct {
	LineNumber *int                `json:"lineNumber"` // null for non-code rows
	Variant    string              `json:"variant"`    // "context", "added", "removed"
	Text       string              `json:"text,omitempty"` // omitted when segments present
	Segments   []InlineDiffSegment `json:"segments,omitempty"`
}

// InlineDiffSegment represents a word-level diff segment within a line.
type InlineDiffSegment struct {
	Key  string `json:"key"`
	Text string `json:"text"`
	Tone string `json:"tone"` // "added", "removed", "context"
}

// TrackedDiffFile holds the parsed diff for a single tracked file.
type TrackedDiffFile struct {
	Path      string    `json:"path"`
	Rows      []DiffRow `json:"rows"`
	Added     int       `json:"added"`
	Removed   int       `json:"removed"`
	FileSize  *int64    `json:"fileSize,omitempty"`  // set for binary/large files
	Truncated bool      `json:"truncated,omitempty"`
	OrigAdded int       `json:"origAdded,omitempty"`
	OrigRemoved int     `json:"origRemoved,omitempty"`
}

// TaskDiff holds the diff output and list of untracked files for a task.
// Tracked is a per-file map for incremental updates. Diff is the legacy
// monolithic unified diff string (kept for backward compatibility).
type TaskDiff struct {
	Diff              string                      `json:"diff"`
	Tracked           map[string]*TrackedDiffFile `json:"-"` // per-file parsed diffs (not JSON-serialized in legacy format)
	Untracked         []UntrackedFile             `json:"untracked"`
	ShortStat         string                      `json:"short_stat"`
	TruncatedFiles    []string                    `json:"truncated_files,omitempty"`
	OriginalFileStats map[string]FileChangeStats  `json:"original_file_stats,omitempty"`
}

// FileChangeStats holds the original added/removed line counts for a file.
type FileChangeStats struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// ComputeFileHash computes a SHA-256 hash for a tracked diff file based on its rows.
func ComputeFileHash(f *TrackedDiffFile) string {
	var sb strings.Builder
	for _, row := range f.Rows {
		fmt.Fprintf(&sb, "%s:%s\n", row.Variant, row.Text)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(sb.String())))
}

// ComputeUntrackedHash computes a SHA-256 hash for an untracked file based on its lines.
func ComputeUntrackedHash(u UntrackedFile) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(u.Path+strings.Join(u.Lines, "\n"))))
}

// GetPerFileHashes returns a map of file path → hash for all tracked and untracked files.
func (d *TaskDiff) GetPerFileHashes() map[string]string {
	hashes := make(map[string]string)
	for path, f := range d.Tracked {
		hashes[path] = ComputeFileHash(f)
	}
	for _, u := range d.Untracked {
		hashes[u.Path] = ComputeUntrackedHash(u)
	}
	return hashes
}

// Default maximum number of change lines per file before truncation.
const defaultMaxChangeLines = 60

// truncateDiffLines truncates a unified diff so each file has at most
// maxChangeLines change lines (lines starting with + or -).
// Within a hunk, lines are dropped from the end and the @@ header counts
// are updated to stay valid. Returns the truncated diff and list of
// truncated file paths.
func truncateDiffLines(diff string, maxChangeLines int) (string, []string) {
	if !strings.Contains(diff, "\n") {
		return diff, nil
	}

	lines := strings.Split(diff, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Split into per-file blocks
	type fileBlock struct {
		path  string
		lines []string
	}
	var blocks []fileBlock
	var cur *fileBlock

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			if cur != nil {
				blocks = append(blocks, *cur)
			}
			match := fileRe.FindStringSubmatch(line)
			path := "unknown"
			if match != nil {
				path = match[1]
			}
			cur = &fileBlock{path: path, lines: []string{line}}
			continue
		}
		if cur != nil {
			cur.lines = append(cur.lines, line)
		}
	}
	if cur != nil {
		blocks = append(blocks, *cur)
	}

	var result []string
	var truncatedFiles []string

	for _, block := range blocks {
		// Count total change lines
		totalChanges := 0
		for _, line := range block.lines {
			if len(line) > 0 && (line[0] == '+' || line[0] == '-') &&
				!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
				totalChanges++
			}
		}

		needsTruncation := totalChanges > maxChangeLines
		if needsTruncation {
			truncatedFiles = append(truncatedFiles, block.path)
		}

		if !needsTruncation {
			result = append(result, block.lines...)
			continue
		}

		// Truncate: walk lines, keep first maxChangeLines changes.
		// When limit is hit mid-hunk, update the @@ header counts and stop.
		keptChanges := 0
		inHunk := false
		var hunkResultIdx int
		var hunkOldStart, hunkNewStart int
		var hunkContext string
		var hunkOldLines, hunkNewLines int

		for _, line := range block.lines {
			if strings.HasPrefix(line, "@@") {
				// If we already hit the limit in a previous hunk, stop
				if keptChanges >= maxChangeLines {
					break
				}
				inHunk = true
				hunkResultIdx = len(result)
				hunkOldLines = 0
				hunkNewLines = 0
				if m := hunkRe.FindStringSubmatch(line); m != nil {
					hunkOldStart, _ = strconv.Atoi(m[1])
					hunkNewStart, _ = strconv.Atoi(m[3])
					hunkContext = m[5]
				}
				result = append(result, line)
				continue
			}
			if !inHunk {
				result = append(result, line)
				continue
			}

			// Track old/new line counts
			if len(line) > 0 {
				if line[0] == ' ' || line[0] == '-' {
					hunkOldLines++
				}
				if line[0] == ' ' || line[0] == '+' {
					hunkNewLines++
				}
			}

			isChange := len(line) > 0 && (line[0] == '+' || line[0] == '-')
			if isChange {
				keptChanges++
				if keptChanges > maxChangeLines {
					// Update the @@ header to reflect actual counts, then stop
					result[hunkResultIdx] = fmt.Sprintf("@@ -%d,%d +%d,%d %s@@",
						hunkOldStart, hunkOldLines, hunkNewStart, hunkNewLines, hunkContext)
					return strings.Join(result, "\n"), truncatedFiles
				}
			}
			result = append(result, line)
		}

		// Update header for last hunk (counts may differ from original)
		if inHunk {
			result[hunkResultIdx] = fmt.Sprintf("@@ -%d,%d +%d,%d %s@@",
				hunkOldStart, hunkOldLines, hunkNewStart, hunkNewLines, hunkContext)
		}
	}

	return strings.Join(result, "\n"), truncatedFiles
}

// countFileChanges scans a unified diff and returns per-file added/removed counts.
func countFileChanges(diff string) map[string]FileChangeStats {
	stats := make(map[string]FileChangeStats)
	if !strings.Contains(diff, "\n") {
		return stats
	}

	var currentPath string
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			match := fileRe.FindStringSubmatch(line)
			currentPath = "unknown"
			if match != nil {
				currentPath = match[1]
			}
			continue
		}
		if currentPath == "" {
			continue
		}
		if len(line) > 0 && line[0] == '+' && !strings.HasPrefix(line, "+++") {
			s := stats[currentPath]
			s.Added++
			stats[currentPath] = s
		} else if len(line) > 0 && line[0] == '-' && !strings.HasPrefix(line, "---") {
			s := stats[currentPath]
			s.Removed++
			stats[currentPath] = s
		}
	}
	return stats
}

// parseUnifiedDiffToFiles parses a unified diff into per-file TrackedDiffFile entries.
// This mirrors the frontend parseUnifiedDiff but runs on the backend for caching.
func parseUnifiedDiffToFiles(diff string, truncatedFiles []string, origStats map[string]FileChangeStats) map[string]*TrackedDiffFile {
	if diff == "" || !strings.Contains(diff, "\n") {
		return nil
	}

	truncatedSet := make(map[string]bool, len(truncatedFiles))
	for _, f := range truncatedFiles {
		truncatedSet[f] = true
	}

	files := make(map[string]*TrackedDiffFile)
	var current *TrackedDiffFile
	var newLine int
	var inHunk bool
	var rowIdx int

	lines := strings.Split(diff, "\n")
	// Remove trailing empty line
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			match := fileRe.FindStringSubmatch(line)
			path := "unknown"
			if match != nil {
				path = match[1]
			}
			current = &TrackedDiffFile{Path: path, Rows: nil}
			files[path] = current
			inHunk = false
			newLine = 0
			rowIdx = 0
			continue
		}
		if strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			if m := hunkRe.FindStringSubmatch(line); m != nil {
				newLine, _ = strconv.Atoi(m[3])
			}
			continue
		}
		if !inHunk || current == nil {
			continue
		}

		if line == "\\ No newline at end of file" {
			continue
		}

		var variant string
		if len(line) > 0 && line[0] == '+' {
			variant = "added"
			current.Added++
		} else if len(line) > 0 && line[0] == '-' {
			variant = "removed"
			current.Removed++
		} else if len(line) > 0 && line[0] == ' ' {
			variant = "context"
		} else {
			continue
		}

		ln := newLine
		row := DiffRow{
			LineNumber: &ln,
			Variant:    variant,
			Text:       line[1:],
		}
		current.Rows = append(current.Rows, row)
		rowIdx++

		if variant == "added" || variant == "context" {
			newLine++
		}
	}

	// Enrich with truncation metadata
	for path, f := range files {
		if len(f.Rows) == 0 {
			delete(files, path)
			continue
		}
		if truncatedSet[path] {
			f.Truncated = true
			if s, ok := origStats[path]; ok {
				f.OrigAdded = s.Added
				f.OrigRemoved = s.Removed
			}
		}
	}

	return files
}

// hunkRe parses a unified diff hunk header.
// Groups: 1=oldStart, 2=oldCount, 3=newStart, 4=newCount, 5=context
// shortStatFromDiff computes a git-style shortstat string from a unified diff.
// Format: "X file(s) changed, Y insertion(s)(+), Z deletion(s)(-)"
// Omits zero-value parts (e.g., no deletions mentioned if count is 0).
func shortStatFromDiff(diff string) string {
	stats := countFileChanges(diff)
	files := len(stats)
	var totalAdded, totalRemoved int
	for _, s := range stats {
		totalAdded += s.Added
		totalRemoved += s.Removed
	}
	if files == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	parts = append(parts, fmt.Sprintf("%d file%s changed", files, pluralS(files)))
	if totalAdded > 0 {
		parts = append(parts, fmt.Sprintf("%d insertion%s(+)", totalAdded, pluralS(totalAdded)))
	}
	if totalRemoved > 0 {
		parts = append(parts, fmt.Sprintf("%d deletion%s(-)", totalRemoved, pluralS(totalRemoved)))
	}
	return strings.Join(parts, ", ")
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// SplitLines splits file content into lines, dropping a trailing empty entry
// caused by a final newline (matching how files are conventionally stored).
func SplitLines(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))?(.*)@@`)

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
	for line := range strings.SplitSeq(listOut, "\n") {
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
// If skipTruncate is true, the diff is not truncated (used for ?full=1 API requests).
func (wm *WorktreeManager) GetTaskDiff(taskID, baseRef, headRef string, skipTruncate ...bool) (*TaskDiff, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree not found")
	}

	// Reject invalid baseRef values (e.g. git error messages like "fatal: ...")
	if strings.HasPrefix(baseRef, "fatal:") {
		baseRef = ""
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
		shortStat = shortStatFromDiff(diffOut)
		diffOut = wm.filterLargeBinaryDiffs(worktreePath, diffOut)
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
		}
		shortStat = shortStatFromDiff(diffOut)
		diffOut = wm.filterLargeBinaryDiffs(worktreePath, diffOut)

		// Get untracked files from git status --porcelain (lines starting with "?? ")
		statusOut, err := wm.gitRunOutput(worktreePath, "status", "--porcelain")
		if err != nil {
			return nil, fmt.Errorf("git status: %w", err)
		}

		const maxDiffSize = 1 << 20 // 1MB
		for line := range strings.SplitSeq(statusOut, "\n") {
			if len(line) >= 4 && strings.HasPrefix(line, "?? ") {
				path := strings.TrimSpace(line[3:])
				if path == "" {
					continue
				}
				// Expand directories into individual file paths
				filePaths := walkUntrackedEntries(worktreePath, path)
				for _, filePath := range filePaths {
					// Skip files in ignored directories (safety net for tracked files)
					if ShouldIgnorePath(filePath) {
						continue
					}
					fullPath := filepath.Join(worktreePath, filePath)
					info, err := os.Stat(fullPath)
					if err != nil || !info.Mode().IsRegular() {
						continue
					}
					fileSize := info.Size()
					// Skip content for large files or binary files
					if fileSize > maxDiffSize || isBinaryFile(fullPath, fileSize) {
						untracked = append(untracked, UntrackedFile{Path: filePath, FileSize: fileSize})
						continue
					}
					// Read file content directly (untracked = new file, all lines are additions)
					data, err := os.ReadFile(fullPath)
					if err != nil {
						continue
					}
					fileLines := SplitLines(string(data))
					untracked = append(untracked, UntrackedFile{Path: filePath, Lines: fileLines})
				}
			}
		}
	}

	doTruncate := len(skipTruncate) == 0 || !skipTruncate[0]
	var truncatedFiles []string
	var originalStats map[string]FileChangeStats
	if doTruncate {
		originalStats = countFileChanges(diffOut)
		diffOut, truncatedFiles = truncateDiffLines(diffOut, defaultMaxChangeLines)
		// Truncate untracked file lines (all additions for new files)
		for i := range untracked {
			if len(untracked[i].Lines) == 0 {
				continue
			}
			if len(untracked[i].Lines) > defaultMaxChangeLines {
				untracked[i].OrigAdded = len(untracked[i].Lines)
				untracked[i].Lines = untracked[i].Lines[:defaultMaxChangeLines]
				untracked[i].Truncated = true
			}
		}
	}

	return &TaskDiff{
		Diff:              diffOut,
		Tracked:           parseUnifiedDiffToFiles(diffOut, truncatedFiles, originalStats),
		Untracked:         untracked,
		ShortStat:         shortStat,
		TruncatedFiles:    truncatedFiles,
		OriginalFileStats: originalStats,
	}, nil
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
	for line := range strings.SplitSeq(statusOut, "\n") {
		if len(line) >= 4 && strings.HasPrefix(line, "?? ") {
			path := strings.TrimSpace(line[3:])
			if path == "" {
				continue
			}
			// Expand directories into individual file paths
			filePaths := walkUntrackedEntries(worktreePath, path)
			for _, filePath := range filePaths {
				untrackedN++
				// Count lines in new (untracked) files
				fullPath := filepath.Join(worktreePath, filePath)
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
	limit := min(size, int64(8192))
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, _ := f.Read(buf)
	for i := range n {
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
	BaseCommit *TaskCommit  `json:"base_commit,omitempty"` // commit at the divergence point
	Commits    []TaskCommit `json:"commits"`
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

	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
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
	// Reject invalid baseRef values (e.g. git error messages like "fatal: ...")
	if strings.HasPrefix(baseRef, "fatal:") {
		baseRef = ""
	}
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
	for line := range strings.SplitSeq(listOut, "\n") {
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

	// Stash not found in list (e.g., dropped after commit-and-done).
	// Fall back to HEAD at stash time (first parent of the stash commit).
	parent, err := wm.gitRunOutput(worktreePath, "rev-parse", headRef+"^")
	if err == nil {
		return strings.TrimSpace(parent), nil
	}
	return headRef, nil
}

var stashIndexRe = regexp.MustCompile(`stash@\{(\d+)\}`)
var fileRe = regexp.MustCompile(`^diff --git a/(.+?) b/`)
