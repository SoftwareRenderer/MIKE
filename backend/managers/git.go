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
	"kanban/backend/stream"
	"kanban/backend/types"
)

// GitHandler handles git operations.
type GitHandler struct {
	store       *store.Store
	worktreeMgr *git.WorktreeManager
	diffCache   sync.Map // taskID -> *git.TaskDiff (full result including untracked file content)
	// getWorktreeMutex returns a per-task mutex for serializing git operations.
	// Set by the server to prevent concurrent worktree mutations.
	getWorktreeMutex func(taskID string) *sync.Mutex
	// getProjectMutex returns a per-project mutex for serializing project-level
	// git operations (checkout, pull, push) that touch shared state.
	getProjectMutex func(projectID string) *sync.Mutex
}

// NewGitHandler creates a new GitHandler.
func NewGitHandler(store *store.Store, worktreeMgr *git.WorktreeManager) *GitHandler {
	return &GitHandler{store: store, worktreeMgr: worktreeMgr}
}

// SetWorktreeMutex sets the callback for acquiring per-task worktree mutexes.
// Called by the server to wire up the shared mutex map.
func (h *GitHandler) SetWorktreeMutex(fn func(taskID string) *sync.Mutex) {
	h.getWorktreeMutex = fn
}

// SetProjectMutex sets the callback for acquiring per-project mutexes.
// Called by the server to wire up the shared project mutex map.
func (h *GitHandler) SetProjectMutex(fn func(projectID string) *sync.Mutex) {
	h.getProjectMutex = fn
}

// UpdateDiffCache stores a precomputed TaskDiff in the cache.
// Called by the worktree watcher so that on-demand file fetches work without a full /diff call.
func (h *GitHandler) UpdateDiffCache(taskID string, taskDiff *git.TaskDiff) {
	h.diffCache.Store(taskID, taskDiff)
}

// GetDiffManifest builds a diff manifest for a task's worktree.
// Returns the manifest (JSON-serializable) with per-file metadata and staged status.
// Used by broadcastTaskDiff to send manifest format via WebSocket.
func (h *GitHandler) GetDiffManifest(taskID, baseRef string) (any, error) {
	if h.worktreeMgr == nil {
		return nil, fmt.Errorf("worktree manager not available")
	}
	taskDiff, err := h.worktreeMgr.GetTaskDiff(taskID, baseRef, "")
	if err != nil {
		return nil, err
	}
	if taskDiff.Untracked == nil {
		taskDiff.Untracked = []git.UntrackedFile{}
	}
	// Cache the full result for on-demand file fetches
	h.diffCache.Store(taskID, taskDiff)
	stagedPaths := getStagedPaths(h.worktreeMgr, taskID, baseRef)
	return buildDiffManifest(taskDiff, stagedPaths, baseRef), nil
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
	if branch != "" {
		if err := types.ValidateBranchName(branch); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		branch = "HEAD"
	}

	path := r.URL.Query().Get("path")
	if path != "" {
		// Basic sanitization: must be relative, no parent traversal
		if filepath.IsAbs(path) || strings.Contains(path, "..") || strings.HasPrefix(path, "~") {
			httputil.JSONError(w, http.StatusBadRequest, "invalid path")
			return
		}
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
		if path != "" {
			args = append(args, "--", path)
		}
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
	if path != "" {
		args = append(args, "--", path)
	}
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

// HandleBranch returns the current branch name for a project.
func (h *GitHandler) HandleBranch(w http.ResponseWriter, r *http.Request) {
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
//   - ?cached=1      — git diff --cached (staged changes only)
func (h *GitHandler) HandleDiff(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}

	base := r.URL.Query().Get("base")
	head := r.URL.Query().Get("head")
	cached := r.URL.Query().Get("cached") == "1"

	// Validate user-supplied refs to prevent git argument injection.
	if base != "" {
		if err := types.ValidateBranchName(base); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if head != "" {
		if err := types.ValidateBranchName(head); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	var c *exec.Cmd
	if cached {
		// Staged changes only
		c = exec.Command("git", "-C", project.Path, "diff", "--cached")
	} else if base != "" && head != "" {
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
		// Unstaged changes: working tree vs index (git diff with no args)
		// This excludes staged changes, which are shown separately via cached=1
		c = exec.Command("git", "-C", project.Path, "diff")
	}

	out, err := c.CombinedOutput()
	if err != nil {
		// git diff exits 1 when there are changes — output is still valid
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Changes exist, output is valid
		} else {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(out)
}

// HandleStatus returns working tree status, current HEAD hash, and git author identity.
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
	// Fetch HEAD hash for staleness detection
	var headHash string
	if headOut, err := exec.Command("git", "-C", project.Path, "rev-parse", "HEAD").CombinedOutput(); err == nil {
		headHash = strings.TrimSpace(string(headOut))
	}
	// Fetch git author identity for commit dialog
	var authorName, authorEmail string
	if nameOut, err := exec.Command("git", "-C", project.Path, "config", "user.name").CombinedOutput(); err == nil {
		authorName = strings.TrimSpace(string(nameOut))
	}
	if emailOut, err := exec.Command("git", "-C", project.Path, "config", "user.email").CombinedOutput(); err == nil {
		authorEmail = strings.TrimSpace(string(emailOut))
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]any{
		"clean":       clean,
		"status":      string(out),
		"head":        headHash,
		"authorName":  authorName,
		"authorEmail": authorEmail,
	})
}

// HandleProjectDiffFile returns the diff for a single file in the project.
// Query params: path (file path relative to project root), staged (true/false).
// When staged=true, returns staged (index) diff. When false/omitted, returns working tree diff.
// Also returns the full file content (from working tree or index) so the frontend can render it.
// When meta=true, only lightweight metadata (line_count, file_size, is_binary) is returned,
// skipping the diff and full file lines — used to list untracked files cheaply.
func (h *GitHandler) HandleProjectDiffFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	staged := r.URL.Query().Get("staged") == "true"
	metaOnly := r.URL.Query().Get("meta") == "1"

	// Get the diff for this file
	var c *exec.Cmd
	if staged {
		c = exec.Command("git", "-C", project.Path, "diff", "--cached", "--", filePath)
	} else {
		c = exec.Command("git", "-C", project.Path, "diff", "--", filePath)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		// git diff exits 1 when there are changes — output is still valid
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Changes exist, output is valid
		} else {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git diff failed: %v", err))
			return
		}
	}

	// Get file content from working tree or index
	var fileContent string
	var lineCount int
	var isBinary bool
	if staged {
		// Read from index (staged version)
		c = exec.Command("git", "-C", project.Path, "show", ":"+filePath)
		contentOut, contentErr := c.CombinedOutput()
		if contentErr == nil {
			fileContent = string(contentOut)
			lineCount = strings.Count(fileContent, "\n")
			if fileContent != "" && fileContent[len(fileContent)-1] != '\n' {
				lineCount++
			}
		}
	} else {
		// Read from working tree
		fullPath := filepath.Join(project.Path, filePath)
		contentOut, contentErr := os.ReadFile(fullPath)
		if contentErr == nil {
			fileContent = string(contentOut)
			lineCount = strings.Count(fileContent, "\n")
			if fileContent != "" && fileContent[len(fileContent)-1] != '\n' {
				lineCount++
			}
		}
	}

	// Check if binary
	if fileContent != "" {
		for i := 0; i < len(fileContent) && i < 8192; i++ {
			if fileContent[i] == 0 {
				isBinary = true
				break
			}
		}
	}

	// File size from disk (used for metadata and binary detection)
	var fileSize int64
	if st, err := os.Stat(filepath.Join(project.Path, filePath)); err == nil {
		fileSize = st.Size()
	}

	// Only report file_size when the file is binary or too large to render inline
	const maxDiffSize = 1 << 20 // 1MB
	reportFileSize := isBinary || (!isBinary && fileSize > maxDiffSize)

	if metaOnly {
		resp := map[string]any{
			"line_count": lineCount,
			"isBinary":   isBinary,
		}
		if reportFileSize {
			resp["file_size"] = fileSize
		}
		httputil.JSONResponse(w, http.StatusOK, resp)
		return
	}

	// Split content into lines
	var lines []string
	if !isBinary && fileContent != "" {
		lines = strings.Split(fileContent, "\n")
		// Remove trailing empty element from final newline
		if len(lines) > 0 && lines[len(lines)-1] == "" && strings.HasSuffix(fileContent, "\n") {
			lines = lines[:len(lines)-1]
		}
	}

	resp := map[string]any{
		"diff":       string(out),
		"lines":      lines,
		"line_count": lineCount,
		"isBinary":   isBinary,
	}
	if reportFileSize {
		resp["file_size"] = fileSize
	}
	httputil.JSONResponse(w, http.StatusOK, resp)
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
	if err := types.ValidateBranchName(req.Branch); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Acquire per-project mutex to serialize project-level git operations.
	if h.getProjectMutex != nil {
		mu := h.getProjectMutex(projectID)
		mu.Lock()
		defer mu.Unlock()
	}
	c := exec.Command("git", "-C", project.Path, "checkout", req.Branch)
	if out, err := c.CombinedOutput(); err != nil {
		log.Printf("git checkout failed: %v: %s", err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git checkout failed")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": req.Branch})
}

// gitErrorSummary returns a trimmed, length-capped summary
func gitErrorSummary(out []byte) string {
	s := strings.TrimSpace(string(out))
	const maxLen = 300
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

// HandlePull pulls from remote.
func (h *GitHandler) HandlePull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	// Acquire per-project mutex to serialize project-level git operations.
	if h.getProjectMutex != nil {
		mu := h.getProjectMutex(projectID)
		mu.Lock()
		defer mu.Unlock()
	}
	c := exec.Command("git", "-C", project.Path, "pull")
	// Never block on an interactive credential prompt.
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := c.CombinedOutput(); err != nil {
		log.Printf("git pull failed: %v: %s", err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git pull failed: "+gitErrorSummary(out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "pulled"})
}

// HandlePush pushes to remote.
func (h *GitHandler) HandlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	// Acquire per-project mutex to serialize project-level git operations.
	if h.getProjectMutex != nil {
		mu := h.getProjectMutex(projectID)
		mu.Lock()
		defer mu.Unlock()
	}
	c := exec.Command("git", "-C", project.Path, "push")
	// Never block on an interactive credential prompt.
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := c.CombinedOutput(); err != nil {
		log.Printf("git push failed: %v: %s", err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git push failed: "+gitErrorSummary(out))
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
	project, _ := h.store.GetProject(projectID)
	projectPath, originBranch := "", ""
	if project != nil {
		projectPath, originBranch = project.Path, project.OriginBranch
	}
	baseRef, _ := h.worktreeMgr.ResolveDiffBaseline(taskID, h.worktreeMgr.ResolveTaskBaseline(projectPath, originBranch, task), task.Branch, forkBranch)

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
	checkpointIdxStr := r.URL.Query().Get("checkpointIdx")

	// Validate user-supplied refs to prevent git argument injection.
	if headRef != "" {
		if err := types.ValidateBranchName(headRef); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if customBase != "" {
		if err := types.ValidateBranchName(customBase); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	checkpointIdx := -1
	if checkpointIdxStr != "" {
		checkpointIdx, _ = strconv.Atoi(checkpointIdxStr)
	}

	// The canonical done-task diff (pinned BaseCommit..BranchTip) is immutable —
	// serve it from the persistent snapshot instead of re-running git on every
	// open. Arbitrary ref comparisons (custom base/head), checkpoint diffs, and
	// full (untruncated) requests still go through the live computation.
	if task.Status == "done" && task.BranchTip != "" && customBase == "" && checkpointIdxStr == "" && !fullDiff &&
		(headRef == "" || headRef == task.BranchTip) {
		if d, err := stream.MaterializeDoneTaskDiff(h.store, h.worktreeMgr, projectID, task); err == nil && d != nil && d.HasChanges {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(d.DiffJSON))
			return
		}
		// Snapshot error or empty diff: fall through to the live computation below.
	}

	// For done tasks, always show the committed diff (BaseCommit..BranchTip).
	// If no explicit head is provided, use BranchTip to trigger commit-to-commit diff.
	// This ensures the response is a raw diff string (not a working-tree manifest),
	// which works correctly even when the worktree no longer exists.
	if task.Status == "done" && headRef == "" && customBase == "" {
		headRef = task.BranchTip
		if headRef == "" {
			headRef = task.Branch
		}
	}

	var baseRef string
	if customBase != "" {
		baseRef = customBase
	} else if headRef != "" && task.BaseCommit != "" {
		// Done-task diff: use the base ref commit captured at task start time.
		// After a fast-forward merge, the base ref branch pointer has moved forward,
		// so using the branch name would give an empty diff.
		baseRef = task.BaseCommit
	} else {
		thread, _ := h.store.GetThread(task.CurrentThreadID)
		forkBranch := ""
		if thread != nil {
			forkBranch = thread.GitBranch
		}
		// Use the captured base commit for the original baseline to avoid moving branch pointers.
		project, _ := h.store.GetProject(projectID)
		projectPath, originBranch := "", ""
		if project != nil {
			projectPath, originBranch = project.Path, project.OriginBranch
		}
		baseRef, _ = h.worktreeMgr.ResolveDiffBaseline(taskID, h.worktreeMgr.ResolveTaskBaseline(projectPath, originBranch, task), task.Branch, forkBranch)
	}

	// Use headRef directly to show changes accumulated up to AND including the checkpoint.
	// The checkpoint hash (stash commit) contains the full working tree state at that point.
	// If headRef resolves to the same commit as baseRef (empty diff), fall back to stash.
	// This handles the case where checkpoint_commit_hash is a branch name (e.g. "master")
	// instead of a stash hash — older data may have branch names stored.
	if headRef != "" && baseRef != "" {
		headResolved, _ := h.worktreeMgr.GitRunInWorktreeOutput(taskID, "rev-parse", "--verify", headRef)
		baseResolved, _ := h.worktreeMgr.GitRunInWorktreeOutput(taskID, "rev-parse", "--verify", baseRef)
		if strings.TrimSpace(headResolved) == strings.TrimSpace(baseResolved) {
			// headRef and baseRef point to the same commit — would give empty diff.
			// Fall back to stash at the matching checkpoint index.
			stashHash, err := h.worktreeMgr.GetStashByCheckpointIndex(taskID, checkpointIdx)
			if err == nil && stashHash != "" {
				headRef = stashHash
			} else {
				// Fallback: use latest stash if index resolution fails
				latestStash, err2 := h.worktreeMgr.GetLatestStashHash(taskID)
				if err2 == nil && latestStash != "" {
					headRef = latestStash
				}
			}
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
		if err != nil && strings.Contains(err.Error(), "exit status 128") {
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
		// Discover which files are staged (in the index but differ from target).
		stagedPaths := getStagedPaths(h.worktreeMgr, taskID, baseRef)
		manifest := buildDiffManifest(taskDiff, stagedPaths, baseRef)
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
	Hash        string `json:"hash"`
	Type        string `json:"type"` // "tracked" or "untracked"
	Added       int    `json:"added,omitempty"`
	Removed     int    `json:"removed,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	OrigAdded   int    `json:"origAdded,omitempty"`
	OrigRemoved int    `json:"origRemoved,omitempty"`
	LineCount   int    `json:"line_count,omitempty"` // for untracked files
	FileSize    int64  `json:"file_size,omitempty"`  // for binary/large files
	IsBinary    bool   `json:"is_binary,omitempty"`  // true for binary files
	IsNew       bool   `json:"isNew,omitempty"`      // true when file is newly created
	IsDeleted   bool   `json:"isDeleted,omitempty"`  // true when file is deleted
	Staged      bool   `json:"staged,omitempty"`     // true if file is staged in the index
}

// diffManifest is the lightweight manifest returned by GET /diff.
// Contains per-file metadata (hashes, stats) without the actual diff content.
type diffManifest struct {
	Files     map[string]diffManifestFile `json:"files"`
	ShortStat string                      `json:"short_stat"`
	BaseRef   string                      `json:"base_ref,omitempty"`
}

// getStagedPaths returns a set of file paths that are staged in the index.
// Always compares against HEAD (current commit), NOT baseRef.
// baseRef may be a different branch and comparing index vs that branch
// would show ALL branch divergence as "staged".
func getStagedPaths(wm *git.WorktreeManager, taskID, baseRef string) map[string]struct{} {
	if wm == nil {
		return nil
	}
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return nil
	}
	cmd := exec.Command("git", "-C", worktreePath, "diff", "--staged", "--name-only", "HEAD")
	out, err := cmd.CombinedOutput()
	// git diff exits 0 = no changes, 1 = changes found (output is valid).
	// Only treat non-1 exit codes as real errors.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Changes exist, output is valid
		} else {
			return nil
		}
	}
	paths := make(map[string]struct{})
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		p := strings.TrimSpace(line)
		if p != "" {
			paths[p] = struct{}{}
		}
	}
	return paths
}

// buildDiffManifest creates a manifest from a TaskDiff with per-file hashes.
func buildDiffManifest(d *git.TaskDiff, stagedPaths map[string]struct{}, baseRef string) diffManifest {
	manifest := diffManifest{
		Files:     make(map[string]diffManifestFile),
		ShortStat: d.ShortStat,
		BaseRef:   baseRef,
	}
	for path, f := range d.Tracked {
		if git.IsInternalFile(path) {
			continue
		}
		_, ok := stagedPaths[path]
		staged := stagedPaths != nil && ok
		entry := diffManifestFile{
			Hash:        git.ComputeFileHash(f),
			Type:        "tracked",
			Added:       f.Added,
			Removed:     f.Removed,
			Truncated:   f.Truncated,
			OrigAdded:   f.OrigAdded,
			OrigRemoved: f.OrigRemoved,
			IsNew:       f.IsNew,
			IsDeleted:   f.IsDeleted,
			Staged:      staged,
		}
		if f.FileSize != nil {
			entry.FileSize = *f.FileSize
		}
		entry.IsBinary = f.IsBinary
		manifest.Files[path] = entry
	}
	for _, u := range d.Untracked {
		if git.IsInternalFile(u.Path) {
			continue
		}
		// Skip untracked files already in the tracked section
		// (e.g., new files appear as additions in git diff AND as untracked).
		if _, exists := manifest.Files[u.Path]; exists {
			continue
		}
		lineCount := len(u.Lines)
		if u.OrigAdded > 0 {
			lineCount = u.OrigAdded
		}
		manifest.Files[u.Path] = diffManifestFile{
			Hash:      git.ComputeUntrackedHash(u),
			Type:      "untracked",
			LineCount: lineCount,
			FileSize:  u.FileSize,
			Added:     lineCount,
			Truncated: u.Truncated,
			OrigAdded: u.OrigAdded,
			IsBinary:  u.IsBinary,
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
			task, err := h.store.GetTaskByID(taskID)
			if err == nil && h.worktreeMgr != nil {
				baseRef, _ := h.worktreeMgr.ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, "")
				if fullDiff, err := h.worktreeMgr.GetTaskDiff(taskID, baseRef, "", true); err == nil {
					if ff, ok := fullDiff.Tracked[filePath]; ok {
						f = ff
					}
				}
			}
		}
		resp := map[string]any{
			"rows":      f.Rows,
			"truncated": f.Truncated,
		}
		if len(f.CommittedRows) > 0 {
			resp["committedRows"] = f.CommittedRows
		}
		if f.OrigAdded > 0 {
			resp["origAdded"] = f.OrigAdded
		}
		if f.OrigRemoved > 0 {
			resp["origRemoved"] = f.OrigRemoved
		}
		if f.IsBinary {
			resp["isBinary"] = true
		}
		if f.FileSize != nil {
			resp["fileSize"] = *f.FileSize
		}
		httputil.JSONResponse(w, http.StatusOK, resp)
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
			resp := map[string]any{
				"lines":      lines,
				"line_count": len(lines),
				"truncated":  false,
				"origAdded":  len(lines),
			}
			if u.IsBinary {
				resp["isBinary"] = true
			}
			if u.FileSize != 0 {
				resp["fileSize"] = u.FileSize
			}
			httputil.JSONResponse(w, http.StatusOK, resp)
			return
		}
	}
	httputil.JSONError(w, http.StatusNotFound, "file not found in diff")
}

// getTaskDiffFromProject runs git diff from the project repo using the task's branch.
// Uses BranchTip (captured at commit-and-done time) if available, otherwise falls back to Branch.
// Uses BaseCommit (captured at task start time) as the base for done tasks,
// since the base ref branch pointer may have moved forward after a merge.
func (h *GitHandler) getTaskDiffFromProject(projectID, taskID string, task *types.Task, baseRef, headRef string) (*git.TaskDiff, error) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}

	// For done tasks, prefer the base ref commit captured at task start time.
	// After a fast-forward merge, the base ref branch pointer has moved forward,
	// so using the branch name would give an empty diff.
	if task.Status == "done" && task.BaseCommit != "" {
		baseRef = task.BaseCommit
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
	// Acquire per-task mutex to serialize git operations on this worktree.
	if h.getWorktreeMutex != nil {
		mu := h.getWorktreeMutex(taskID)
		mu.Lock()
		defer mu.Unlock()
	}
	// Reset worktree to the checkpoint (commit hash or stash hash)
	if err := h.worktreeMgr.ResetWorktreeTo(taskID, req.CommitHash); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("reset failed: %v", err))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "reset"})
}

// HandleStageFile stages or unstages a single file in the task's worktree.
// POST /api/projects/{projectID}/tasks/{taskID}/diff/stage
// Body: {"path": "file.c", "stage": true}
func (h *GitHandler) HandleStageFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		httputil.JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Path  string `json:"path"`
		Stage bool   `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path required")
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
	if task.Status == "backlog" || task.Status == "done" {
		httputil.JSONError(w, http.StatusBadRequest, "task must be in_progress or review")
		return
	}
	// Acquire per-task mutex to serialize git operations on this worktree.
	if h.getWorktreeMutex != nil {
		mu := h.getWorktreeMutex(taskID)
		mu.Lock()
		defer mu.Unlock()
	}
	worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "worktree not found")
		return
	}
	fullPath := filepath.Join(worktreePath, req.Path)

	if req.Stage {
		// Stage the file: git add -- <path>
		// Use -- to separate options from the path argument, preventing
		// paths starting with '-' (e.g. "-f") from being interpreted as flags.
		// For deleted files, git add records the deletion.
		cmd := exec.Command("git", "-C", worktreePath, "add", "--", req.Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("git add failed: %v: %s", err, out)
			httputil.JSONError(w, http.StatusInternalServerError, "git add failed")
			return
		}
	} else {
		// Unstage the file: git reset HEAD <path>
		// This restores the working tree to match the index (restores deleted content too).
		cmd := exec.Command("git", "-C", worktreePath, "reset", "HEAD", "--", req.Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("git reset failed: %v: %s", err, out)
			httputil.JSONError(w, http.StatusInternalServerError, "git reset failed")
			return
		}
		// If the file was newly added (not previously tracked), remove it from the index.
		// git reset HEAD on a new file doesn't delete it from disk, which is correct.
		// But we also need to handle the case where the file was deleted and staged.
		// git checkout -- <path> restores the working tree from the index.
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			// File was deleted — restore from index (HEAD)
			cmd = exec.Command("git", "-C", worktreePath, "checkout", "HEAD", "--", req.Path)
			if err := cmd.Run(); err != nil {
				// File might not be in HEAD (new file that was staged then deleted).
				// This is expected — just skip silently.
				_ = err
			}
		}
	}

	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "action": map[bool]string{true: "staged", false: "unstaged"}[req.Stage]})
}

// HandleProjectStageFile stages or unstages a single file in the project repo.
// POST /api/projects/{projectID}/git/stage
// Body: {"path": "file.c", "stage": true}
func (h *GitHandler) HandleProjectStageFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	var req struct {
		Path  string `json:"path"`
		Stage bool   `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path required")
		return
	}
	// Acquire per-project mutex to serialize index-modifying operations.
	if h.getProjectMutex != nil {
		mu := h.getProjectMutex(projectID)
		mu.Lock()
		defer mu.Unlock()
	}
	if req.Stage {
		cmd := exec.Command("git", "-C", project.Path, "add", "--", req.Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("git add failed: %v: %s", err, out)
			httputil.JSONError(w, http.StatusInternalServerError, "git add failed")
			return
		}
	} else {
		cmd := exec.Command("git", "-C", project.Path, "reset", "HEAD", "--", req.Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("git reset failed: %v: %s", err, out)
			httputil.JSONError(w, http.StatusInternalServerError, "git reset failed")
			return
		}
		// Restore deleted file from HEAD if it no longer exists on disk
		fullPath := filepath.Join(project.Path, req.Path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			exec.Command("git", "-C", project.Path, "checkout", "HEAD", "--", req.Path).CombinedOutput()
		}
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "action": map[bool]string{true: "staged", false: "unstaged"}[req.Stage]})
}

// HandleProjectCommit commits staged changes in the project repo.
// POST /api/projects/{projectID}/git/commit
// Body: {"message": "commit message"}
func (h *GitHandler) HandleProjectCommit(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		httputil.JSONError(w, http.StatusBadRequest, "message required")
		return
	}
	// Acquire per-project mutex to serialize project-level git operations.
	if h.getProjectMutex != nil {
		mu := h.getProjectMutex(projectID)
		mu.Lock()
		defer mu.Unlock()
	}
	// Check if there are staged changes
	cmd := exec.Command("git", "-C", project.Path, "diff", "--cached", "--quiet")
	if err := cmd.Run(); err == nil {
		// Exit code 0 means no staged changes
		httputil.JSONError(w, http.StatusBadRequest, "no staged changes to commit")
		return
	}
	cmd = exec.Command("git", "-C", project.Path, "commit", "-m", req.Message)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("git commit failed: %v: %s", err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git commit failed")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "committed"})
}

// HandleTaskLog returns commit log for a task's worktree branch.
func (h *GitHandler) HandleTaskLog(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}

	// Get worktree path
	worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task worktree not found")
		return
	}

	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = task.Branch
	}
	if branch != "" {
		if err := types.ValidateBranchName(branch); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if branch == "" {
		branch = "HEAD"
	}

	full := r.URL.Query().Get("full") == "1"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if full {
		args := []string{"-C", worktreePath, "log"}
		if offset > 0 {
			args = append(args, "--skip", strconv.Itoa(offset))
		}
		if limit > 0 {
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
			if len(blockLines) > 1 {
				rest := blockLines[1]
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
		currentBranch := h.getCurrentBranch(worktreePath)

		// Build branch-to-commit mapping from local refs
		branchMap := h.getBranchMap(worktreePath)

		for i := range commits {
			if branches, ok := branchMap[commits[i].Hash]; ok {
				commits[i].Branches = branches
			}
		}

		// Get origin tip
		originTip := ""
		if currentBranch != "" && currentBranch != "HEAD" {
			originTip = h.getOriginTip(worktreePath, currentBranch)
		}

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
		httputil.JSONResponse(w, http.StatusOK, resp)
		return
	}

	// Simple log (non-full)
	args := []string{"-C", worktreePath, "log"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	args = append(args, "--format=%H|%h|%s", branch)
	c := exec.Command("git", args...)
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
		return
	}
	var commits []types.GitCommit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		commits = append(commits, types.GitCommit{Hash: parts[0], Short: parts[1], Subject: parts[2]})
	}
	httputil.JSONResponse(w, http.StatusOK, commits)
}

// HandleTaskBranches lists branches for a task's worktree.
func (h *GitHandler) HandleTaskBranches(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	_, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}

	worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task worktree not found")
		return
	}

	c := exec.Command("git", "-C", worktreePath, "branch", "--format=%(refname:short)")
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git branch failed: %v", err))
		return
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	httputil.JSONResponse(w, http.StatusOK, branches)
}

// HandleTaskCheckout checks out a branch in a task's worktree.
func (h *GitHandler) HandleTaskCheckout(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	_, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}

	worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task worktree not found")
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Branch == "" {
		httputil.JSONError(w, http.StatusBadRequest, "branch is required")
		return
	}
	if err := types.ValidateBranchName(req.Branch); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	c := exec.Command("git", "-C", worktreePath, "checkout", req.Branch)
	out, err := c.CombinedOutput()
	if err != nil {
		log.Printf("git checkout failed: %v: %s", err, out)
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git checkout failed: %s", out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "checked out", "branch": req.Branch})
}
