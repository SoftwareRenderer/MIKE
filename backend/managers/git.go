package managers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"
)

// GitHandler handles git operations.
type GitHandler struct {
	store       *store.Store
	worktreeMgr *git.WorktreeManager
}

// NewGitHandler creates a new GitHandler.
func NewGitHandler(store *store.Store, worktreeMgr *git.WorktreeManager) *GitHandler {
	return &GitHandler{store: store, worktreeMgr: worktreeMgr}
}

// HandleLog returns commit log for a project.
// With ?full=1 query param, returns extended fields: hash, author, date, parents.
func (h *GitHandler) HandleLog(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "HEAD"
	}

	full := r.URL.Query().Get("full") == "1"

	if full {
		c := exec.Command("git", "-C", project.Path, "log", "--format=%H|%h|%s|%aI|%P", branch)
		out, err := c.CombinedOutput()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
			return
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		var commits []types.GitCommit
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "|", 5)
			if len(parts) < 4 {
				continue
			}
			commit := types.GitCommit{
				Hash:    parts[0],
				Short:   parts[1],
				Subject: parts[2],
				Author:  "",
				Date:    parts[3],
			}
			if len(parts) == 5 && parts[4] != "" {
				commit.Parents = strings.Fields(parts[4])
			}
			commits = append(commits, commit)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(commits)
		return
	}

	c := exec.Command("git", "-C", project.Path, "log", "--oneline", "--decorate", branch)
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []types.GitCommit
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		commit := types.GitCommit{Short: parts[0], Subject: parts[1]}
		commits = append(commits, commit)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(commits)
}

// HandleBranches lists branches for a project.
func (h *GitHandler) HandleBranches(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "branch", "-a", "--format=%(refname:short)")
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git branches failed: %v", err))
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var branches []string
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	httputil.JSONResponse(w, http.StatusOK, branches)
}

// HandleCurrentBranch returns the current branch name for a project.
func (h *GitHandler) HandleCurrentBranch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := c.CombinedOutput()
	branch := strings.TrimSpace(string(out))
	if err != nil {
		// Not a git repo or other error
		httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": ""})
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": branch})
}

// HandleDiff returns diff between refs.
func (h *GitHandler) HandleDiff(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "HEAD"
	}
	c := exec.Command("git", "-C", project.Path, "diff", ref)
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(out)
}

// HandleStatus returns working tree status.
func (h *GitHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "status", "--porcelain")
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}
	clean := len(strings.TrimSpace(string(out))) == 0
	httputil.JSONResponse(w, http.StatusOK, map[string]any{"clean": clean, "status": string(out)})
}

// HandleCheckout checks out a branch.
func (h *GitHandler) HandleCheckout(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	var req types.GitCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	c := exec.Command("git", "-C", project.Path, "checkout", req.Branch)
	if out, err := c.CombinedOutput(); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git checkout failed: %v: %s", err, out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": req.Branch})
}

// HandlePull pulls from remote.
func (h *GitHandler) HandlePull(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "pull")
	if out, err := c.CombinedOutput(); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git pull failed: %v: %s", err, out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "pulled"})
}

// HandlePush pushes to remote.
func (h *GitHandler) HandlePush(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	c := exec.Command("git", "-C", project.Path, "push")
	if out, err := c.CombinedOutput(); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git push failed: %v: %s", err, out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "pushed"})
}

// HandleTaskCommits returns the commit log for a task's worktree branch.
// Only commits after the divergence from the original branch are returned.
// Also returns the base commit info for display in the diff selector.
// Falls back to project repo when worktree doesn't exist (e.g., after commit-and-done).
func (h *GitHandler) HandleTaskCommits(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}
	if task.Status == "backlog" {
		httputil.JSONResponse(w, http.StatusOK, git.TaskCommitsResult{Commits: []git.TaskCommit{}})
		return
	}
	// Resolve the baseline ref to filter commits to only those on the task branch
	thread, _ := h.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := h.worktreeMgr.ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, forkBranch)

	result, err := h.worktreeMgr.GetTaskCommits(taskID, baseRef)
	if err != nil {
		// Worktree may not exist (e.g., after commit-and-done).
		// Fall back to project repo if task has a branch.
		if task.Branch != "" && strings.Contains(err.Error(), "worktree not found") {
			result, err = h.getTaskCommitsFromProject(projectID, task, baseRef)
		}
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
			return
		}
	}
	if result.Commits == nil {
		result.Commits = []git.TaskCommit{}
	}
	httputil.JSONResponse(w, http.StatusOK, result)
}

// getTaskCommitsFromProject runs git log from the project repo using the task's branch.
// Uses BranchTip (captured at commit-and-done time) if available, otherwise falls back to Branch.
func (h *GitHandler) getTaskCommitsFromProject(projectID string, task *types.Task, baseRef string) (*git.TaskCommitsResult, error) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}

	result := &git.TaskCommitsResult{Commits: []git.TaskCommit{}}

	// Get base commit info
	baseOut, err := exec.Command("git", "-C", project.Path, "log", "--format=%H|%h|%s|%aI", "-1", baseRef).CombinedOutput()
	if err == nil {
		line := strings.TrimSpace(string(baseOut))
		parts := strings.SplitN(line, "|", 4)
		if len(parts) == 4 {
			result.BaseCommit = &git.TaskCommit{
				Hash:    parts[0],
				Short:   parts[1],
				Subject: parts[2],
				Date:    parts[3],
			}
		}
	}

	// Use branch tip captured at commit time, or fall back to branch name
	target := task.BranchTip
	if target == "" {
		target = task.Branch
	}
	ref := fmt.Sprintf("%s..%s", baseRef, target)
	out, err := exec.Command("git", "-C", project.Path, "log", "--format=%H|%h|%s|%aI", ref).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git log from project: %s: %w", string(out), err)
	}

	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		result.Commits = append(result.Commits, git.TaskCommit{
			Hash:    parts[0],
			Short:   parts[1],
			Subject: parts[2],
			Date:    parts[3],
		})
	}
	return result, nil
}

// HandleTaskDiff returns diff and untracked files for a task's worktree.
// Optional query params:
//   - base: custom base ref (overrides resolved baseline)
//   - head: second ref for commit-to-commit diff (git diff base..head)
//
// Falls back to project repo when worktree doesn't exist (e.g., after commit-and-done).
func (h *GitHandler) HandleTaskDiff(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}

	headRef := r.URL.Query().Get("head")
	customBase := r.URL.Query().Get("base")
	fullDiff := r.URL.Query().Get("full") == "1"

	var baseRef string
	if customBase != "" {
		baseRef = customBase
	} else {
		thread, _ := h.store.GetThread(task.CurrentThreadID)
		forkBranch := ""
		if thread != nil {
			forkBranch = thread.GitBranch
		}
		baseRef, _ = h.worktreeMgr.ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, forkBranch)
	}

	// For stash refs, resolve to the common ancestor (state before stash changes).
	// This shows changes accumulated UP TO (but not including) the checkpoint.
	// Falls back to headRef^ (HEAD at stash time) if the stash is not in the list.
	if headRef != "" && baseRef != "" {
		resolved, err := h.worktreeMgr.ResolveStashParent(taskID, baseRef, headRef)
		if err == nil {
			headRef = resolved
		}
	}

	taskDiff, err := h.worktreeMgr.GetTaskDiff(taskID, baseRef, headRef, fullDiff)
	if err != nil {
		// Worktree may not exist (e.g., after commit-and-done).
		// Fall back to project repo if task has a branch.
		if task.Branch != "" && strings.Contains(err.Error(), "worktree not found") {
			taskDiff, err = h.getTaskDiffFromProject(projectID, taskID, task, baseRef, headRef)
		}
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
			return
		}
	}
	if taskDiff.Untracked == nil {
		taskDiff.Untracked = []git.UntrackedFile{}
	}
	httputil.JSONResponse(w, http.StatusOK, taskDiff)
}

// getTaskDiffFromProject runs git diff from the project repo using the task's branch.
// Uses BranchTip (captured at commit-and-done time) if available, otherwise falls back to Branch.
func (h *GitHandler) getTaskDiffFromProject(projectID, taskID string, task *types.Task, baseRef, headRef string) (*git.TaskDiff, error) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}

	// Determine the refs to diff
	var refSpec string
	if headRef != "" {
		refSpec = fmt.Sprintf("%s..%s", baseRef, headRef)
	} else {
		// Use branch tip captured at commit time, or fall back to branch name
		target := task.BranchTip
		if target == "" {
			target = task.Branch
		}
		refSpec = fmt.Sprintf("%s..%s", baseRef, target)
	}

	c := exec.Command("git", "-C", project.Path, "diff", refSpec)
	out, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff from project: %s: %w", string(out), err)
	}

	shortStatOut, _ := exec.Command("git", "-C", project.Path, "diff", "--shortstat", refSpec).CombinedOutput()

	return &git.TaskDiff{
		Diff:      string(out),
		Untracked: []git.UntrackedFile{},
		ShortStat: string(shortStatOut),
	}, nil
}

// HandleResetWorktree resets the task's worktree to a specific commit.
// Used when deleting messages after a checkpoint to roll back file changes.
func (h *GitHandler) HandleResetWorktree(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		httputil.JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		CommitHash string `json:"commit_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CommitHash == "" {
		httputil.JSONError(w, http.StatusBadRequest, "commit_hash required")
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}
	// Only reset if worktree exists and task is active
	if task.Status == "backlog" || task.Status == "done" {
		httputil.JSONError(w, http.StatusBadRequest, "task must be in_progress or review")
		return
	}
	// Reset worktree to the checkpoint (commit hash or stash hash)
	if err := h.worktreeMgr.ResetWorktreeTo(taskID, req.CommitHash); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("reset failed: %v", err))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "reset"})
}
