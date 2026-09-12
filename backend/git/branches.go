package git

import (
	"fmt"
	"strings"

	"kanban/backend/types"
	"kanban/pkg/logging"
)

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
	all, err := wm.ListProjectBranches(projectID, prefix)
	if err != nil {
		return canonicalBranch
	}
	var matches []string
	for _, b := range all {
		if b != canonicalBranch {
			matches = append(matches, b)
		}
	}
	if len(matches) == 0 {
		return canonicalBranch
	}
	// Multiple slug matches (e.g. re-created tasks): pick the most recent commit.
	best := matches[0]
	var bestTS int64 = -1
	for _, b := range matches {
		tsOut, err := wm.GitRunInProjectOutput(projectID, "log", "-1", "--format=%ct", b)
		if err != nil {
			continue
		}
		var ts int64
		if _, err := fmt.Sscanf(strings.TrimSpace(tsOut), "%d", &ts); err == nil && ts > bestTS {
			bestTS = ts
			best = b
		}
	}
	return best
}

// ListProjectBranches lists a project's branch names under a prefix, such as "task/" or one task's
// slug prefix. The rule about listings is in listBranchNames.
func (wm *WorktreeManager) ListProjectBranches(projectID, prefix string) ([]string, error) {
	project, err := wm.store.GetProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return wm.listBranchNames(project.Path, prefix)
}

// listBranchNames lists refs/heads/<prefix>*, one path segment deep - the shape this repo writes.
// It reads refs with for-each-ref because `git branch --list` prefixes entries with "+ " or "* ".
func (wm *WorktreeManager) listBranchNames(dir, prefix string) ([]string, error) {
	// A plain for-each-ref pattern matches only up to a slash boundary, so the glob is needed for a
	// slug prefix to match anything.
	pattern := "refs/heads/" + prefix
	if !strings.HasSuffix(pattern, "*") {
		pattern += "*"
	}
	out, err := wm.gitRunOutput(dir, "for-each-ref", "--format=%(refname:short)", pattern)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// deleteBranch force-deletes a branch and puts what git printed in the error, since git gives its
// reason on stderr. -D, not -d: the row that owned the branch is already gone.
func (wm *WorktreeManager) deleteBranch(dir, branch string) error {
	out, err := wm.gitRunOutput(dir, "branch", "-D", branch)
	if err == nil {
		return nil
	}
	logging.For(logging.Worktree).Warnf("deleting branch %s failed: %s", branch, strings.TrimSpace(out))
	return fmt.Errorf("delete branch %s: %s", branch, strings.TrimSpace(out))
}

// branchExistsInProject reports whether a git branch exists in a project's repo.
func (wm *WorktreeManager) branchExistsInProject(projectID, branch string) bool {
	_, err := wm.GitRunInProjectOutput(projectID, "rev-parse", "--verify", "--quiet", branch)
	return err == nil
}

// branchExistsInRepo checks if a branch exists in the given git repo.
func (wm *WorktreeManager) branchExistsInRepo(repoPath, branchName string) bool {
	_, err := wm.gitRunOutput(repoPath, "rev-parse", "--verify", branchName)
	return err == nil
}

func (wm *WorktreeManager) gitCurrentBranch(dir string) (string, error) {
	out, err := wm.gitRunOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out), err
}

// IsMergedInto checks if the task branch has been merged into the target branch.
// Uses git merge-base --is-ancestor to check if taskBranch is an ancestor of targetBranch.
// Returns true if merged, false if not merged or if check fails.
func (wm *WorktreeManager) IsMergedInto(projectPath, taskBranch, targetBranch string) bool {
	if taskBranch == "" || targetBranch == "" {
		return false
	}
	cmd := Command("", "-C", projectPath, "merge-base", "--is-ancestor", taskBranch, targetBranch)
	return cmd.Run() == nil
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
