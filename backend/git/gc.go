package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kanban/backend/store"
	"kanban/backend/types"
	"kanban/pkg/logging"
)

// threadBranchNamespace is the fork branch namespace; server/handler_thread.go writes the same name.
const threadBranchNamespace = "thread/"

// GarbageCollect removes worktree directories that have no matching task in the database.
func (wm *WorktreeManager) GarbageCollect(dryRun bool) (int, error) {
	worktreesDir, err := wm.worktreesRoot()
	if err != nil {
		return 0, err
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
		logging.For(logging.Worktree).Warnf("error getting task branches: %v", err)
	}
	activeBranchSet := make(map[string]bool, len(activeBranches))
	for _, b := range activeBranches {
		activeBranchSet[b] = true
	}

	// Get all active thread IDs for orphaned thread branch detection
	activeThreadIDs, err := wm.store.GetAllThreadIDs()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error getting thread IDs: %v", err)
	}
	activeThreadSet := make(map[string]bool, len(activeThreadIDs))
	for _, id := range activeThreadIDs {
		activeThreadSet[id] = true
	}

	projects, err := wm.store.ListProjects()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error listing projects: %v", err)
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
						logging.For(logging.Worktree).Infof("would remove orphaned worktree: %s", orphanPath)
						removed++
					} else {
						logging.For(logging.Worktree).Infof("removing orphaned worktree: %s", orphanPath)
						if err := os.RemoveAll(orphanPath); err != nil {
							logging.For(logging.Worktree).Warnf("error removing %s: %v", orphanPath, err)
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
		// The same lock CreateWorktree takes: prune rewrites the repository's .git/worktrees/ registry while
		// add writes into it. Scoped to the prune, not the whole GC body, because that body continues and a
		// lock released on some paths only is how a permanently-locked project is born.
		if unlockWorktree, locked := wm.tryLockProjectWorktree(p.ID, worktreeLockTimeout); locked {
			wm.gitRun(p.Path, "worktree", "prune")
			unlockWorktree()
		} else {
			// Branch GC is skipped with it: this project is busy, and the next GC round covers it.
			logging.For(logging.Worktree).Infof("skipping project %s this round: a worktree operation holds the lock", p.Name)
			continue
		}
		// Keep slug-named Main branches of active titled tasks: once a fork becomes the
		// active branch, the slug branch leaves activeBranchSet and would be GC'd.
		slugPrefixes := taskSlugPrefixSet(wm.store, p.ID)
		// Prune orphaned task branches (task/<id> with no matching task in DB)
		branches, err := wm.listBranchNames(p.Path, types.TaskBranchPrefix)
		if err != nil {
			continue
		}
		for _, branch := range branches {
			taskID := strings.TrimPrefix(branch, types.TaskBranchPrefix)
			if !activeSet[taskID] && !activeBranchSet[branch] && !slugPrefixes[types.TaskBranchSlugPrefix(branch)] {
				if dryRun {
					logging.For(logging.Worktree).Infof("would delete orphaned branch %s in project %s", branch, p.Name)
				} else {
					logging.For(logging.Worktree).Infof("deleting orphaned branch %s in project %s", branch, p.Name)
					wm.deleteBranch(p.Path, branch)
				}
			}
		}
		// Prune orphaned thread branches (thread/<threadID> with no matching thread in DB)
		threadBranches, err := wm.listBranchNames(p.Path, threadBranchNamespace)
		if err != nil {
			continue
		}
		for _, branch := range threadBranches {
			threadID := strings.TrimPrefix(branch, threadBranchNamespace)
			if !activeThreadSet[threadID] {
				if dryRun {
					logging.For(logging.Worktree).Infof("would delete orphaned thread branch %s in project %s", branch, p.Name)
				} else {
					logging.For(logging.Worktree).Infof("deleting orphaned thread branch %s in project %s", branch, p.Name)
					wm.deleteBranch(p.Path, branch)
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
		unlockStash := wm.LockProjectStash(p.ID)
		// Reverse order: highest stash index first so refs don't shift.
		for i := len(dropRefs) - 1; i >= 0; i-- {
			e := dropRefs[i]
			if dryRun {
				logging.For(logging.Worktree).Infof("would drop stale stash entry %s in project %s", e.Hash[:8], p.Name)
			} else {
				logging.For(logging.Worktree).Infof("dropping stale stash entry %s in project %s", e.Hash[:8], p.Name)
				_ = wm.gitRun(p.Path, "stash", "drop", e.Ref)
			}
		}
		unlockStash()
	}

	if dryRun {
		logging.For(logging.Worktree).Infof("dry-run complete: %d orphaned worktrees would be removed", removed)
	} else {
		logging.For(logging.Worktree).Infof("complete: %d orphaned worktrees removed", removed)
	}

	return removed, nil
}

// PruneOrphanedBranches removes task/* and thread/* branches from a single project
// that no longer have matching entries in the database. This is a targeted version of
// the branch cleanup logic in GarbageCollect, suitable for calling after task deletion.
// Returns the number of branches removed.
func (wm *WorktreeManager) PruneOrphanedBranches(projectID string) int {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		logging.For(logging.Worktree).Warnf("error getting project %s: %v", projectID, err)
		return 0
	}

	activeTaskIDs, err := wm.store.GetAllTaskIDs()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error getting task IDs: %v", err)
		return 0
	}
	activeTaskSet := make(map[string]bool, len(activeTaskIDs))
	for _, id := range activeTaskIDs {
		activeTaskSet[id] = true
	}

	// Also check branch names referenced by tasks (slugified names won't match task IDs)
	activeBranches, err := wm.store.GetAllTaskBranches()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error getting task branches: %v", err)
	}
	activeBranchSet := make(map[string]bool, len(activeBranches))
	for _, b := range activeBranches {
		activeBranchSet[b] = true
	}

	activeThreadIDs, err := wm.store.GetAllThreadIDs()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error getting thread IDs: %v", err)
	}
	activeThreadSet := make(map[string]bool, len(activeThreadIDs))
	for _, id := range activeThreadIDs {
		activeThreadSet[id] = true
	}

	// Prune before listing: a branch a registration still holds is one git refuses to delete
	// (RemoveWorktree). The prune takes the lock CreateWorktree takes, as in GarbageCollect.
	unlockWorktree, locked := wm.tryLockProjectWorktree(projectID, worktreeLockTimeout)
	if !locked {
		logging.For(logging.Worktree).Infof("skipping branch sweep for project %s: a worktree operation holds the lock", project.Name)
		return 0
	}
	_ = wm.gitRun(project.Path, "worktree", "prune")
	unlockWorktree()

	removed := 0

	// Prune orphaned task branches (task/<id> with no matching task in DB)
	if branches, err := wm.listBranchNames(project.Path, types.TaskBranchPrefix); err == nil {
		for _, branch := range branches {
			taskID := strings.TrimPrefix(branch, types.TaskBranchPrefix)
			if !activeTaskSet[taskID] && !activeBranchSet[branch] {
				logging.For(logging.Worktree).Infof("deleting orphaned branch %s in project %s", branch, project.Name)
				if wm.deleteBranch(project.Path, branch) == nil {
					removed++
				}
			}
		}
	}

	// Prune orphaned thread branches (thread/<threadID> with no matching thread in DB)
	if threadBranches, err := wm.listBranchNames(project.Path, threadBranchNamespace); err == nil {
		for _, branch := range threadBranches {
			threadID := strings.TrimPrefix(branch, threadBranchNamespace)
			if !activeThreadSet[threadID] {
				logging.For(logging.Worktree).Infof("deleting orphaned thread branch %s in project %s", branch, project.Name)
				if wm.deleteBranch(project.Path, branch) == nil {
					removed++
				}
			}
		}
	}

	if removed > 0 {
		logging.For(logging.Worktree).Infof("project %s: removed %d orphaned branches", project.Name, removed)
	}

	return removed
}

// taskSlugPrefixSet returns the task-branch slug prefixes ("task/<slug>-") of a project's active
// titled tasks, so the GC keeps a slug-named Main branch even when a fork is the active branch.
// The slug comes from types.SlugifyBranchName, the same function that named the branch: a GC
// parsing a different shape deletes a live branch.
func taskSlugPrefixSet(store *store.Store, projectID string) map[string]bool {
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

// HasWorktreeChanges returns true if the worktree has uncommitted changes
// (modified, staged, or untracked files). Uses git status --porcelain.
func (wm *WorktreeManager) HasWorktreeChanges(taskID string) bool {
	out, err := wm.GitStatus(taskID)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
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
		logging.For(logging.Worktree).With(logging.ProjectID, project.ID).Warnf("failed to create info dir: %v", err)
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		logging.For(logging.Worktree).With(logging.ProjectID, project.ID).Warnf("failed to open info/exclude: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString("\n# kanban worktrees (kept out of project status)\n" + entry + "\n"); err != nil {
		logging.For(logging.Worktree).With(logging.ProjectID, project.ID).Warnf("failed to write info/exclude: %v", err)
	}
}
