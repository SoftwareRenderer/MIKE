package managers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"kanban/backend/fsutil"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/toolresult"
	"kanban/backend/types"
	"kanban/pkg/logging"
)

// GitHandler handles git operations.
type GitHandler struct {
	store       *store.Store
	worktreeMgr *git.WorktreeManager
	diffCache   sync.Map // taskID -> *git.TaskDiff (full result including untracked file content)
	// getWorktreeMutex locks the per-task worktree and returns the function that releases it.
	// Set by the server. A release function is handed back rather than a *sync.Mutex because a
	// mutex that crosses a package boundary can be stored and copied, and two holders of copies
	// serialize nothing.
	getWorktreeMutex func(taskID string) func()
	// getProjectMutex locks the per-project git operations (checkout, pull, push) that touch shared
	// state, and returns the function that releases it.
	getProjectMutex func(projectID string) func()
}

func NewGitHandler(store *store.Store, worktreeMgr *git.WorktreeManager) *GitHandler {
	return &GitHandler{store: store, worktreeMgr: worktreeMgr}
}

// SetWorktreeMutex sets the callback for acquiring per-task worktree mutexes.
// Called by the server to wire up the shared mutex map.
func (h *GitHandler) SetWorktreeMutex(fn func(taskID string) func()) {
	h.getWorktreeMutex = fn
}

// SetProjectMutex sets the callback for acquiring per-project mutexes.
// Called by the server to wire up the shared project mutex map.
func (h *GitHandler) SetProjectMutex(fn func(projectID string) func()) {
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

// fullLogFormat is the git log pretty-format writeFullGitLog parses: one header
// line of pipe-separated fields, then the body between the BODY/END markers.
const fullLogFormat = `--format=%H|%h|%s|%aN|%aI|%P%n---BODY---%n%b%n---END---`

// writeFullGitLog writes the ?full=1 commit log for dir: every commit with author, date,
// parents and body, plus the current branch, the origin tip and a hasMore flag. Both log
// endpoints answer ?full=1 with exactly this; their non-full responses deliberately differ.
// Callers validate branch and path first.
func (h *GitHandler) writeFullGitLog(w http.ResponseWriter, dir, branch, path string, limit, offset int) {
	args := []string{"-C", dir, "log"}
	if offset > 0 {
		args = append(args, "--skip", strconv.Itoa(offset))
	}
	if limit > 0 {
		// Fetch one extra commit to detect whether more exist.
		args = append(args, "-n", strconv.Itoa(limit+1))
	}
	// --end-of-options: the branch is a client value.
	args = append(args, fullLogFormat, "--end-of-options", branch)
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := git.Command("", args...).CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git log failed: %v", err))
		return
	}

	var commits []types.GitCommit
	for _, block := range strings.Split(string(out), "---END---") {
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
		// The body is everything after the "---BODY---" marker line.
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

	currentBranch := h.getCurrentBranch(dir)

	// Attach local branch names to the commits they point at.
	branchMap := h.getBranchMap(dir)
	for i := range commits {
		if branches, ok := branchMap[commits[i].Hash]; ok {
			commits[i].Branches = branches
		}
	}

	// originTip is where origin/<current-branch> points.
	originTip := ""
	if currentBranch != "" && currentBranch != "HEAD" {
		originTip = h.getOriginTip(dir, currentBranch)
	}

	// Drop the extra commit fetched only to detect hasMore.
	hasMore := false
	if limit > 0 && len(commits) > limit {
		hasMore = true
		commits = commits[:limit]
	}

	httputil.JSONResponse(w, http.StatusOK, types.GitLogResponse{
		Commits:       commits,
		CurrentBranch: currentBranch,
		OriginTip:     originTip,
		HasMore:       hasMore,
	})
}

// HandleLog returns commit log for a project.
// With ?full=1 query param, returns extended fields: hash, author, date, parents, branches.
// Also includes current_branch and origin_tip metadata.
func (h *GitHandler) HandleLog(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
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
		h.writeFullGitLog(w, project.Path, branch, path, limit, offset)
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
	c := git.Command("", args...)
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
	httputil.JSONResponse(w, http.StatusOK, commits)
}

// getCurrentBranch returns the name of the currently checked-out branch.
func (h *GitHandler) getCurrentBranch(path string) string {
	c := git.Command("", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// getBranchMap returns a map of commit hash -> branch names for all local branches.
func (h *GitHandler) getBranchMap(path string) map[string][]string {
	c := git.Command("", "-C", path, "for-each-ref", "--format=%(objectname)|%(refname:short)", "refs/heads/")
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
	c := git.Command("", "-C", path, "rev-parse", "origin/"+branch)
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitBranchNames runs a git branch listing in dir and returns one entry per
// non-empty output line (git pads refname:short output, hence the trim). Both
// branch endpoints share this; they differ only in the subcommand — a project
// lists remote refs too (-a), a task worktree only its local branches.
func gitBranchNames(dir string, args ...string) ([]string, error) {
	out, err := git.Command("", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// HandleBranches lists all branches of a project, local and remote.
func (h *GitHandler) HandleBranches(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	branches, err := gitBranchNames(project.Path, "branch", "-a", "--format=%(refname:short)")
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git branches failed: %v", err))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, branches)
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
		httputil.Fail(w, err, "get project", "project not found")
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
		c = git.Command("", "-C", project.Path, "diff", "--cached")
	} else if base != "" && head != "" {
		// Commit-to-commit comparison
		// Special case: empty tree hash is a virtual constant, not a real git object.
		// git diff EMPTY_TREE..HEAD fails with "Invalid revision range".
		// Use git diff-tree --root instead to show the root commit's changes.
		if base == git.EmptyTreeHash {
			c = git.Command("", "-C", project.Path, "diff-tree", "--root", "--format=", "-p", head)
		} else {
			c = git.Command("", "-C", project.Path, "diff", base+".."+head)
		}
	} else {
		// Unstaged changes: working tree vs index (git diff with no args)
		// This excludes staged changes, which are shown separately via cached=1
		c = git.Command("", "-C", project.Path, "diff")
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
	// ?parsed=1 returns the parser's output instead of the patch text, so the wire carries the
	// one grammar Go already parses — a second one in the browser drifted from it. Raw stays the
	// default for ?cached=1/base/head consumers and external callers.
	if r.URL.Query().Get("parsed") == "1" {
		files := git.ParseUnifiedDiff(string(out))
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(files); err != nil {
			logging.For(logging.Git).Infof("encoding parsed diff for project %s: %v", projectID, err)
		}
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(out)
}

// HandleStatus returns working tree status, current HEAD hash, and git author identity.
func (h *GitHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	c := git.Command("", "-C", project.Path, "status", "--porcelain")
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git status failed: %v", err))
		return
	}
	clean := len(strings.TrimSpace(string(out))) == 0
	// Fetch HEAD hash for staleness detection
	var headHash string
	if headOut, err := git.Command("", "-C", project.Path, "rev-parse", "HEAD").CombinedOutput(); err == nil {
		headHash = strings.TrimSpace(string(headOut))
	}
	// Fetch git author identity for commit dialog
	var authorName, authorEmail string
	if nameOut, err := git.Command("", "-C", project.Path, "config", "user.name").CombinedOutput(); err == nil {
		authorName = strings.TrimSpace(string(nameOut))
	}
	if emailOut, err := git.Command("", "-C", project.Path, "config", "user.email").CombinedOutput(); err == nil {
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

// HandleProjectDiffFile returns the diff for one file in the project, plus its full content
// (working tree or index) so the frontend can render it. Query params: path (relative to the
// project root), staged (true = index diff, omitted = working tree), and meta=true for
// metadata only (line_count, file_size, is_binary) — the cheap way to list untracked files.
func (h *GitHandler) HandleProjectDiffFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.MethodNotAllowed(w)
		return
	}
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	// The diff below is contained by git (a pathspec outside the repository is an
	// error), but the working-tree read is ours, so jail it here and open only what the
	// jail returned.
	worktreeFile, err := repoFilePath(project.Path, filePath)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	staged := r.URL.Query().Get("staged") == "true"
	metaOnly := r.URL.Query().Get("meta") == "1"

	// Get the diff for this file
	var c *exec.Cmd
	if staged {
		c = git.Command("", "-C", project.Path, "diff", "--cached", "--", filePath)
	} else {
		c = git.Command("", "-C", project.Path, "diff", "--", filePath)
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

	// Stat before reading: this is a file the request named.
	var fileSize int64
	if st, err := os.Stat(worktreeFile); err == nil {
		fileSize = st.Size()
	}

	// Get file content from working tree or index
	var fileContent string
	var lineCount int
	var isBinary bool
	if staged {
		// Read the staged version, bounded at capture.
		c = git.Command("", "-C", project.Path, "show", ":"+filePath)
		contentOut := toolresult.NewCapture()
		c.Stdout = contentOut
		c.Stderr = contentOut
		if contentErr := c.Run(); contentErr == nil {
			fileContent = contentOut.String()
			lineCount = strings.Count(fileContent, "\n")
			if fileContent != "" && fileContent[len(fileContent)-1] != '\n' {
				lineCount++
			}
		}
	} else if fileSize <= maxDiffFileBytes {
		// Read from working tree
		contentOut, contentErr := os.ReadFile(worktreeFile)
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

	// Only report file_size when the file is binary or too large to render inline
	reportFileSize := isBinary || (!isBinary && fileSize > maxDiffFileBytes)

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

// maxDiffFileBytes is the largest working-tree file a diff read here pulls into memory. The
// stat is already taken, so an oversized read is refused rather than tried.
const maxDiffFileBytes int64 = 1_000_000

// repoFilePath jails a repository-relative path to the checkout in dir and returns the host
// file it names, symlink-resolved. An absolute path or one that walks out of the repository is
// a forged request, not a filename. The symlink check is the point: the working-tree read
// follows links, so a committed `key.pem -> ../../.ssh/id_rsa` would read any file the server
// user can open.
func repoFilePath(dir, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the repository", rel)
	}
	return fsutil.Contained(filepath.Join(dir, rel), []string{filepath.Clean(dir)})
}

// HandleCheckout checks out a branch in the project's own checkout.
func (h *GitHandler) HandleCheckout(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	branch, ok := checkoutBranchFrom(w, r)
	if !ok {
		return
	}
	// Acquire per-project mutex to serialize project-level git operations.
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	if !runCheckout(w, project.Path, branch) {
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": branch})
}

// checkoutBranchFrom reads and validates the branch out of a checkout request body.
// Both checkout endpoints take the same body, and the branch is a `git checkout`
// argument, so the validation lives with the shared runner rather than being copied
// per endpoint. On refusal the response is written and false is returned.
func checkoutBranchFrom(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req types.GitCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "branch is required")
		return "", false
	}
	if err := types.ValidateBranchName(req.Branch); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return req.Branch, true
}

// runCheckout switches the checkout in dir to branch. A refusal says so with git's
// own reason, capped: "your local changes would be overwritten by checkout" is the
// answer the user needs, while git's full output is pages of paths on the server's
// filesystem. The unsanitised output always goes to the log.
func runCheckout(w http.ResponseWriter, dir, branch string) bool {
	out, err := git.Command("", "-C", dir, "checkout", branch).CombinedOutput()
	if err != nil {
		logging.For(logging.Managers).Errorf("git checkout failed in %s: %v: %s", dir, err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git checkout failed: "+gitErrorSummary(out))
		return false
	}
	return true
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

// handleGitRemote runs a project-level remote git command (pull/push),
// serialized by the per-project mutex. op must be "pull" or "push".
func (h *GitHandler) handleGitRemote(w http.ResponseWriter, r *http.Request, op string) {
	if r.Method != http.MethodPost {
		httputil.MethodNotAllowed(w)
		return
	}
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	// Acquire per-project mutex to serialize project-level git operations.
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	// Bounded to the request: an unreachable remote used to hold the per-project mutex — and a
	// server-side git process — for the full ssh/TCP timeout, queueing every other git operation
	// on that project behind it.
	c := git.CommandContext(r.Context(), "", "-C", project.Path, op)
	// No c.Env on purpose: a bare extra list replaces the environment git.Command sets, which is
	// where the prompt refusal and the hook fence come from. The fence means a pre-push hook does
	// not run when the user clicks Push.
	if out, err := c.CombinedOutput(); err != nil {
		logging.For(logging.Managers).Errorf("git %s failed: %v: %s", op, err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git "+op+" failed: "+gitErrorSummary(out))
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": op + "ed"})
}

// HandlePull pulls from remote.
func (h *GitHandler) HandlePull(w http.ResponseWriter, r *http.Request) {
	h.handleGitRemote(w, r, "pull")
}

// HandlePush pushes to remote.
func (h *GitHandler) HandlePush(w http.ResponseWriter, r *http.Request) {
	h.handleGitRemote(w, r, "push")
}

// HandleTaskCommits returns the commit log for a task's worktree branch.
// Only commits after the divergence from the original branch are returned.
// Also returns the base commit info for display in the diff selector.
// Falls back to project repo when worktree doesn't exist (e.g., after commit-and-done).
func (h *GitHandler) HandleTaskCommits(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	if !requireWorktreeMgr(w, h.worktreeMgr) {
		return
	}
	if task.Status == types.StatusBacklog {
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
		if task.Branch != "" && errors.Is(err, git.ErrWorktreeNotFound) {
			result, err = h.getTaskCommitsFromProject(projectID, task, baseRef)
		}
		// Orphaned worktree — return 404
		if code, ok := git.GitExitCode(err); ok && code == 128 {
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

// getTaskCommitsFromProject answers the commit list from the project checkout for a task
// whose worktree is gone (after commit-and-done). It is the same log the worktree answers
// with — same refs, same parser — so switching between the two cannot show a different
// history. Head comes from BranchTip when captured at commit time, else Branch.
func (h *GitHandler) getTaskCommitsFromProject(projectID string, task *types.Task, baseRef string) (*git.TaskCommitsResult, error) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}

	// Use branch tip captured at commit time, or fall back to branch name
	target := task.BranchTip
	if target == "" {
		target = task.Branch
	}

	result, err := git.TaskCommitsIn(project.Path, baseRef, target)
	if err != nil {
		// Branch may not exist (orphaned worktree): answer with no commits.
		logging.For(logging.Git).With(logging.TaskID, task.ID).Infof("branch %s not found in project, returning empty commits: %v", target, err)
		return &git.TaskCommitsResult{Commits: []git.TaskCommit{}}, nil
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
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	if !requireWorktreeMgr(w, h.worktreeMgr) {
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
	// checkpointIdx is only read as a flag below: a checkpoint diff must never come from the cached
	// done-task snapshot.

	// The canonical done-task diff (pinned BaseCommit..BranchTip) is immutable —
	// serve it from the persistent snapshot instead of re-running git on every
	// open. Arbitrary ref comparisons (custom base/head), checkpoint diffs, and
	// full (untruncated) requests still go through the live computation.
	if task.Status == types.StatusDone && task.BranchTip != "" && customBase == "" && checkpointIdxStr == "" && !fullDiff &&
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
	if task.Status == types.StatusDone && headRef == "" && customBase == "" {
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

	// headRef is the checkpoint commit, whose tree holds the whole working state at that point, so
	// baseRef..headRef is "the changes up to and including this checkpoint". A checkpoint that resolves to
	// the baseline has no changes of its own, and the honest diff for that is empty.

	taskDiff, err := h.worktreeMgr.GetTaskDiff(taskID, baseRef, headRef, fullDiff)
	if err != nil {
		// Worktree may not exist (e.g., after commit-and-done).
		// Fall back to project repo if task has a branch.
		if task.Branch != "" && errors.Is(err, git.ErrWorktreeNotFound) {
			taskDiff, err = h.getTaskDiffFromProject(projectID, taskID, task, baseRef, headRef)
		}
		// Orphaned worktree — return 404
		if code, ok := git.GitExitCode(err); ok && code == 128 {
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
	// ?parsed=1 attaches the per-file structure to the ref-comparison response. Without it a
	// headRef diff ships as patch TEXT and the browser re-parses it with a second grammar; Go has
	// already parsed this diff to compute hashes and truncation, so the work is done. The manifest
	// branch above already returns per-file data and needs nothing.
	if r.URL.Query().Get("parsed") == "1" {
		taskDiff.ParsedFiles = sortedTrackedFiles(taskDiff.Tracked)
	}
	httputil.JSONResponse(w, http.StatusOK, taskDiff)
}

// sortedTrackedFiles flattens the parsed map into path order, so a response never inherits Go's map
// iteration order.
func sortedTrackedFiles(tracked map[string]*git.TrackedDiffFile) []git.TrackedDiffFile {
	out := make([]git.TrackedDiffFile, 0, len(tracked))
	for _, f := range tracked {
		if f != nil {
			out = append(out, *f)
		}
	}
	slices.SortFunc(out, func(a, b git.TrackedDiffFile) int { return strings.Compare(a.Path, b.Path) })
	return out
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
	cmd := git.Command("", "-C", worktreePath, "diff", "--staged", "--name-only", "HEAD")
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
		httputil.MethodNotAllowed(w)
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
	for _, u := range taskDiff.Untracked {
		if u.Path == filePath {
			lines := u.Lines
			truncated := u.Truncated
			// A truncated entry is re-read from the worktree when the caller asked for the file.
			if fullFile && u.Truncated && h.worktreeMgr != nil {
				worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
				if err == nil {
					// The cached path came from git status, which lists a symlink the way it lists
					// a file, so the re-read needs the listing's jail and the size cap: past the cap
					// the cached lines are the answer and truncated says so.
					target, size, err := git.UntrackedReadTarget(worktreePath, filePath)
					if err != nil {
						httputil.JSONError(w, http.StatusForbidden, err.Error())
						return
					}
					if size <= maxDiffFileBytes {
						if data, readErr := os.ReadFile(target); readErr == nil {
							lines = git.SplitLines(string(data))
							truncated = false
						}
					}
				}
			}
			resp := map[string]any{
				"lines":      lines,
				"line_count": len(lines),
				"truncated":  truncated,
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
	if task.Status == types.StatusDone && task.BaseCommit != "" {
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

	c := git.Command("", "-C", project.Path, "diff", refSpec)
	out, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff from project: %s: %w", string(out), err)
	}

	shortStatOut, _ := git.Command("", "-C", project.Path, "diff", "--shortstat", refSpec).CombinedOutput()

	return &git.TaskDiff{
		Diff:      string(out),
		Untracked: []git.UntrackedFile{},
		ShortStat: string(shortStatOut),
	}, nil
}

// stageOrUnstage applies one file's staged state in dir and returns the action name for the
// response body. Every path goes to git after `--`, so a path like "-f" is never parsed as an
// option. On failure the error is written to w and false is returned, so callers just return.
func (h *GitHandler) stageOrUnstage(w http.ResponseWriter, dir, path string, stage bool) bool {
	if stage {
		// git add records a deletion too, so a staged delete survives the round trip.
		out, err := git.Command("", "-C", dir, "add", "--", path).CombinedOutput()
		if err != nil {
			logging.For(logging.Managers).Errorf("git add failed: %v: %s", err, out)
			httputil.JSONError(w, http.StatusInternalServerError, "git add failed")
			return false
		}
		return true
	}

	// git reset HEAD -- <path> makes the index match HEAD again.
	out, err := git.Command("", "-C", dir, "reset", "HEAD", "--", path).CombinedOutput()
	if err != nil {
		logging.For(logging.Managers).Errorf("git reset failed: %v: %s", err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git reset failed")
		return false
	}
	// Content is gone from disk but was tracked: bring it back from HEAD. A file
	// that was staged and then deleted exists in no commit, so git fails here —
	// that is the expected outcome and the error is deliberately ignored.
	if _, statErr := os.Stat(filepath.Join(dir, path)); os.IsNotExist(statErr) {
		_, _ = git.Command("", "-C", dir, "checkout", "HEAD", "--", path).CombinedOutput()
	}
	return true
}

// stageActionName is the "action" field both stage endpoints report.
func stageActionName(stage bool) string {
	if stage {
		return "staged"
	}
	return "unstaged"
}

// HandleStageFile stages or unstages a single file in the task's worktree.
// POST /api/projects/{projectID}/tasks/{taskID}/diff/stage
// Body: {"path": "file.c", "stage": true}
func (h *GitHandler) HandleStageFile(w http.ResponseWriter, r *http.Request) {
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
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	if !requireWorktreeMgr(w, h.worktreeMgr) {
		return
	}
	if task.Status == types.StatusBacklog || task.Status == types.StatusDone {
		httputil.JSONError(w, http.StatusBadRequest, "task must be in_progress or review")
		return
	}
	// Acquire per-task mutex to serialize git operations on this worktree.
	if h.getWorktreeMutex != nil {
		defer h.getWorktreeMutex(taskID)()
	}
	worktreePath, ok := resolveWorktree(w, r, h.worktreeMgr)
	if !ok {
		return
	}
	if !h.stageOrUnstage(w, worktreePath, req.Path, req.Stage) {
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "action": stageActionName(req.Stage)})
}

// HandleProjectStageFile stages or unstages a single file in the project repo.
// POST /api/projects/{projectID}/git/stage
// Body: {"path": "file.c", "stage": true}
func (h *GitHandler) HandleProjectStageFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
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
		defer h.getProjectMutex(projectID)()
	}
	if !h.stageOrUnstage(w, project.Path, req.Path, req.Stage) {
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "action": stageActionName(req.Stage)})
}

// HandleProjectCommit commits staged changes in the project repo.
// POST /api/projects/{projectID}/git/commit
// Body: {"message": "commit message"}
func (h *GitHandler) HandleProjectCommit(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
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
		defer h.getProjectMutex(projectID)()
	}
	// Check if there are staged changes
	cmd := git.Command("", "-C", project.Path, "diff", "--cached", "--quiet")
	if err := cmd.Run(); err == nil {
		// Exit code 0 means no staged changes
		httputil.JSONError(w, http.StatusBadRequest, "no staged changes to commit")
		return
	}
	cmd = git.Command("", "-C", project.Path, "commit", "-m", req.Message)
	if out, err := cmd.CombinedOutput(); err != nil {
		logging.For(logging.Managers).Errorf("git commit failed: %v: %s", err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git commit failed")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "committed"})
}

// HandleTaskLog returns commit log for a task's worktree branch.
func (h *GitHandler) HandleTaskLog(w http.ResponseWriter, r *http.Request) {
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}

	// Get worktree path
	worktreePath, ok := resolveWorktree(w, r, h.worktreeMgr)
	if !ok {
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
		h.writeFullGitLog(w, worktreePath, branch, "", limit, offset)
		return
	}

	// Simple log (non-full)
	args := []string{"-C", worktreePath, "log"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	args = append(args, "--format=%H|%h|%s", branch)
	c := git.Command("", args...)
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
