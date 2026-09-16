// Package git is the version-control layer: one worktree and branch per task, plus the status,
// diff, stash, merge and checkpoint operations the UI needs. Client refs are validated and then
// fenced with --end-of-options, and clones are restricted to allowed transports (clone.go).
package git

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mike/backend/config"
	"mike/backend/fsutil"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/keylock"
	"mike/pkg/logging"
)

// EmptyTreeHash is git's SHA-1 of "tree 0\0" — the empty tree object.
const EmptyTreeHash = "4b825dc642cb6eb9a060e54bf899d69f826635b6"

// hexRe matches a string of exactly 40 hex characters (a git SHA-1).
var hexRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Git author identity, process-wide: unset means git uses its own configured default. The
// add-project flow sets it once and every later commit uses it, so it is written while other
// goroutines read it - use SetAuthorIdentity / AuthorIdentity, never the variables below.
var (
	authorMu    sync.RWMutex
	authorName  string
	authorEmail string
)

// SetAuthorIdentity sets the identity commits are signed with. An empty half means "git's default".
func SetAuthorIdentity(name, email string) {
	authorMu.Lock()
	authorName, authorEmail = name, email
	authorMu.Unlock()
}

// AuthorIdentity returns the configured overrides.
func AuthorIdentity() (name, email string) {
	authorMu.RLock()
	defer authorMu.RUnlock()
	return authorName, authorEmail
}

// GitAuthorFlags returns -c flags to override the commit author identity.
// When neither is set, returns nil so Git uses its configured defaults.
func GitAuthorFlags() []string {
	name, email := AuthorIdentity()
	var flags []string
	if name != "" {
		flags = append(flags, "-c", "user.name="+name)
	}
	if email != "" {
		flags = append(flags, "-c", "user.email="+email)
	}
	return flags
}

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

// IgnoredDirs returns the well-known directory names to always skip.
//
// It returns a COPY: handing out the live map let any caller change what every other caller,
// including the worktree watcher, considers ignorable.
func IgnoredDirs() map[string]struct{} {
	out := make(map[string]struct{}, len(ignoredDirs))
	for k, v := range ignoredDirs {
		out[k] = v
	}
	return out
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

	if fsutil.IsAtomicTemp(base) {
		return true
	}
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

// IsGitIgnored reports whether relPath (relative to repoRoot) is excluded by gitignore. Tracked
// files are never considered ignored (git check-ignore's default), so events for them are still
// broadcast. False when git is unavailable, the repo is missing, or the path is outside it.
func IsGitIgnored(repoRoot, relPath string) bool {
	if repoRoot == "" || relPath == "" {
		return false
	}
	if filepath.IsAbs(relPath) {
		rel, err := filepath.Rel(repoRoot, relPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return false
		}
	}
	cmd := Command("", "check-ignore", "--quiet", "--", relPath)
	cmd.Dir = repoRoot
	// Exit code 0 means ignored, 1 means not ignored, 128 means fatal error.
	// Any error (not ignored or git unavailable) → not ignored.
	return cmd.Run() == nil
}

// IsInternalFile returns true if the path is an internal file that should not
// appear in the diff panel or card stats (e.g., .git/*, kanban.trace).
func IsInternalFile(path string) bool {
	// Check if any path component is .git (handles vendor/lib/.git, .git/index, etc.)
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	base := filepath.Base(path)
	if base == "kanban.trace" || base == "kanban.trace.old" {
		return true
	}
	return false
}

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	store       *store.Store
	configStore *config.ConfigStore

	// Three lock domains, all keyed by project. Distinct named types, and each one its own table: with
	// one shared table, fusing two domains would compile, and the keys cannot be trusted to tell them
	// apart because a task ID and a project ID are drawn from the same alphabet (F16). With a type per
	// domain, that mistake does not compile.
	worktree worktreeLocks // worktree creation
	stash    stashLocks    // the one stash stack, shared by every worktree and by the user's own entries
	merge    mergeLocks    // the main checkout, which has a single HEAD and index
}

// Embedded by value, not by pointer: a domain built this way is usable at its zero value the way the
// sync.Map it replaced was, and a copy of the owning struct is refused by `go vet` copylocks instead of
// silently giving two callers two guards over one map.
type (
	worktreeLocks struct{ keylock.Table }
	stashLocks    struct{ keylock.Table }
	mergeLocks    struct{ keylock.Table }
)

// worktreeLockTimeout is how long a caller waits for a project's worktree lock. Creating a worktree
// runs a handful of git commands, so a longer wait means something is wedged. It is the one bounded
// git wait, and the number is unchanged: it predates this package and was chosen for the operation it
// guards, which is the only git-family section that does not wait on anything else.
const worktreeLockTimeout = 30 * time.Second

// gitSlowWait is when a lock wait is worth a log line. A bound is not the right instrument for the
// stash and merge locks because both sections can legitimately wait on a task's worktree lock —
// `exec/merge.go:654` takes mergeMu and then waits for the task's own tool lock at `:672`, and that one
// is held across a whole tool call (`bash` runs up to 300 s) — so a seconds-scale timeout there would
// fail the merges and checkpoints it is meant to protect. The wait is reported instead.
const gitSlowWait = 2 * time.Second

// slowWaitReporter names the domain in the slow-wait line, because a project can be contended on any of
// the three locks and "which one" is the first question the answer needs.
func slowWaitReporter(domain string) keylock.WaitReport {
	return func(projectID string, waited time.Duration) {
		logging.For(logging.Git).With(logging.ProjectID, projectID).Warnf(
			"waited %s for the project's %s lock - another git operation holds it",
			waited.Round(time.Millisecond), domain)
	}
}

func NewWorktreeManager(store *store.Store, configStore *config.ConfigStore) *WorktreeManager {
	wm := &WorktreeManager{store: store, configStore: configStore}
	wm.worktree.Configure(gitSlowWait, slowWaitReporter("worktree"))
	wm.stash.Configure(gitSlowWait, slowWaitReporter("stash"))
	wm.merge.Configure(gitSlowWait, slowWaitReporter("merge"))
	return wm
}

// GetTaskByID retrieves a task by its globally-unique ID (without project ID).
func (wm *WorktreeManager) GetTaskByID(taskID string) (*types.Task, error) {
	return wm.store.GetTaskByID(taskID)
}

// worktreesRoot is the directory every task worktree lives under
func (wm *WorktreeManager) worktreesRoot() string {
	return wm.configStore.WorkspaceDir()
}

// projectWorktreesDir is the directory holding one project's task worktrees: <root>/<projectID>.
func (wm *WorktreeManager) projectWorktreesDir(projectID string) string {
	return filepath.Join(wm.worktreesRoot(), projectID)
}

// RemoveProjectWorktrees removes a project's worktree directory. Each task's own removal takes only
// its own, so this is what takes the parent and anything a failed removal left in it.
func (wm *WorktreeManager) RemoveProjectWorktrees(projectID string) error {
	return os.RemoveAll(wm.projectWorktreesDir(projectID))
}

// WorktreePath returns the filesystem path for a task's worktree.
// Path format: worktreesDir/projectID/taskID
func (wm *WorktreeManager) WorktreePath(taskID string) (string, error) {
	task, err := wm.store.GetTaskByID(taskID)
	if err != nil {
		return "", err
	}
	return filepath.Join(wm.projectWorktreesDir(task.ProjectID), taskID), nil
}

// CreateWorktree creates a git worktree for a task, capturing the current working tree state.
// If sourceBranch is non-empty, the worktree is created from that branch instead of HEAD.
// Returns (worktreePath, originalBranch, error).
func (wm *WorktreeManager) CreateWorktree(projectID, taskID, branchName, sourceBranch string) (string, string, error) {
	// Guard against empty or 40-hex branch names (ambiguous with commit SHAs).
	if branchName == "" || (len(branchName) == 40 && branchName == hexRe.FindString(branchName)) {
		branchName = "task/" + taskID
	}

	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return "", "", fmt.Errorf("get project: %w", err)
	}

	worktreesDir := wm.worktreesRoot()
	worktreePath := filepath.Join(worktreesDir, projectID, taskID)

	// Acquire per-project mutex with timeout
	unlockWorktree, locked := wm.tryLockProjectWorktree(projectID, worktreeLockTimeout)
	if !locked {
		return "", "", fmt.Errorf("timeout waiting for worktree lock (another task is initializing)")
	}
	defer unlockWorktree()

	// Keep worktree content out of the project's git state when the worktrees
	// dir lives inside the project directory.
	wm.ensureWorktreesExcluded(project, worktreesDir)

	if err := os.MkdirAll(filepath.Join(worktreesDir, projectID), 0o755); err != nil {
		return "", "", fmt.Errorf("create worktrees dir: %w", err)
	}

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

	// Worktree starts from HEAD (committed state only — uncommitted changes are not carried)
	sourceRef := "HEAD"
	// If a source branch is specified (e.g., parent branch for child tasks), use it instead
	if sourceBranch != "" {
		sourceRef = sourceBranch
	}

	// Remove stale git worktree registration (prune skips locked entries).
	staleRegPath := filepath.Join(project.Path, ".git", "worktrees", taskID)
	_ = os.RemoveAll(staleRegPath)

	cmd := Command("", "worktree", "add", "--detach", worktreePath, sourceRef)
	cmd.Dir = project.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("create worktree: %s: %w", string(out), err)
	}

	// Create the branch in the worktree. If the branch already exists (e.g., created
	// by ensureParentBranch for a parent whose subtask started first), just checkout it.
	// Otherwise, create it with -B (forces creation at current commit).
	branchExists := wm.branchExistsInRepo(project.Path, branchName)
	var branchErr error
	if branchExists {
		_, branchErr = wm.gitRunOutput(worktreePath, "checkout", branchName)
	} else {
		_, branchErr = wm.gitRunOutput(worktreePath, "checkout", "-B", branchName)
	}
	if branchErr != nil {
		wm.gitRun(project.Path, "worktree", "remove", "--force", worktreePath)
		return "", "", fmt.Errorf("create branch %s: %w", branchName, branchErr)
	}

	// Return the source branch name (merge target / diff baseline).
	// For child tasks this is the parent's branch. For root tasks, fall back
	// to the origin branch captured before worktree creation.
	baseRef := sourceBranch
	if baseRef == "" {
		baseRef = originalBranch
	}
	// Sanitize: reject git error messages as branch names
	if strings.HasPrefix(baseRef, "fatal:") {
		baseRef = ""
	}

	return worktreePath, baseRef, nil
}

// RemoveWorktree removes a task's worktree directory, its registration, and its branch.
//
// The order matters: the .git/worktrees/ registration, not the directory, is what stops git deleting
// the branch, and a prune before the directory is gone prunes nothing.
func (wm *WorktreeManager) RemoveWorktree(projectID, taskID, branchName string) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}

	// The prune and branch delete run even when the directory could not be removed, so a directory
	// this process cannot delete does not strand the branch.
	var errs []error
	if err := os.RemoveAll(worktreePath); err != nil {
		errs = append(errs, fmt.Errorf("remove worktree dir: %w", err))
	}
	_ = wm.gitRun(project.Path, "worktree", "prune")
	if branchName != "" {
		if err := wm.deleteBranch(project.Path, branchName); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GitStatus returns the git status --porcelain output for a task's worktree.
func (wm *WorktreeManager) GitStatus(taskID string) (string, error) {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return "", ErrWorktreeNotFound
	}

	out, err := wm.gitRunOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return out, nil
}

// ParseStatusPaths reads `git status --porcelain` (v1) output into workspace-relative paths: two
// status columns, a space, then the path — "R  old -> new" for a rename (whose destination is the
// file that exists), quoted when it contains unusual characters. An untracked directory ("?? dir/")
// comes back unexpanded; use walkUntrackedEntries for the files inside it.
func ParseStatusPaths(out string) []string {
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[2:])
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:] // a rename is about its destination
		}
		path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), `"`)
		if path != "" {
			files = append(files, path)
		}
	}
	return files
}

// UntrackedFile carries raw content lines: an untracked file is always new, so there is no diff.
type UntrackedFile struct {
	Path      string   `json:"path"`
	Lines     []string `json:"lines,omitempty"`      // file content lines (set for text files)
	LineCount int      `json:"line_count,omitempty"` // total line count (set when Lines is omitted)
	FileSize  int64    `json:"file_size,omitempty"`  // set when file is binary or >1MB (lines omitted)
	IsBinary  bool     `json:"is_binary,omitempty"`  // true when file is detected as binary
	Truncated bool     `json:"truncated,omitempty"`  // true when lines were truncated to maxChangeLines
	OrigAdded int      `json:"orig_added,omitempty"` // original line count before truncation
}

// DiffRow represents a single line in a parsed diff. Word-level inline
// highlighting is NOT modelled here: the frontend derives those segments itself
// from paired removed/added rows (see frontend/src/lib/utils/diff-parser.ts).
type DiffRow struct {
	LineNumber *int   `json:"lineNumber"` // null for non-code rows
	Variant    string `json:"variant"`    // "context", "added", "removed"
	Text       string `json:"text"`       // empty string for blank lines
}

// TrackedDiffFile holds the parsed diff for a single tracked file.
type TrackedDiffFile struct {
	Path          string    `json:"path"`
	OrigPath      string    `json:"origPath,omitempty"`      // original path for renames (a/ path when different from b/)
	CommittedRows []DiffRow `json:"committedRows,omitempty"` // changes committed to branch since baseRef
	Rows          []DiffRow `json:"rows"`
	Added         int       `json:"added"`
	Removed       int       `json:"removed"`
	FileSize      *int64    `json:"fileSize,omitempty"`  // set for binary/large files
	IsBinary      bool      `json:"isBinary,omitempty"`  // true when file is binary (git-detected)
	IsNew         bool      `json:"isNew,omitempty"`     // true when file is newly created (from "new file mode" marker)
	IsDeleted     bool      `json:"isDeleted,omitempty"` // true when file is deleted (from "deleted file mode" marker)
	Truncated     bool      `json:"truncated,omitempty"`
	OrigAdded     int       `json:"origAdded,omitempty"`
	OrigRemoved   int       `json:"origRemoved,omitempty"`
}

// TaskDiff carries two shapes: Tracked is a per-file map, for incremental updates, and Diff is the
// legacy monolithic unified diff string.
type TaskDiff struct {
	Diff          string                      `json:"diff"`
	CommittedDiff string                      `json:"-"` // raw diff of committed changes since baseRef (for hash computation)
	Tracked       map[string]*TrackedDiffFile `json:"-"` // per-file parsed diffs (not JSON-serialized in legacy format)
	// ParsedFiles is the diff as an ordered array, present only when the caller asked for ?parsed=1, so
	// the browser does not run a second grammar over the same patch text. Not named `files`: this route's
	// manifest response already uses that key for a keyed map of hashes.
	ParsedFiles       []TrackedDiffFile          `json:"parsed_files,omitempty"`
	Untracked         []UntrackedFile            `json:"untracked"`
	ShortStat         string                     `json:"short_stat"`
	TruncatedFiles    []string                   `json:"truncated_files,omitempty"`
	OriginalFileStats map[string]FileChangeStats `json:"original_file_stats,omitempty"`
	StagedHash        string                     `json:"-"` // SHA-256 of staged state; changes diff hash when files are staged/unstaged
	StagedPaths       []string                   `json:"-"` // paths currently staged in the index
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
	for _, row := range f.CommittedRows {
		fmt.Fprintf(&sb, "c:%s:%s\n", row.Variant, row.Text)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(sb.String())))
}

// GetPerFileHashes returns a map of file path → hash for all tracked and untracked files.
// Tracked entries take priority — untracked files already in the tracked section are skipped.
func (d *TaskDiff) GetPerFileHashes() map[string]string {
	hashes := make(map[string]string)
	for path, f := range d.Tracked {
		hashes[path] = ComputeFileHash(f)
	}
	for _, u := range d.Untracked {
		if _, exists := hashes[u.Path]; !exists {
			hashes[u.Path] = ComputeUntrackedHash(u)
		}
	}
	return hashes
}

// Default maximum number of change lines per file before truncation.
const defaultMaxChangeLines = 60

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

// PluralS returns "s" if count is not 1, empty string otherwise.
func PluralS(n int) string {
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

// hunkRe parses a unified diff hunk header: 1=oldStart, 2=oldCount, 3=newStart, 4=newCount,
// 5=context.
var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))?(.*)@@`)

// Stash message label prefixes used by kanban operations
const (
	stashLabelSwitch = "mike-switch-"
	stashLabelFork   = "mike-fork-"
	// No longer written: checkpoints are snapshots pinned under a ref (see snapshots.go). Kept so the
	// sweep still cleans up repos whose checkpoints were stash entries.
	stashLabelCheckpoint = "mike-checkpoint-"
	stashLabelSyncMerge  = "mike-sync-merge-"
	stashLabelAutoMerge  = "mike-auto-merge-"
)

// kanbanLabelPrefixes are the stash message prefixes used by kanban operations.
var kanbanLabelPrefixes = []string{
	stashLabelSwitch,
	stashLabelFork,
	stashLabelCheckpoint,
	stashLabelSyncMerge,
	stashLabelAutoMerge,
}

// hasKanbanLabel returns true if the subject contains any kanban label prefix.
func hasKanbanLabel(subject string) bool {
	for _, p := range kanbanLabelPrefixes {
		if strings.Contains(subject, p) {
			return true
		}
	}
	return false
}

// matchKanbanTaskID returns true if the subject is a kanban-labeled stash for the given task.
func matchKanbanTaskID(subject, taskID string) bool {
	for _, p := range kanbanLabelPrefixes {
		if strings.Contains(subject, p+taskID) {
			return true
		}
	}
	return false
}

// DropStashEntriesForTask removes kanban-labeled stash entries for the given task.
// Drops by commit hash — no index renumbering issues.
func (wm *WorktreeManager) DropStashEntriesForTask(projectID, taskID string) error {
	defer wm.LockProjectStash(projectID)()
	return wm.dropMatchingStashes(projectID, func(subject string) bool {
		return matchKanbanTaskID(subject, taskID)
	})
}

// -- Helper functions --

// fileAnnotation holds metadata for a file whose diff was replaced with an annotation.
type fileAnnotation struct {
	path     string // b/ path (current name)
	origPath string // a/ path (empty if same as path)
	size     int64
	isBinary bool
}

// getFileSize tries os.Stat() first, falls back to git cat-file -s for blob hash.
func getFileSize(worktreePath, filePath, blobHash string, wm *WorktreeManager) (int64, bool) {
	if filePath != "" {
		fullPath := filepath.Join(worktreePath, filePath)
		if info, err := os.Stat(fullPath); err == nil && info.Mode().IsRegular() {
			return info.Size(), true
		}
	}
	if blobHash != "" {
		if size, err := wm.gitCatFileSize(blobHash); err == nil {
			return size, true
		}
	}
	return 0, false
}

// gitCatFileSize returns the size of a git blob object.
func (wm *WorktreeManager) gitCatFileSize(hash string) (int64, error) {
	out, err := wm.gitRunOutput("", "cat-file", "-s", hash)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// extractBinarySize tries to get the file size from a "Binary files a/X and b/Y differ" line.
// Git doesn't include sizes in this line, so this always returns 0.
// The caller must resolve the size via os.Stat() or git cat-file.
func extractBinarySize(_ string) int64 {
	return 0
}

// splitQuotedPathPair divides `"a/x" "b/x"` into its two raw quoted tokens, honoring backslash escapes
// so an escaped quote inside the first path is not mistaken for its end.
func splitQuotedPathPair(s string) (string, string) {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // the escaped byte is part of the token, whatever it is
		case '"':
			return s[:i+1], strings.TrimSpace(s[i+1:])
		}
	}
	return s, ""
}

// unquoteGitPath turns a git C-quoted path token into the path itself. strconv.Unquote speaks the same
// escape language git writes (\", \\, \n, \t, \uXXXX), so it is used rather than a hand-rolled subset.
func unquoteGitPath(tok string) string {
	tok = strings.TrimSpace(tok)
	if len(tok) < 2 || tok[0] != '"' || tok[len(tok)-1] != '"' {
		return tok
	}
	if unq, err := strconv.Unquote(tok); err == nil {
		return unq
	}
	// An escape strconv rejects (git writes \a, for instance) must not lose the path. Return the token
	// with its outer quotes removed, which is still a usable path and beats the empty string.
	return tok[1 : len(tok)-1]
}

// binaryExts lists extensions unreadable as text; it is what labels a new file "Binary file — N
// bytes" in the diff panel. SVG is deliberately absent (it is XML source) — the separate imageExts
// allowlist in backend/exec/loop.go is what includes it.
var binaryExts = map[string]bool{
	// Images
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".ico": true, ".tiff": true, ".tif": true,
	".avif": true, ".heic": true, ".heif": true, ".psd": true, ".ai": true,
	".raw": true, ".cr2": true, ".nef": true,
	// Archives/Compressed
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true,
	".7z": true, ".rar": true, ".tz": true, ".tgz": true, ".txz": true,
	".deb": true, ".rpm": true, ".jar": true, ".war": true, ".ear": true,
	// Fonts
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	// Media
	".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wav": true,
	".webm": true, ".ogg": true, ".flac": true, ".m4a": true, ".wmv": true,
	".mkv": true, ".flv": true, ".wma": true, ".aac": true, ".ogv": true,
	// Executables/Binary
	".dll": true, ".so": true, ".dylib": true, ".exe": true, ".bin": true,
	".msi": true, ".dmg": true, ".appimage": true,
	// Compiled/Object
	".class": true, ".pyc": true, ".o": true, ".a": true, ".lib": true,
	".pdb": true, ".pyo": true,
	// Documents
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true,
	// Database
	".sqlite": true, ".sqlite3": true, ".db": true,
	// Other
	".DS_Store": true, ".git": true, ".lock": true,
}

// isBinaryExtension checks if a file path has a known binary extension.
func isBinaryExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return binaryExts[ext]
}

// isBinaryContent checks if a file is binary by scanning the first 1KB for null bytes.
func isBinaryContent(path string, size int64) bool {
	if size == 0 {
		return false
	}
	limit := min(size, int64(1024))
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

func (wm *WorktreeManager) gitRun(dir string, args ...string) error {
	_, err := wm.gitRunInGitDir(dir, args...)
	return err
}

// gitRunInGitDir runs git against dir's own gitdir and returns what git printed, because git writes
// its reason for refusing to stderr and an exit code alone tells the user nothing. GIT_DIR goes in as
// an Env extra rather than an assignment to cmd.Env, which would replace the allowlisted environment.
func (wm *WorktreeManager) gitRunInGitDir(dir string, args ...string) (string, error) {
	cmd := Command(dir, args...)
	cmd.Env = Env("GIT_DIR=" + filepath.Join(dir, ".git"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (wm *WorktreeManager) gitRunOutput(dir string, args ...string) (string, error) {
	cmd := Command(dir, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// baselineUsable reports whether a recorded baseline ref is a meaningful
// diff baseline for the task's branch
func (wm *WorktreeManager) baselineUsable(projectPath, baseline, taskBranch string) bool {
	if baseline == "" {
		return false
	}
	if baseline == taskBranch {
		return false
	}
	if projectPath == "" {
		return true
	}
	out, err := wm.gitRunOutput(projectPath, "rev-parse", "--verify", "--quiet", baseline+"^{commit}")
	return err == nil && strings.TrimSpace(out) != ""
}

// ResolveTaskBaseline returns the git ref to diff a task against: its recorded BaseCommit
func (wm *WorktreeManager) ResolveTaskBaseline(projectPath, originBranch string, task *types.Task) string {
	baseline := task.BaseRef
	if task.BaseCommit != "" {
		baseline = task.BaseCommit
	}
	if task.Branch != "" && !wm.baselineUsable(projectPath, baseline, task.Branch) {
		baseline = wm.DivergenceBase(projectPath, originBranch, task.Branch)
	}
	return baseline
}

// GitRunInWorktree runs a git command in the task's worktree directory.
func (wm *WorktreeManager) GitRunInWorktree(taskID string, args ...string) error {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		return ErrWorktreeNotFound
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
		return "", ErrWorktreeNotFound
	}
	return wm.gitRunOutput(worktreePath, args...)
}

// StageIndexedFiles refreshes the index of currently staged files from the
// working tree, so post-staging manual edits are included in the next commit
// instead of being silently dropped. Never-staged files stay untouched.
// Best-effort: logs failures, never blocks the commit.
//
// An unmerged path is skipped: `git add` is how git is told a conflict is resolved. The paths it left
// alone are returned so the caller can refuse the commit.
func (wm *WorktreeManager) StageIndexedFiles(taskID string) []string {
	out, err := wm.GitRunInWorktreeOutput(taskID, "diff", "--staged", "--name-only", "-z", "HEAD")
	if err != nil {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("failed to list staged files: %v", err)
		return nil
	}
	unmerged, err := wm.GitRunInWorktreeOutput(taskID, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("failed to list unmerged files: %v", err)
		return nil
	}
	skip := map[string]bool{}
	var unmergedList []string
	for _, p := range strings.Split(unmerged, "\x00") {
		if p != "" {
			skip[p] = true
			unmergedList = append(unmergedList, p)
		}
	}
	// Output is NUL-separated; the final token after the trailing NUL is empty.
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p == "" || skip[p] {
			continue
		}
		paths = append(paths, p)
	}
	if len(unmergedList) > 0 {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("left %d unmerged file(s) unstaged: %s", len(unmergedList), strings.Join(unmergedList, ", "))
	}
	if len(paths) == 0 {
		return unmergedList
	}
	args := append([]string{"add", "--"}, paths...)
	if err := wm.GitRunInWorktree(taskID, args...); err != nil {
		logging.For(logging.Worktree).With(logging.TaskID, taskID).Warnf("failed to re-stage %d file(s): %v", len(paths), err)
	}
	return unmergedList
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

// GitRunInDir runs a git command in the given directory.
func (wm *WorktreeManager) GitRunInDir(dir string, args ...string) error {
	return wm.gitRun(dir, args...)
}

// GitRunInDirOutput runs a git command in the given directory and returns its output.
func (wm *WorktreeManager) GitRunInDirOutput(dir string, args ...string) (string, error) {
	return wm.gitRunOutput(dir, args...)
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
		return nil, ErrWorktreeNotFound
	}

	return TaskCommitsIn(worktreePath, baseRef, "")
}

// IsAncestor reports whether `ancestor` is an ancestor of (or equal to) `descendant`.
func (wm *WorktreeManager) IsAncestor(taskID, ancestor, descendant string) bool {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return false
	}
	cmd := Command("", "-C", worktreePath, "merge-base", "--is-ancestor", ancestor, descendant)
	return cmd.Run() == nil
}

// ResetWorktreeTo restores the files a snapshot captured and never moves HEAD: they land as
// unstaged/untracked changes, which is what git status, the diff views and the commit flow read.
// The snapshot is read as a tree, not applied as a patch — a patch cannot carry a binary file, and
// `reset --hard` would move the branch onto the snapshot commit.
func (wm *WorktreeManager) ResetWorktreeTo(taskID, hash string) error {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(hash) == "" {
		return fmt.Errorf("restore: no snapshot given for task %s", taskID)
	}
	// Check the snapshot exists before throwing the current work away.
	if _, err := wm.gitRunOutput(worktreePath, "rev-parse", "--verify", hash+"^{tree}"); err != nil {
		return fmt.Errorf("%w: snapshot %.12s is not in this repository: %w", ErrSnapshotMissing, hash, err)
	}

	// Clean working tree.
	_ = wm.gitRun(worktreePath, "checkout", "-f", "HEAD")
	_ = wm.gitRun(worktreePath, "clean", "-fd")

	// Make index and working tree match the snapshot exactly; --reset overwrites local changes.
	if err := wm.gitRun(worktreePath, "read-tree", "--reset", "-u", hash); err != nil {
		return fmt.Errorf("restore task %s to snapshot %.12s: %w", taskID, hash, err)
	}
	// A `stash push -u` entry keeps its untracked files in a third parent, absent from the tree above.
	if untracked, ok := wm.stashUntrackedCommit(worktreePath, hash); ok {
		if err := wm.gitRun(worktreePath, "checkout", untracked, "--", "."); err != nil {
			return fmt.Errorf("restore untracked files of snapshot %.12s: %w", hash, err)
		}
	}
	// Unstage again so the restored files read as the task's own changes, not a staged commit.
	if err := wm.gitRun(worktreePath, "reset", "-q"); err != nil {
		return fmt.Errorf("unstage restored files for task %s: %w", taskID, err)
	}
	return nil
}

// StashEntry holds a parsed stash list entry.
type StashEntry struct {
	Ref     string // stash@{N}
	Hash    string // commit hash
	Subject string // reflog subject
}

// stashHashSet snapshots the stash list as a set of commit hashes, so a later list can be diffed against
// it to find what a push added.
func (wm *WorktreeManager) stashHashSet(repoPath string) (map[string]bool, error) {
	entries, err := wm.listStashEntries(repoPath)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(entries))
	for _, e := range entries {
		set[e.Hash] = true
	}
	return set, nil
}

// shortHash renders a commit hash for a log line or error message.
func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

// RestoreStashForTask restores the stash entry pushed under `label` (a StashMessageFor* value), if
// one is on the stack; found reports whether anything of ours was there. It exists so no caller
// reaches for `git stash pop`, which ignores who pushed it.
// The caller must hold the project stash mutex.
func (wm *WorktreeManager) RestoreStashForTask(dir, label string) (bool, error) {
	hash, found, err := wm.NewestStashHashForMessage(dir, label)
	if err != nil || !found {
		return false, err
	}
	return wm.PopStashByHash(dir, hash)
}

// DropStashForTask discards the stash entry pushed under label (a StashMessageFor* value), if
// present: the conflict-resolution path discards stashed pre-sync edits rather than re-applying
// them, because they are committed by then. The caller must hold the project stash mutex.
func (wm *WorktreeManager) DropStashForTask(dir, label string) (bool, error) {
	hash, found, err := wm.NewestStashHashForMessage(dir, label)
	if err != nil || !found {
		return false, err
	}
	return true, wm.DropStashByHash(dir, hash)
}

// maxStashMessage is the maximum length for any stash message.
const maxStashMessage = 50

// stashMessageWithPrefix creates a stash message from a prefix and task ID,
// truncated to maxStashMessage characters.
func stashMessageWithPrefix(prefix, taskID string) string {
	msg := prefix + taskID
	if len(msg) > maxStashMessage {
		msg = msg[:maxStashMessage]
	}
	return msg
}

// StashMessageForSwitch returns a stash message for a thread switch.
func StashMessageForSwitch(taskID string) string {
	return stashMessageWithPrefix(stashLabelSwitch, taskID)
}

// StashMessageForFork returns a stash message for a thread fork.
func StashMessageForFork(taskID string) string {
	return stashMessageWithPrefix(stashLabelFork, taskID)
}

var fileRe = regexp.MustCompile(`^diff --git a/(.+?) b/`)

// IsGitRepo checks if a path is a git repository.
func IsGitRepo(path string) bool {
	cmd := Command("", "-C", path, "rev-parse", "--git-dir")
	cmd.Dir = path
	return cmd.Run() == nil
}

// HasGitDir reports whether path carries a .git entry of its own — a repository root, a linked
// worktree (where .git is a file), or a submodule. One stat instead of a `git rev-parse`, for
// callers that check many entries. Narrower than IsGitRepo, whose rev-parse walks up the tree.
func HasGitDir(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}
