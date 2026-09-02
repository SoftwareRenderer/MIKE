// Package git is the version-control layer: one worktree and branch per task, plus the
// status, diff, stash, merge and checkpoint operations the UI needs.
// Two invariants shape it: client refs are validated and then fenced with
// --end-of-options, and clones are restricted to allowed transports by both our own check
// and git's (clone.go).
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
	"kanban/backend/types"
)

// EmptyTreeHash is the SHA-1 of an empty git tree object.
// It's a git internal constant (SHA-1 of "tree 0\0").
const EmptyTreeHash = "4b825dc642cb6eb9a060e54bf899d69f826635b6"

// hexRe matches a string of exactly 40 hex characters (a git SHA-1).
var hexRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Git author identity.
// When unset, Git uses its configured defaults (from .gitconfig / system config).
var (
	GitAuthorName  = ""
	GitAuthorEmail = ""
)

// GitAuthorFlags returns -c flags to override the commit author identity.
// When neither is set, returns nil so Git uses its configured defaults.
func GitAuthorFlags() []string {
	var flags []string
	if GitAuthorName != "" {
		flags = append(flags, "-c", "user.name="+GitAuthorName)
	}
	if GitAuthorEmail != "" {
		flags = append(flags, "-c", "user.email="+GitAuthorEmail)
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

// IsGitIgnored reports whether relPath (relative to repoRoot) is excluded by the
// repository's gitignore rules. Tracked files are NOT considered ignored (matching
// git check-ignore default behavior), so events for tracked files still get
// broadcast. Returns false when git is unavailable, repoRoot is not a git repo,
// or the path is outside repoRoot.
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
	cmd := exec.Command("git", "check-ignore", "--quiet", "--", relPath)
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

// worktreeStore is the ids and branches that tie a worktree to a task - the only
// database access a git runner needs.
type worktreeStore interface {
	GetAllTaskBranches() ([]string, error)
	GetAllTaskIDs() ([]string, error)
	GetAllThreadIDs() ([]string, error)
	GetProject(id string) (*types.Project, error)
	GetTaskByID(taskID string) (*types.Task, error)
	GetTasksByProject(projectID string) ([]types.Task, error)
	ListProjects() ([]types.Project, error)
	UpdateProjectOriginBranch(id string, branch string) error
}

var _ worktreeStore = (*store.Store)(nil)

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	store       worktreeStore
	configStore *config.ConfigStore
	worktreeMu  sync.Map // per-project mutex for worktree creation
	stashMu     sync.Map // per-project mutex for stash operations
	mergeMu     sync.Map // per-project mutex for merges using the project's main checkout

}

// NewWorktreeManager creates a new WorktreeManager.
func NewWorktreeManager(store *store.Store, configStore *config.ConfigStore) *WorktreeManager {
	return &WorktreeManager{store: store, configStore: configStore}
}

// GetTaskByID retrieves a task by its globally-unique ID (without project ID).
func (wm *WorktreeManager) GetTaskByID(taskID string) (*types.Task, error) {
	return wm.store.GetTaskByID(taskID)
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

// GetProjectMergeMu returns a per-project mutex for serializing merges that
// operate on the project's main checkout (root-task merges). The main checkout
// has a single HEAD/index — interleaved merges clobber each other's checkout,
// stashes, and restore. Lock order: always acquired BEFORE (never after) the
// project stash mutex.
func (wm *WorktreeManager) GetProjectMergeMu(projectID string) *sync.Mutex {
	mu, _ := wm.mergeMu.LoadOrStore(projectID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// WorktreePath returns the filesystem path for a task's worktree.
// Path format: worktreesDir/projectID/taskID
func (wm *WorktreeManager) WorktreePath(taskID string) (string, error) {
	task, err := wm.store.GetTaskByID(taskID)
	if err != nil {
		return "", err
	}
	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return "", err
	}
	worktreesDir := cfg.WorkspaceDir
	return filepath.Join(worktreesDir, task.ProjectID, taskID), nil
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

	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return "", "", fmt.Errorf("get config: %w", err)
	}

	worktreesDir := cfg.WorkspaceDir
	if !filepath.IsAbs(worktreesDir) {
		abs, err := filepath.Abs(worktreesDir)
		if err == nil {
			worktreesDir = abs
		}
	}
	worktreePath := filepath.Join(worktreesDir, projectID, taskID)

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

	cmd := exec.Command("git", "worktree", "add", "--detach", worktreePath, sourceRef)
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

// ensureWorktreesExcluded idempotently appends the worktrees dir to .git/info/exclude
// when it's inside the project, keeping untracked worktree files out of git status.
// Best-effort: never blocks worktree creation.
func (wm *WorktreeManager) ensureWorktreesExcluded(project *types.Project, worktreesDir string) {
	cleanRoot := filepath.Clean(project.Path)
	cleanWT := filepath.Clean(worktreesDir)
	rel, err := filepath.Rel(cleanRoot, cleanWT)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return // worktrees dir is not inside the project
	}

	// Locate the repo's exclude file. A project that is itself a git worktree
	// has a .git file (pointing at the real git dir) rather than a directory.
	gitDir := filepath.Join(cleanRoot, ".git")
	if info, err := os.Stat(gitDir); err == nil && !info.IsDir() {
		if data, err := os.ReadFile(gitDir); err == nil {
			line := strings.TrimSpace(string(data))
			if strings.HasPrefix(line, "gitdir: ") {
				dir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(cleanRoot, dir)
				}
				gitDir = dir
			}
		}
	}
	excludePath := filepath.Join(gitDir, "info", "exclude")

	// Escape git-exclude glob characters in the path.
	entry := strings.NewReplacer(`*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(rel) + string(filepath.Separator)

	data, err := os.ReadFile(excludePath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == entry {
				return // already excluded
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		log.Printf("[worktree] project=%s failed to create info dir: %v", project.ID, err)
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[worktree] project=%s failed to open info/exclude: %v", project.ID, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString("\n# kanban worktrees (kept out of project status)\n" + entry + "\n"); err != nil {
		log.Printf("[worktree] project=%s failed to write info/exclude: %v", project.ID, err)
	}
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

// GarbageCollect removes worktree directories that have no matching task in the database.
func (wm *WorktreeManager) GarbageCollect(dryRun bool) (int, error) {
	cfg, err := wm.configStore.GetConfig()
	if err != nil {
		return 0, fmt.Errorf("get config: %w", err)
	}

	worktreesDir := cfg.WorkspaceDir
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

	// Also build a set of all branch names referenced by tasks.
	// Branches with slugified names (e.g., task/count-lines-in-hello-c) won't
	// match any task ID but are still in use.
	activeBranches, err := wm.store.GetAllTaskBranches()
	if err != nil {
		log.Printf("[gc] error getting task branches: %v", err)
	}
	activeBranchSet := make(map[string]bool, len(activeBranches))
	for _, b := range activeBranches {
		activeBranchSet[b] = true
	}

	// Get all active thread IDs for orphaned thread branch detection
	activeThreadIDs, err := wm.store.GetAllThreadIDs()
	if err != nil {
		log.Printf("[gc] error getting thread IDs: %v", err)
	}
	activeThreadSet := make(map[string]bool, len(activeThreadIDs))
	for _, id := range activeThreadIDs {
		activeThreadSet[id] = true
	}

	projects, err := wm.store.ListProjects()
	if err != nil {
		log.Printf("[gc] error listing projects: %v", err)
		return 0, nil
	}

	// Build project ID set for scanning worktrees directory
	projectSet := make(map[string]bool, len(projects))
	for _, p := range projects {
		projectSet[p.ID] = true
	}

	// Scan worktrees directory for orphaned task worktrees.
	// Path structure: worktreesDir/projectID/taskID
	removed := 0
	if entries, err := os.ReadDir(worktreesDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name()[0] == '.' {
				continue
			}
			projectID := entry.Name()
			if !projectSet[projectID] {
				continue // skip unknown project dirs (e.g., stale or test artifacts)
			}
			projectDir := filepath.Join(worktreesDir, projectID)
			taskEntries, err := os.ReadDir(projectDir)
			if err != nil {
				continue
			}
			for _, taskEntry := range taskEntries {
				if !taskEntry.IsDir() || taskEntry.Name()[0] == '.' {
					continue
				}
				taskName := taskEntry.Name()
				if !activeSet[taskName] {
					orphanPath := filepath.Join(projectDir, taskName)
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
		}
	}

	// Prune git worktree registrations and orphaned branches per project
	for _, p := range projects {
		wm.gitRun(p.Path, "worktree", "prune")
		// Keep slug-named Main branches of active titled tasks: once a fork becomes the
		// active branch, the slug branch leaves activeBranchSet and would be GC'd.
		slugPrefixes := taskSlugPrefixSet(wm.store, p.ID)
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
			if !activeSet[taskID] && !activeBranchSet[branch] && !slugPrefixes[types.TaskBranchSlugPrefix(branch)] {
				if dryRun {
					log.Printf("[gc] would delete orphaned branch %s in project %s", branch, p.Name)
				} else {
					log.Printf("[gc] deleting orphaned branch %s in project %s", branch, p.Name)
					wm.gitRun(p.Path, "branch", "-D", branch)
				}
			}
		}
		// Prune orphaned thread branches (thread/<threadID> with no matching thread in DB)
		threadBranchesOut, err := wm.gitRunOutput(p.Path, "branch", "--list", "thread/*")
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(threadBranchesOut, "\n") {
			branch := strings.TrimSpace(line)
			if branch == "" || !strings.HasPrefix(branch, "thread/") {
				continue
			}
			threadID := strings.TrimPrefix(branch, "thread/")
			if !activeThreadSet[threadID] {
				if dryRun {
					log.Printf("[gc] would delete orphaned thread branch %s in project %s", branch, p.Name)
				} else {
					log.Printf("[gc] deleting orphaned thread branch %s in project %s", branch, p.Name)
					wm.gitRun(p.Path, "branch", "-D", branch)
				}
			}
		}
	}

	// Drop kanban-labeled stashes whose task ID is no longer in the active set.
	// Label-based match (permissive), hash-based drop.
	for _, p := range projects {
		entries, err := wm.listStashEntries(p.Path)
		if err != nil {
			continue
		}
		var dropRefs []StashEntry
		for _, e := range entries {
			if !hasKanbanLabel(e.Subject) {
				continue
			}
			belongsToActiveTask := false
			for taskID := range activeSet {
				if matchKanbanTaskID(e.Subject, taskID) {
					belongsToActiveTask = true
					break
				}
			}
			if !belongsToActiveTask {
				dropRefs = append(dropRefs, e)
			}
		}
		wm.GetProjectStashMu(p.ID).Lock()
		// Reverse order: highest stash index first so refs don't shift.
		for i := len(dropRefs) - 1; i >= 0; i-- {
			e := dropRefs[i]
			if dryRun {
				log.Printf("[gc] would drop stale stash entry %s in project %s", e.Hash[:8], p.Name)
			} else {
				log.Printf("[gc] dropping stale stash entry %s in project %s", e.Hash[:8], p.Name)
				_ = wm.gitRun(p.Path, "stash", "drop", e.Ref)
			}
		}
		wm.GetProjectStashMu(p.ID).Unlock()
	}

	if dryRun {
		log.Printf("[gc] dry-run complete: %d orphaned worktrees would be removed", removed)
	} else {
		log.Printf("[gc] complete: %d orphaned worktrees removed", removed)
	}

	return removed, nil
}

// branchExistsInProject reports whether a git branch exists in a project's repo.
func (wm *WorktreeManager) branchExistsInProject(projectID, branch string) bool {
	_, err := wm.GitRunInProjectOutput(projectID, "rev-parse", "--verify", "--quiet", branch)
	return err == nil
}

// ResolveMainBranch returns the branch to use for a task's Main thread. A titled
// task's Main branch is slug-named (task/<slug>-<random6>), not task/<taskID>; if the
// canonical branch is missing, recover the slug branch. Falls back to canonicalBranch.
func (wm *WorktreeManager) ResolveMainBranch(projectID, taskID, title, canonicalBranch string) string {
	if canonicalBranch == "" {
		canonicalBranch = types.TaskBranchPrefix + taskID
	}
	if wm.branchExistsInProject(projectID, canonicalBranch) {
		return canonicalBranch
	}
	slug := types.SlugifyBranchName(title)
	if slug == "" {
		return canonicalBranch
	}
	prefix := types.TaskBranchPrefix + slug + "-"
	out, err := wm.GitRunInProjectOutput(projectID, "branch", "--list", prefix+"*")
	if err != nil {
		return canonicalBranch
	}
	var matches []string
	for line := range strings.SplitSeq(out, "\n") {
		b := strings.TrimSpace(line)
		b = strings.TrimPrefix(b, " + ")
		b = strings.TrimPrefix(b, "+ ")
		if b != "" && b != canonicalBranch {
			matches = append(matches, b)
		}
	}
	if len(matches) == 0 {
		return canonicalBranch
	}
	// Multiple slug matches (e.g. re-created tasks): pick the most recent commit.
	best := matches[0]
	var bestTs int64 = -1
	for _, b := range matches {
		tsOut, err := wm.GitRunInProjectOutput(projectID, "log", "-1", "--format=%ct", b)
		if err != nil {
			continue
		}
		var ts int64
		if _, err := fmt.Sscanf(strings.TrimSpace(tsOut), "%d", &ts); err == nil && ts > bestTs {
			bestTs = ts
			best = b
		}
	}
	return best
}

// taskSlugPrefixSet returns a set of task-branch slug prefixes ("task/<slug>-")
// for all active titled tasks in a project, so the GC can recognize and keep a
// titled task's slug-named Main branch even when a fork is the active branch.
// The slug comes from types.SlugifyBranchName — the same function the executor used
// to name the branch. If the two diverged, the GC would stop recognizing a live
// branch and delete it.
func taskSlugPrefixSet(store worktreeStore, projectID string) map[string]bool {
	set := make(map[string]bool)
	tasks, err := store.GetTasksByProject(projectID)
	if err != nil {
		return set
	}
	for i := range tasks {
		if slug := types.SlugifyBranchName(tasks[i].Title); slug != "" {
			set[types.TaskBranchPrefix+slug+"-"] = true
		}
	}
	return set
}

// PruneOrphanedBranches removes task/* and thread/* branches from a single project
// that no longer have matching entries in the database. This is a targeted version of
// the branch cleanup logic in GarbageCollect, suitable for calling after task deletion.
// Returns the number of branches removed.
func (wm *WorktreeManager) PruneOrphanedBranches(projectID string) int {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		log.Printf("[prune] error getting project %s: %v", projectID, err)
		return 0
	}

	activeTaskIDs, err := wm.store.GetAllTaskIDs()
	if err != nil {
		log.Printf("[prune] error getting task IDs: %v", err)
		return 0
	}
	activeTaskSet := make(map[string]bool, len(activeTaskIDs))
	for _, id := range activeTaskIDs {
		activeTaskSet[id] = true
	}

	// Also check branch names referenced by tasks (slugified names won't match task IDs)
	activeBranches, err := wm.store.GetAllTaskBranches()
	if err != nil {
		log.Printf("[prune] error getting task branches: %v", err)
	}
	activeBranchSet := make(map[string]bool, len(activeBranches))
	for _, b := range activeBranches {
		activeBranchSet[b] = true
	}

	activeThreadIDs, err := wm.store.GetAllThreadIDs()
	if err != nil {
		log.Printf("[prune] error getting thread IDs: %v", err)
	}
	activeThreadSet := make(map[string]bool, len(activeThreadIDs))
	for _, id := range activeThreadIDs {
		activeThreadSet[id] = true
	}

	removed := 0

	// Prune orphaned task branches (task/<id> with no matching task in DB)
	branchesOut, err := wm.gitRunOutput(project.Path, "branch", "--list", "task/*")
	if err == nil {
		for line := range strings.SplitSeq(branchesOut, "\n") {
			branch := strings.TrimSpace(line)
			if branch == "" || !strings.HasPrefix(branch, "task/") {
				continue
			}
			taskID := strings.TrimPrefix(branch, "task/")
			if !activeTaskSet[taskID] && !activeBranchSet[branch] {
				log.Printf("[prune] deleting orphaned branch %s in project %s", branch, project.Name)
				wm.gitRun(project.Path, "branch", "-D", branch)
				removed++
			}
		}
	}

	// Prune orphaned thread branches (thread/<threadID> with no matching thread in DB)
	threadBranchesOut, err := wm.gitRunOutput(project.Path, "branch", "--list", "thread/*")
	if err == nil {
		for line := range strings.SplitSeq(threadBranchesOut, "\n") {
			branch := strings.TrimSpace(line)
			if branch == "" || !strings.HasPrefix(branch, "thread/") {
				continue
			}
			threadID := strings.TrimPrefix(branch, "thread/")
			if !activeThreadSet[threadID] {
				log.Printf("[prune] deleting orphaned thread branch %s in project %s", branch, project.Name)
				wm.gitRun(project.Path, "branch", "-D", branch)
				removed++
			}
		}
	}

	if removed > 0 {
		log.Printf("[prune] project %s: removed %d orphaned branches", project.Name, removed)
	}

	return removed
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
		return "", ErrWorktreeNotFound
	}

	out, err := wm.gitRunOutput(worktreePath, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	return out, nil
}

// ParseStatusPaths reads `git status --porcelain` (v1) output into workspace-relative
// paths: two status columns, a space, then the path — "R  old -> new" for a rename (whose
// destination is the file that exists), quoted when it contains unusual characters.
// Directories reported as untracked ("?? dir/") come back as the directory, not expanded;
// callers that need files use walkUntrackedEntries for those.
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

// HasWorktreeChanges returns true if the worktree has uncommitted changes
// (modified, staged, or untracked files). Uses git status --porcelain.
func (wm *WorktreeManager) HasWorktreeChanges(taskID string) bool {
	out, err := wm.GitStatus(taskID)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// UntrackedFile holds the path and content lines for an untracked (new) file.
// Since untracked files are always new, we store raw lines instead of a unified diff.
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

// TaskDiff holds the diff output and list of untracked files for a task.
// Tracked is a per-file map for incremental updates. Diff is the legacy
// monolithic unified diff string (kept for backward compatibility).
type TaskDiff struct {
	Diff              string                      `json:"diff"`
	CommittedDiff     string                      `json:"-"` // raw diff of committed changes since baseRef (for hash computation)
	Tracked           map[string]*TrackedDiffFile `json:"-"` // per-file parsed diffs (not JSON-serialized in legacy format)
	Untracked         []UntrackedFile             `json:"untracked"`
	ShortStat         string                      `json:"short_stat"`
	TruncatedFiles    []string                    `json:"truncated_files,omitempty"`
	OriginalFileStats map[string]FileChangeStats  `json:"original_file_stats,omitempty"`
	StagedHash        string                      `json:"-"` // SHA-256 of staged state; changes diff hash when files are staged/unstaged
	StagedPaths       []string                    `json:"-"` // paths currently staged in the index
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

// ComputeUntrackedHash computes a SHA-256 hash for an untracked file based on its lines.
func ComputeUntrackedHash(u UntrackedFile) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(u.Path+strings.Join(u.Lines, "\n"))))
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
					// Remove overflow line from counts (it won't be appended),
					// then break to continue with the next file block.
					if line[0] == '-' {
						hunkOldLines--
					} else {
						hunkNewLines--
					}
					break
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
// annotations provides metadata for binary/large files whose hunks were replaced.
func parseUnifiedDiffToFiles(diff string, truncatedFiles []string, origStats map[string]FileChangeStats, annotations map[string]*fileAnnotation) map[string]*TrackedDiffFile {
	if diff == "" || !strings.Contains(diff, "\n") {
		// Even with empty diff, annotations may have binary/large file entries
		if len(annotations) > 0 {
			return annotationsToFiles(annotations)
		}
		return nil
	}

	truncatedSet := make(map[string]bool, len(truncatedFiles))
	for _, f := range truncatedFiles {
		truncatedSet[f] = true
	}

	files := make(map[string]*TrackedDiffFile)
	var current *TrackedDiffFile
	var oldLine, newLine int
	var inHunk bool

	lines := strings.Split(diff, "\n")
	// Remove trailing empty line
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			_, bPath := extractDiffPaths(line)
			// If the file already exists (e.g. from a previous diff segment),
			// merge the new hunks into the existing entry instead of overwriting.
			if existing, ok := files[bPath]; ok {
				current = existing
			} else {
				current = &TrackedDiffFile{Path: bPath, Rows: nil}
				files[bPath] = current
			}
			inHunk = false
			oldLine = 0
			newLine = 0
			continue
		}
		if strings.HasPrefix(line, "new file mode ") {
			if current != nil {
				current.IsNew = true
			}
			continue
		}
		if strings.HasPrefix(line, "deleted file mode ") {
			if current != nil {
				current.IsDeleted = true
			}
			continue
		}
		if strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		// Check for binary/large file annotation
		if strings.HasPrefix(line, "# binary-file: ") || strings.HasPrefix(line, "# large-file: ") {
			if current != nil {
				isBinary := strings.HasPrefix(line, "# binary-file: ")
				sizeStr := strings.TrimPrefix(line, "# binary-file: ")
				if !isBinary {
					sizeStr = strings.TrimPrefix(line, "# large-file: ")
				}
				if size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64); err == nil {
					current.FileSize = &size
					current.IsBinary = isBinary
					// Enrich with annotation metadata (origPath for renames)
					if ann := annotations[current.Path]; ann != nil && ann.origPath != "" {
						current.OrigPath = ann.origPath
					}
				}
			}
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			if m := hunkRe.FindStringSubmatch(line); m != nil {
				oldLine, _ = strconv.Atoi(m[1])
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
		if variant == "removed" {
			ln = oldLine
		}
		row := DiffRow{
			LineNumber: &ln,
			Variant:    variant,
			Text:       line[1:],
		}
		current.Rows = append(current.Rows, row)

		if variant == "context" || variant == "removed" {
			oldLine++
		}
		if variant == "context" || variant == "added" {
			newLine++
		}
	}

	// Enrich with truncation metadata + annotation data
	for path, f := range files {
		if len(f.Rows) == 0 && f.FileSize == nil && !f.IsNew && !f.IsDeleted {
			// Skip files with no diff content, but preserve new/deleted files
			// (a new or deleted blank file has a diff header but no hunk rows).
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
		// Enrich with annotation metadata for renames
		if f.FileSize != nil {
			if ann := annotations[path]; ann != nil && ann.origPath != "" {
				f.OrigPath = ann.origPath
			}
		}
	}

	return files
}

// annotationsToFiles creates TrackedDiffFile entries from annotations (for empty diffs with binary files).
func annotationsToFiles(annotations map[string]*fileAnnotation) map[string]*TrackedDiffFile {
	files := make(map[string]*TrackedDiffFile, len(annotations))
	for path, ann := range annotations {
		f := &TrackedDiffFile{
			Path:     ann.path,
			FileSize: &ann.size,
			IsBinary: ann.isBinary,
		}
		if ann.origPath != "" {
			f.OrigPath = ann.origPath
		}
		files[path] = f
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
	parts = append(parts, fmt.Sprintf("%d file%s changed", files, PluralS(files)))
	if totalAdded > 0 {
		parts = append(parts, fmt.Sprintf("%d insertion%s(+)", totalAdded, PluralS(totalAdded)))
	}
	if totalRemoved > 0 {
		parts = append(parts, fmt.Sprintf("%d deletion%s(-)", totalRemoved, PluralS(totalRemoved)))
	}
	return strings.Join(parts, ", ")
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

// dropMatchingStashes lists stashes in the project, filters by predicate, and drops by ref.
// Drops in reverse order (highest index first) so refs don't shift mid-loop.
// The caller must hold the project stash mutex.
func (wm *WorktreeManager) dropMatchingStashes(projectID string, filter func(subject string) bool) error {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	entries, err := wm.listStashEntries(project.Path)
	if err != nil {
		return nil
	}
	// Reverse order: highest stash index first so refs don't shift.
	for i := len(entries) - 1; i >= 0; i-- {
		if filter(entries[i].Subject) {
			_ = wm.gitRun(project.Path, "stash", "drop", entries[i].Ref)
		}
	}
	return nil
}

// DropStashEntriesForTask removes kanban-labeled stash entries for the given task.
// Drops by commit hash — no index renumbering issues.
func (wm *WorktreeManager) DropStashEntriesForTask(projectID, taskID string) error {
	wm.GetProjectStashMu(projectID).Lock()
	defer wm.GetProjectStashMu(projectID).Unlock()
	return wm.dropMatchingStashes(projectID, func(subject string) bool {
		return matchKanbanTaskID(subject, taskID)
	})
}

// DropStashEntriesForBranch removes stash entries for the given branch.
// Drops by commit hash — no index renumbering issues.
func (wm *WorktreeManager) DropStashEntriesForBranch(projectID string, branchName string) error {
	wm.GetProjectStashMu(projectID).Lock()
	defer wm.GetProjectStashMu(projectID).Unlock()
	branchMarker := " " + branchName + ":"
	return wm.dropMatchingStashes(projectID, func(subject string) bool {
		return strings.Contains(subject, branchMarker)
	})
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
		return nil, ErrWorktreeNotFound
	}

	// Reject invalid baseRef values (e.g. git error messages like "fatal: ...")
	if strings.HasPrefix(baseRef, "fatal:") {
		baseRef = ""
	}

	var diffOut, shortStat string
	var untracked []UntrackedFile
	var annotations map[string]*fileAnnotation

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
		diffOut, annotations = wm.filterLargeBinaryDiffs(worktreePath, diffOut)
		// No untracked files for commit-to-commit diff
	} else {
		// Single-ref diff: working tree vs baseRef.
		// Checkpoints use stash create+store, so changes stay visible in the working tree.
		// Working-tree diff: always compare against HEAD.
		// This captures only uncommitted changes (staged + unstaged).
		// Committed changes are handled separately below.
		diffOut, err = wm.gitRunOutput(worktreePath, "diff", "HEAD")
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				// Changes exist, diffOut has the output — continue
			} else {
				return nil, fmt.Errorf("git diff: %w", err)
			}
		}
		shortStat = shortStatFromDiff(diffOut)
		diffOut, annotations = wm.filterLargeBinaryDiffs(worktreePath, diffOut)

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
					// Skip content for large files or binary files.
					// Binary detection: extension check first (O(1)), null-byte scan as fallback.
					isBin := isBinaryExtension(filePath) || isBinaryContent(fullPath, fileSize)
					if fileSize > maxDiffSize || isBin {
						untracked = append(untracked, UntrackedFile{
							Path:     filePath,
							FileSize: fileSize,
							IsBinary: isBin,
						})
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

	// Compute staged hash so the diff hash changes on git add/reset.
	// Always compare against HEAD (current commit), NOT baseRef.
	// baseRef may be a different branch (e.g. "develop") and comparing
	// index vs develop would show ALL branch divergence as "staged".
	var stagedHash string
	var stagedPaths map[string]struct{}
	if headRef == "" {
		// git diff exits 1 when changes exist — output is still valid.
		stagedOut, stagedErr := wm.gitRunOutput(worktreePath, "diff", "--cached", "HEAD")
		if stagedErr != nil {
			if exitErr, ok := stagedErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				stagedHash = fmt.Sprintf("%x", sha256.Sum256([]byte(stagedOut)))
			}
		} else {
			stagedHash = fmt.Sprintf("%x", sha256.Sum256([]byte(stagedOut)))
		}
		// Get set of staged file paths to mark Staged on tracked entries.
		stagedNames, namesErr := wm.gitRunOutput(worktreePath, "diff", "--cached", "--name-only", "HEAD")
		if namesErr != nil {
			if exitErr, ok := namesErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				// Changes exist, output is valid
			} else {
				stagedNames = ""
			}
		}
		if stagedNames != "" {
			stagedPaths = make(map[string]struct{})
			for line := range strings.SplitSeq(stagedNames, "\n") {
				p := strings.TrimSpace(line)
				if p != "" {
					stagedPaths[p] = struct{}{}
				}
			}
		}
	}

	tracked := parseUnifiedDiffToFiles(diffOut, truncatedFiles, originalStats, annotations)
	if tracked == nil {
		tracked = make(map[string]*TrackedDiffFile)
	}
	var committedDiff string
	if baseRef != "" {
		committedOut, err := wm.gitRunOutput(worktreePath, "diff", fmt.Sprintf("%s..HEAD", baseRef))
		if err == nil && committedOut != "" {
			committedDiff = committedOut
			committedOut, committedAnnotations := wm.filterLargeBinaryDiffs(worktreePath, committedOut)
			committedFiles := parseUnifiedDiffToFiles(committedOut, nil, nil, committedAnnotations)
			for path, cf := range committedFiles {
				if tf, ok := tracked[path]; ok {
					tf.CommittedRows = cf.Rows
				} else {
					tracked[path] = &TrackedDiffFile{
						Path:          path,
						Rows:          nil,
						CommittedRows: cf.Rows,
						Added:         cf.Added,
						Removed:       cf.Removed,
						FileSize:      cf.FileSize,
						IsBinary:      cf.IsBinary,
						Truncated:     cf.Truncated,
						OrigAdded:     cf.OrigAdded,
						OrigRemoved:   cf.OrigRemoved,
					}
				}
			}
		}
	}

	// Convert to sorted slice for deterministic WebSocket broadcast.
	var stagedPathsList []string
	if stagedPaths != nil {
		stagedPathsList = make([]string, 0, len(stagedPaths))
		for p := range stagedPaths {
			stagedPathsList = append(stagedPathsList, p)
		}
	}

	return &TaskDiff{
		Diff:              diffOut,
		CommittedDiff:     committedDiff,
		Tracked:           tracked,
		Untracked:         untracked,
		ShortStat:         shortStat,
		TruncatedFiles:    truncatedFiles,
		OriginalFileStats: originalStats,
		StagedHash:        stagedHash,
		StagedPaths:       stagedPathsList,
	}, nil
}

// TaskDiffFromRepo computes a commit-to-commit TaskDiff
func (wm *WorktreeManager) TaskDiffFromRepo(repoPath, baseRef, headRef string, skipTruncate ...bool) (*TaskDiff, error) {
	if repoPath == "" || headRef == "" {
		return nil, fmt.Errorf("TaskDiffFromRepo: repoPath and headRef are required")
	}
	target := baseRef
	if target == "" {
		target = "HEAD"
	}
	diffOut, err := wm.gitRunOutput(repoPath, "diff", fmt.Sprintf("%s..%s", target, headRef))
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	shortStat := shortStatFromDiff(diffOut)
	diffOut, annotations := wm.filterLargeBinaryDiffs(repoPath, diffOut)

	var truncatedFiles []string
	var originalStats map[string]FileChangeStats
	if doTruncate := len(skipTruncate) == 0 || !skipTruncate[0]; doTruncate {
		originalStats = countFileChanges(diffOut)
		diffOut, truncatedFiles = truncateDiffLines(diffOut, defaultMaxChangeLines)
	}
	tracked := parseUnifiedDiffToFiles(diffOut, truncatedFiles, originalStats, annotations)
	if tracked == nil {
		tracked = make(map[string]*TrackedDiffFile)
	}
	return &TaskDiff{
		Diff:              diffOut,
		Tracked:           tracked,
		Untracked:         []UntrackedFile{},
		ShortStat:         shortStat,
		TruncatedFiles:    truncatedFiles,
		OriginalFileStats: originalStats,
	}, nil
}

// -- Helper functions --

// fileAnnotation holds metadata for a file whose diff was replaced with an annotation.
type fileAnnotation struct {
	path     string // b/ path (current name)
	origPath string // a/ path (empty if same as path)
	size     int64
	isBinary bool
}

// filterLargeBinaryDiffs replaces diff hunks for binary or >1MB files with size annotations.
// Returns the filtered diff and a map of file annotations for the parser to consume.
// Binary detection uses git's own "Binary files a/X and b/Y differ" marker.
// Large-file detection uses os.Stat() (>1MB text files).
func (wm *WorktreeManager) filterLargeBinaryDiffs(worktreePath, diffText string) (string, map[string]*fileAnnotation) {
	if diffText == "" {
		return diffText, nil
	}
	const maxDiffSize = 1 << 20 // 1MB
	annotations := make(map[string]*fileAnnotation)
	var result strings.Builder
	lines := strings.Split(diffText, "\n")
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "diff --git ") {
			// Extract a/ and b/ paths from "diff --git a/X b/Y"
			aPath, bPath := extractDiffPaths(line)

			// Scan header lines for "Binary files a/X and b/Y differ" marker
			// and the index line for blob hashes (fallback size lookup).
			isBinary := false
			var binarySize int64
			var blobHash string
			// Write the "diff --git" line and advance past it
			result.WriteString(line + "\n")
			i++
			for i < len(lines) {
				l := lines[i]
				if strings.HasPrefix(l, "@@ ") || strings.HasPrefix(l, "diff --git ") {
					break
				}
				if strings.HasPrefix(l, "Binary files ") {
					isBinary = true
					binarySize = extractBinarySize(l)
				}
				if strings.HasPrefix(l, "index ") && blobHash == "" {
					// index abc123..def456 mode — grab the second hash (new blob)
					blobHash = extractBlobHash(l)
				}
				result.WriteString(l + "\n")
				i++
			}

			if isBinary {
				// Git confirmed binary — resolve size.
				if binarySize == 0 {
					checkPath := bPath
					if checkPath == "" || strings.Contains(line, "/dev/null") {
						checkPath = aPath
					}
					if size, ok := getFileSize(worktreePath, checkPath, blobHash, wm); ok {
						binarySize = size
					}
				}
				result.WriteString(fmt.Sprintf("# binary-file: %d\n", binarySize))
				for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
					i++
				}
				annotations[bPath] = &fileAnnotation{
					path:     bPath,
					origPath: aPath,
					size:     binarySize,
					isBinary: true,
				}
				continue
			}

			// Not binary — check if it's a large text file.
			if size, ok := getFileSize(worktreePath, bPath, "", wm); ok && size > maxDiffSize {
				result.WriteString(fmt.Sprintf("# large-file: %d\n", size))
				for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
					i++
				}
				annotations[bPath] = &fileAnnotation{
					path:     bPath,
					origPath: aPath,
					size:     size,
					isBinary: false,
				}
				continue
			}
			// Normal text file — headers already written by inner loop.
			// Continue outer loop to process hunk lines from current i position.
			continue
		}
		result.WriteString(line + "\n")
		i++
	}
	out := result.String()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, annotations
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

// extractBlobHash extracts the second (new) hash from "index abc..def mode" line.
func extractBlobHash(line string) string {
	line = strings.TrimPrefix(line, "index ")
	parts := strings.SplitN(line, "..", 2)
	if len(parts) == 2 {
		// Second hash may be followed by " mode..."
		hash := strings.SplitN(parts[1], " ", 2)[0]
		return hash
	}
	return ""
}

// extractDiffPaths pulls both a/ and b/ paths from "diff --git a/X b/Y" header.
func extractDiffPaths(line string) (aPath, bPath string) {
	line = strings.TrimPrefix(line, "diff --git ")
	// Parse "a/path b/path"
	if !strings.HasPrefix(line, "a/") {
		return "", ""
	}
	line = line[2:] // strip "a/"
	aPath = line
	// Find " b/" separator
	if idx := strings.Index(line, " b/"); idx >= 0 {
		aPath = line[:idx]
		bPath = line[idx+3:] // strip " b/"
	} else {
		bPath = aPath
	}
	return aPath, bPath
}

// binaryExts is a set of file extensions considered binary.
var binaryExts = map[string]bool{
	// Images
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".ico": true, ".tiff": true, ".tif": true, ".svg": true,
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

// gitRunInGitDir runs git against dir's own gitdir and returns what git printed. The output is the point:
// git's reason for refusing is on stderr, and a caller that keeps only the exit code cannot tell the user
// anything ("exit status 1").
func (wm *WorktreeManager) gitRunInGitDir(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(dir, ".git"))
	out, err := cmd.CombinedOutput()
	return string(out), err
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

// DivergenceBase returns the commit at which taskBranch diverged from the project's main branch
func (wm *WorktreeManager) DivergenceBase(projectPath, mainBranch, taskBranch string) string {
	if projectPath == "" || taskBranch == "" {
		return ""
	}
	if mainBranch == "" {
		var err error
		mainBranch, err = wm.gitCurrentBranch(projectPath)
		if err != nil {
			return ""
		}
	}
	if mainBranch == "" || mainBranch == "HEAD" {
		// Detached HEAD — no main branch to measure divergence from.
		return ""
	}
	out, err := wm.gitRunOutput(projectPath, "merge-base", taskBranch, mainBranch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
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
func (wm *WorktreeManager) StageIndexedFiles(taskID string) {
	out, err := wm.GitRunInWorktreeOutput(taskID, "diff", "--staged", "--name-only", "-z", "HEAD")
	if err != nil {
		log.Printf("[stage] task=%s failed to list staged files: %v", taskID, err)
		return
	}
	// Output is NUL-separated; the final token after the trailing NUL is empty.
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return
	}
	args := append([]string{"add", "--"}, paths...)
	if err := wm.GitRunInWorktree(taskID, args...); err != nil {
		log.Printf("[stage] task=%s failed to re-stage %d file(s): %v", taskID, len(paths), err)
	}
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
		return nil, ErrWorktreeNotFound
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

// IsAncestor reports whether `ancestor` is an ancestor of (or equal to) `descendant`.
func (wm *WorktreeManager) IsAncestor(taskID, ancestor, descendant string) bool {
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return false
	}
	cmd := exec.Command("git", "-C", worktreePath, "merge-base", "--is-ancestor", ancestor, descendant)
	return cmd.Run() == nil
}

// IsMergedInto checks if the task branch has been merged into the target branch.
// Uses git merge-base --is-ancestor to check if taskBranch is an ancestor of targetBranch.
// Returns true if merged, false if not merged or if check fails.
func (wm *WorktreeManager) IsMergedInto(projectPath, taskBranch, targetBranch string) bool {
	if taskBranch == "" || targetBranch == "" {
		return false
	}
	cmd := exec.Command("git", "-C", projectPath, "merge-base", "--is-ancestor", taskBranch, targetBranch)
	return cmd.Run() == nil
}

// ResetWorktreeTo restores the files a snapshot captured and never moves HEAD. The restored files land on
// the current branch as unstaged/untracked changes, which is what git status, the diff views and the
// commit flow read as the task's work.
//
// The snapshot is read as a tree, not applied as a patch: a patch cannot carry a binary file, and the old
// `reset --hard` fallback moved the branch onto the snapshot commit — restoring files by rewriting history.
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

// stashUntrackedCommit returns the commit holding a stash entry's untracked files, when hash names a
// stash commit made with -u. Git keeps those files only in the third parent, so reading the stash
// commit's own tree would lose them.
func (wm *WorktreeManager) stashUntrackedCommit(dir, hash string) (string, bool) {
	out, err := wm.gitRunOutput(dir, "rev-list", "--parents", "-n1", hash)
	if err != nil {
		return "", false
	}
	ancestors := strings.Fields(out)
	if len(ancestors) != 4 {
		return "", false // not a stash commit with an untracked parent
	}
	candidate := ancestors[3]
	files, err := wm.gitRunOutput(dir, "ls-tree", "-r", "--name-only", candidate)
	if err != nil || strings.TrimSpace(files) == "" {
		return "", false // nothing to overlay
	}
	return candidate, true
}

// branchExistsInRepo checks if a branch exists in the given git repo.
func (wm *WorktreeManager) branchExistsInRepo(repoPath, branchName string) bool {
	_, err := wm.gitRunOutput(repoPath, "rev-parse", "--verify", branchName)
	return err == nil
}

// StashEntry holds a parsed stash list entry.
type StashEntry struct {
	Ref     string // stash@{N}
	Hash    string // commit hash
	Subject string // reflog subject
}

// listStashEntries parses "git stash list --format=%gd%x00%H%x00%gs" output.
func (wm *WorktreeManager) listStashEntries(repoPath string) ([]StashEntry, error) {
	out, err := wm.gitRunOutput(repoPath, "stash", "list", "--format=%gd%x00%H%x00%gs")
	if err != nil {
		return nil, err
	}
	var entries []StashEntry
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) < 3 {
			continue
		}
		entries = append(entries, StashEntry{
			Ref:     parts[0],
			Hash:    strings.TrimSpace(parts[1]),
			Subject: parts[2],
		})
	}
	return entries, nil
}

// PushStash stashes the worktree changes under `message` and returns the hash of the entry it created.
// The entry is identified by diffing the stash list across the push: the stack is shared by every worktree
// of the repository and holds the user's own entries, so the entry on top is not necessarily the one we
// just made, and handing it back would save somebody else's state as this thread's.
//
// The caller must hold the project stash mutex.
func (wm *WorktreeManager) PushStash(repoPath, message string) (string, error) {
	before, err := wm.stashHashSet(repoPath)
	if err != nil {
		return "", fmt.Errorf("stash list: %w", err)
	}
	if out, err := wm.gitRunInGitDir(repoPath, "stash", "push", "-u", "-m", message); err != nil {
		return "", fmt.Errorf("stash push: %s", gitMessage(out, err))
	}
	entries, err := wm.listStashEntries(repoPath)
	if err != nil {
		return "", fmt.Errorf("stash list: %w", err)
	}
	var fresh []StashEntry
	for _, e := range entries { // newest first
		if !before[e.Hash] {
			fresh = append(fresh, e)
		}
	}
	for _, e := range fresh {
		if strings.Contains(e.Subject, message) {
			return e.Hash, nil
		}
	}
	switch len(fresh) {
	case 0:
		return "", ErrNothingToStash
	case 1:
		// git rewrapped the label; the one entry that appeared during our push is ours.
		return fresh[0].Hash, nil
	default:
		return "", fmt.Errorf("stash push: %d entries appeared and none carries %q", len(fresh), message)
	}
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

// DropStashByHash drops a stash entry identified by its commit hash.
// Searches the stash list to find the matching entry and drops it by ref.
// The caller must hold the project stash mutex.
func (wm *WorktreeManager) DropStashByHash(repoPath, hash string) error {
	if hash == "" {
		return nil
	}
	hash = strings.TrimSpace(hash)
	entries, err := wm.listStashEntries(repoPath)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Hash == hash || strings.HasPrefix(e.Hash, hash) {
			return wm.gitRun(repoPath, "stash", "drop", e.Ref)
		}
	}
	return wm.gitRun(repoPath, "stash", "drop", "stash@{0}")
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

func StashMessageForSyncMerge(taskID string) string {
	return stashMessageWithPrefix(stashLabelSyncMerge, taskID)
}

// StashMessageForAutoMerge returns a stash message for an auto-merge operation.
func StashMessageForAutoMerge(taskID string) string {
	return stashMessageWithPrefix(stashLabelAutoMerge, taskID)
}

var fileRe = regexp.MustCompile(`^diff --git a/(.+?) b/`)

// IsGitRepo checks if a path is a git repository.
func IsGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	cmd.Dir = path
	return cmd.Run() == nil
}

// HasGitDir reports whether path carries a .git entry of its own — a repository
// root, a linked worktree (where .git is a file), or a submodule checkout. One
// stat instead of a `git rev-parse` process, for callers that check many entries.
//
// Narrower than IsGitRepo on purpose: rev-parse walks up the tree, so it also
// says yes to a plain subdirectory of a repository. Ask that question once for
// the directory being listed — when it is yes, every entry is inside the same
// repository.
func HasGitDir(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}
