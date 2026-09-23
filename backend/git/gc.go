package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mike/backend/fsutil"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/logging"
)

// threadBranchNamespace is the fork branch namespace; server/handler_thread.go writes the same name.
const threadBranchNamespace = "thread/"

func setOf(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// liveBranchAndThreadRefs returns the branch names tasks carry and the IDs threads hold — the two lists
// a sweep must not delete. A read that fails warns and leaves that set empty, so those refs read as
// absent; the caller decides what that means.
func (wm *WorktreeManager) liveBranchAndThreadRefs() (branches, threads map[string]bool) {
	activeBranches, err := wm.store.GetAllTaskBranches()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error getting task branches: %v", err)
	}
	branches = setOf(activeBranches)

	activeThreadIDs, err := wm.store.GetAllThreadIDs()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error getting thread IDs: %v", err)
	}
	threads = setOf(activeThreadIDs)
	return branches, threads
}

// GarbageCollect removes worktree directories that have no matching task in the database.
func (wm *WorktreeManager) GarbageCollect(dryRun bool) (int, error) {
	worktreesDir := wm.worktreesRoot()

	if _, err := os.Stat(worktreesDir); os.IsNotExist(err) {
		return 0, nil
	}

	activeTaskIDs, err := wm.store.GetAllTaskIDs()
	if err != nil {
		return 0, fmt.Errorf("get task IDs: %w", err)
	}
	activeSet := setOf(activeTaskIDs)
	activeBranchSet, activeThreadSet := wm.liveBranchAndThreadRefs()

	projects, err := wm.store.ListProjects()
	if err != nil {
		logging.For(logging.Worktree).Warnf("error listing projects: %v", err)
		return 0, nil
	}

	// Layout: worktreesDir/projectID/taskID.
	removed := 0
	projectSet := make(map[string]bool, len(projects))
	for _, p := range projects {
		projectSet[p.ID] = true
	}

	if entries, err := os.ReadDir(worktreesDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name()[0] == '.' {
				continue
			}
			projectID := entry.Name()
			if !projectSet[projectID] {
				continue
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

	for _, p := range projects {
		// The same lock CreateWorktree takes: prune rewrites the repository's .git/worktrees/ registry
		// while add writes into it. It is released after the prune, not held across the sweep.
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

	// Drop kanban-labeled stashes whose task ID is no longer in the active set. The match is by label;
	// the drop uses the positional ref from this listing, which is why the loop runs in reverse.
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

// PruneOrphanedBranches removes task/* and thread/* branches from a single project that no longer have
// a matching entry in the database. It returns the number of branches removed.
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
	activeTaskSet := setOf(activeTaskIDs)
	activeBranchSet, activeThreadSet := wm.liveBranchAndThreadRefs()

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

// taskSlugPrefixSet returns the task-branch slug prefixes ("task/<slug>-") of a project's active titled
// tasks, so the sweep keeps a slug-named Main branch even when a fork is the active branch. The slug
// comes from types.SlugifyBranchName, the function that named the branch: a sweep parsing a different
// shape deletes a live branch.
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

// ensureWorktreesExcluded keeps untracked worktree files out of the project's git status by excluding the
// worktrees dir when it is inside the project. Best-effort: never blocks worktree creation.
func (wm *WorktreeManager) ensureWorktreesExcluded(project *types.Project, worktreesDir string) {
	cleanRoot := filepath.Clean(project.Path)
	rel, err := filepath.Rel(cleanRoot, filepath.Clean(worktreesDir))
	if err != nil || !fsutil.RelBelowRoot(rel) {
		return
	}

	entry := strings.NewReplacer(`*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(rel) + string(filepath.Separator)
	if err := EnsureExcluded(cleanRoot, entry, "# kanban worktrees (kept out of project status)"); err != nil {
		logging.For(logging.Worktree).With(logging.ProjectID, project.ID).Warnf("failed to exclude the worktrees dir: %v", err)
	}
}

// EnsureExcluded appends one gitignore pattern to the repository's local .git/info/exclude, once. That
// file belongs to the clone rather than the tree — never committed, shared by every worktree — and its
// patterns are relative to the checkout root given here, so one meant to match inside a worktree must stay
// unanchored.
func EnsureExcluded(projectRoot, pattern, header string) error {
	excludePath, err := repoExcludeFile(projectRoot)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", excludePath, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil
		}
	}

	infoDir := filepath.Dir(excludePath)
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", infoDir, err)
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", excludePath, err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n" + header + "\n" + pattern + "\n"); err != nil {
		return fmt.Errorf("write %s: %w", excludePath, err)
	}
	return nil
}

// repoExcludeFile locates the repository's local exclude file. It lives in the common git dir: a checkout
// made by `git worktree add` has a gitdir of its own and git reads exclude from the shared one, so a
// pattern written under the per-checkout gitdir is a silent no-op.
func repoExcludeFile(projectRoot string) (string, error) {
	out, err := runGitInDir(projectRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("find the git dir of %s: %w (%s)", projectRoot, err, strings.TrimSpace(out))
	}
	common := strings.TrimSpace(out)
	if !filepath.IsAbs(common) {
		return "", fmt.Errorf("find the git dir of %s: git answered %q", projectRoot, common)
	}
	return filepath.Join(common, "info", "exclude"), nil
}
