package managers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"
)

// GitHandler handles git operations.
type GitHandler struct {
	store       *store.Store
	worktreeMgr *git.WorktreeManager
	diffCache   sync.Map // taskID -> *git.TaskDiff (full result including untracked file content)
}

// NewGitHandler creates a new GitHandler.
func NewGitHandler(store *store.Store, worktreeMgr *git.WorktreeManager) *GitHandler {
	return &GitHandler{store: store, worktreeMgr: worktreeMgr}
}

// UpdateDiffCache stores a precomputed TaskDiff in the cache.
// Called by the worktree watcher so that on-demand file fetches work without a full /diff call.
func (h *GitHandler) UpdateDiffCache(taskID string, taskDiff *git.TaskDiff) {
	h.diffCache.Store(taskID, taskDiff)
}

// HandleLog returns commit log for a project.
// With ?full=1 query param, returns extended fields: hash, author, date, parents, branches.
// Also includes current_branch and origin_tip metadata.
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if full {
		args := []string{"-C", project.Path, "log"}
		if offset > 0 {
			args = append(args, "--skip", strconv.Itoa(offset))
		}
		if limit > 0 {
			// Fetch one extra to detect if more commits exist
			args = append(args, "-n", strconv.Itoa(limit+1))
		}
		args = append(args, "--format=%H|%h|%s|%aN|%aI|%P%n---BODY---%n%b%n---END---", branch)
		c := exec.Command("git", args...)
		out, err := c.CombinedOutput()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
			return
		}
		blocks := strings.Split(string(out), "---END---")
		var commits []types.GitCommit
		for _, block := range blocks {
			block = strings.TrimSpace(block)
			if block == "" {
				continue
			}
			blockLines := strings.SplitN(block, "\n", 2)
			parts := strings.SplitN(blockLines[0], "|", 6)
			if len(parts) < 5 {
				continue
			}
			commit := types.GitCommit{
				Hash:    parts[0],
				Short:   parts[1],
				Subject: parts[2],
				Author:  parts[3],
				Date:    parts[4],
			}
			if len(parts) == 6 && parts[5] != "" {
				commit.Parents = strings.Fields(parts[5])
			}
			// Extract body from the rest of the block
			if len(blockLines) > 1 {
				rest := blockLines[1]
				// Remove "---BODY---" prefix and leading blank line
				if strings.HasPrefix(rest, "---BODY---\n") {
					rest = strings.TrimPrefix(rest, "---BODY---\n")
				} else if strings.HasPrefix(rest, "---BODY---") {
					rest = ""
				}
				commit.Body = strings.TrimSpace(rest)
			}
			commits = append(commits, commit)
		}

		// Resolve current branch name
		currentBranch := h.getCurrentBranch(project.Path)

		// Build branch-to-commit mapping from local refs
		branchMap := h.getBranchMap(project.Path)

		// Attach branch names to commits
		for i := range commits {
			if branches, ok := branchMap[commits[i].Hash]; ok {
				commits[i].Branches = branches
			}
		}

		// Get origin tip (where origin/<current-branch> points)
		originTip := ""
		if currentBranch != "" && currentBranch != "HEAD" {
			originTip = h.getOriginTip(project.Path, currentBranch)
		}

		// Trim extra commit used to detect if more exist
		hasMore := false
		if limit > 0 && len(commits) > limit {
			hasMore = true
			commits = commits[:limit]
		}

		resp := types.GitLogResponse{
			Commits:       commits,
			CurrentBranch: currentBranch,
			OriginTip:     originTip,
			HasMore:       hasMore,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	args := []string{"-C", project.Path, "log", "--oneline", "--decorate"}
	if offset > 0 {
		args = append(args, "--skip", strconv.Itoa(offset))
	}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	args = append(args, branch)
	c := exec.Command("git", args...)
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

// getCurrentBranch returns the name of the currently checked-out branch.
func (h *GitHandler) getCurrentBranch(path string) string {
	c := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getBranchMap returns a map of commit hash -> branch names for all local branches.
func (h *GitHandler) getBranchMap(path string) map[string][]string {
	c := exec.Command("git", "-C", path, "for-each-ref", "--format=%(objectname)|%(refname:short)", "refs/heads/")
	out, err := c.CombinedOutput()
	if err != nil {
		return nil
	}
	m := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) == 2 {
			m[parts[0]] = append(m[parts[0]], parts[1])
		}
	}
	return m
}

// getOriginTip returns the commit hash where origin/<branch> points, or empty string if no remote.
func (h *GitHandler) getOriginTip(path, branch string) string {
	c := exec.Command("git", "-C", path, "rev-parse", "origin/"+branch)
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
// Supports:
//   - ?ref=X         — git diff X (working tree vs ref, legacy)
//   - ?base=A&head=B — git diff A..B (commit-to-commit comparison)
func (h *GitHandler) HandleDiff(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}

	base := r.URL.Query().Get("base")
	head := r.URL.Query().Get("head")

	var c *exec.Cmd
	if base != "" && head != "" {
		// Commit-to-commit comparison
		// Special case: empty tree hash is a virtual constant, not a real git object.
		// git diff EMPTY_TREE..HEAD fails with "Invalid revision range".
		// Use git diff-tree --root instead to show the root commit's changes.
		if base == git.EmptyTreeHash {
			c = exec.Command("git", "-C", project.Path, "diff-tree", "--root", "--format=", "-p", head)
		} else {
			c = exec.Command("git", "-C", project.Path, "diff", base+".."+head)
		}
	} else {
		// Legacy: working tree vs ref
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "HEAD"
		}
		c = exec.Command("git", "-C", project.Path, "diff", ref)
	}

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
		// Orphaned worktree — return 404
		if strings.Contains(err.Error(), "exit status 128") {
			httputil.JSONError(w, http.StatusNotFound, "worktree not found")
			return
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
		// Branch may not exist (e.g., orphaned worktree). Return empty commits.
		log.Printf("[git] task=%s branch %s not found in project, returning empty commits: %v", task.ID, target, err)
		return result, nil
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
		// Orphaned worktree — return 404
		if strings.Contains(err.Error(), "exit status 128") {
			httputil.JSONError(w, http.StatusNotFound, "worktree not found")
			return
		}
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
			return
		}
	}
	if taskDiff.Untracked == nil {
		taskDiff.Untracked = []git.UntrackedFile{}
	}
	// Cache the full result (including untracked file content) for on-demand access
	h.diffCache.Store(taskID, taskDiff)

	// For default working tree diff, return a manifest (per-file metadata only).
	// Custom ref comparisons and explicit full=1 requests return full content.
	if headRef == "" && customBase == "" && !fullDiff {
		manifest := buildDiffManifest(taskDiff)
		httputil.JSONResponse(w, http.StatusOK, manifest)
		return
	}

	// Strip Lines from untracked files for the response
	taskDiff = stripUntrackedLines(taskDiff)
	httputil.JSONResponse(w, http.StatusOK, taskDiff)
}

// stripUntrackedLines creates a copy of TaskDiff with Lines removed from untracked entries.
// Sets LineCount so the frontend knows the file size without fetching content.
func stripUntrackedLines(d *git.TaskDiff) *git.TaskDiff {
	copy := *d
	copy.Untracked = make([]git.UntrackedFile, len(d.Untracked))
	for i, u := range d.Untracked {
		entry := u
		if len(u.Lines) > 0 {
			entry.LineCount = len(u.Lines)
			entry.Lines = nil
		}
		copy.Untracked[i] = entry
	}
	return &copy
}

// diffManifestFile is a single file entry in the diff manifest.
type diffManifestFile struct {
	Hash      string `json:"hash"`
	Type      string `json:"type"` // "tracked" or "untracked"
	Added     int    `json:"added,omitempty"`
	Removed   int    `json:"removed,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	OrigAdded int    `json:"origAdded,omitempty"`
	OrigRemoved int  `json:"origRemoved,omitempty"`
	LineCount int    `json:"line_count,omitempty"` // for untracked files
	FileSize  int64  `json:"file_size,omitempty"`  // for binary/large files
	IsBinary  bool   `json:"is_binary,omitempty"`  // true for binary files
}

// diffManifest is the lightweight manifest returned by GET /diff.
// Contains per-file metadata (hashes, stats) without the actual diff content.
type diffManifest struct {
	Files     map[string]diffManifestFile `json:"files"`
	ShortStat string                      `json:"short_stat"`
}

// buildDiffManifest creates a manifest from a TaskDiff with per-file hashes.
func buildDiffManifest(d *git.TaskDiff) diffManifest {
	manifest := diffManifest{
		Files:     make(map[string]diffManifestFile),
		ShortStat: d.ShortStat,
	}
	for path, f := range d.Tracked {
		entry := diffManifestFile{
			Hash:        git.ComputeFileHash(f),
			Type:        "tracked",
			Added:       f.Added,
			Removed:     f.Removed,
			Truncated:   f.Truncated,
			OrigAdded:   f.OrigAdded,
			OrigRemoved: f.OrigRemoved,
		}
		if f.FileSize != nil {
			entry.FileSize = *f.FileSize
		}
		entry.IsBinary = f.IsBinary
		manifest.Files[path] = entry
	}
	for _, u := range d.Untracked {
		manifest.Files[u.Path] = diffManifestFile{
			Hash:        git.ComputeUntrackedHash(u),
			Type:        "untracked",
			LineCount:   len(u.Lines),
			FileSize:    u.FileSize,
			Added:       len(u.Lines),
			Truncated:   u.Truncated,
			OrigAdded:   u.OrigAdded,
			IsBinary:    u.IsBinary,
		}
	}
	return manifest
}

// HandleTaskDiffFile returns the content of a single untracked file from the cached diff.
// Query params: path (file path relative to worktree).
// Returns 404 if the task's diff is not cached or the file is not found.
func (h *GitHandler) HandleTaskDiffFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := r.PathValue("taskID")
	filePath := r.URL.Query().Get("path")
	fullFile := r.URL.Query().Get("full") == "1"
	if filePath == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	cached, ok := h.diffCache.Load(taskID)
	if !ok {
		httputil.JSONError(w, http.StatusNotFound, "diff not available (open the Changes panel first)")
		return
	}
	taskDiff := cached.(*git.TaskDiff)
	// Check tracked files first
	if f, ok := taskDiff.Tracked[filePath]; ok {
		// If full=1 and file was truncated, recompute diff without truncation
		if fullFile && f.Truncated {
			task, err := h.store.GetTaskByTaskID(taskID)
			if err == nil && h.worktreeMgr != nil {
				baseRef, _ := h.worktreeMgr.ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, "")
				if fullDiff, err := h.worktreeMgr.GetTaskDiff(taskID, baseRef, "", true); err == nil {
					if ff, ok := fullDiff.Tracked[filePath]; ok {
						f = ff
					}
				}
			}
		}
		httputil.JSONResponse(w, http.StatusOK, map[string]any{
			"rows":        f.Rows,
			"truncated":   f.Truncated,
			"origAdded":   f.OrigAdded,
			"origRemoved": f.OrigRemoved,
		})
		return
	}
	// Then check untracked files
	for _, u := range taskDiff.Untracked {
		if u.Path == filePath {
			lines := u.Lines
			// If full=1 and file was truncated, re-read from disk
			if fullFile && u.Truncated && h.worktreeMgr != nil {
				worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
				if err == nil {
					fullPath := filepath.Join(worktreePath, filePath)
					data, err := os.ReadFile(fullPath)
					if err == nil {
						lines = git.SplitLines(string(data))
					}
				}
			}
			httputil.JSONResponse(w, http.StatusOK, map[string]any{
				"lines":      lines,
				"line_count": len(lines),
				"truncated":  false,
				"origAdded":  len(lines),
			})
			return
		}
	}
	httputil.JSONError(w, http.StatusNotFound, "file not found in diff")
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
