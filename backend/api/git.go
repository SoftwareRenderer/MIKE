package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"
	"mike/pkg/logging"
)

// GitHandler handles git operations.
type GitHandler struct {
	store       *store.Store
	worktreeMgr *git.WorktreeManager
	diffCache   taskDiffCache
	// getWorktreeMutex locks the per-task worktree and returns the function that releases it; the server
	// sets it. A release function crosses the boundary rather than a *sync.Mutex, which a holder could
	// copy and then serialize nothing.
	getWorktreeMutex func(taskID string) func()
	// getProjectMutex locks the project-level git operations that touch shared state.
	getProjectMutex func(projectID string) func()
}

// taskDiffCache holds, per task, its worktree diff against HEAD at the baseline
// git.WorktreeManager.TaskDiffBaseline names. Nothing else may be stored here: GET /tasks/{id}/diff/file
// names a path and no refs, so it can only answer for that one comparison.
type taskDiffCache struct {
	byTask sync.Map // taskID -> *git.TaskDiff
}

func (c *taskDiffCache) put(taskID string, taskDiff *git.TaskDiff) {
	c.byTask.Store(taskID, taskDiff)
}

func (c *taskDiffCache) get(taskID string) (*git.TaskDiff, bool) {
	raw, ok := c.byTask.Load(taskID)
	if !ok {
		return nil, false
	}
	taskDiff, _ := raw.(*git.TaskDiff)
	return taskDiff, taskDiff != nil
}

func NewGitHandler(store *store.Store, worktreeMgr *git.WorktreeManager) *GitHandler {
	return &GitHandler{store: store, worktreeMgr: worktreeMgr}
}

// SetWorktreeMutex sets the callback that hands out per-task worktree locks.
func (h *GitHandler) SetWorktreeMutex(fn func(taskID string) func()) {
	h.getWorktreeMutex = fn
}

// SetProjectMutex sets the callback that hands out per-project git locks.
func (h *GitHandler) SetProjectMutex(fn func(projectID string) func()) {
	h.getProjectMutex = fn
}

// UpdateDiffCache records the worktree watcher's working-tree diff, so on-demand file fetches work without
// a full /diff call. See taskDiffCache for what may be stored here.
func (h *GitHandler) UpdateDiffCache(taskID string, taskDiff *git.TaskDiff) {
	h.diffCache.put(taskID, taskDiff)
}

// currentTaskDiff is this package's only way to compute the diff behind the Changes panel. It resolves the
// baseline itself, so a caller cannot answer a different question than the one the panel asked.
func (h *GitHandler) currentTaskDiff(task *types.Task, skipTruncate bool) (*git.TaskDiff, string, error) {
	if h.worktreeMgr == nil {
		return nil, "", fmt.Errorf("worktree manager not available")
	}
	baseRef := h.worktreeMgr.TaskDiffBaseline(task)
	taskDiff, err := h.worktreeMgr.GetTaskDiff(task.ID, baseRef, "", skipTruncate)
	if err != nil {
		return nil, baseRef, err
	}
	if taskDiff.Untracked == nil {
		taskDiff.Untracked = []git.UntrackedFile{}
	}
	return taskDiff, baseRef, nil
}

// GetDiffManifest builds a task worktree's diff manifest, the format broadcastTaskDiff sends over
// WebSocket, and records the diff it was built from for the per-file fetches that follow.
func (h *GitHandler) GetDiffManifest(task *types.Task) (any, error) {
	taskDiff, baseRef, err := h.currentTaskDiff(task, false)
	if err != nil {
		return nil, err
	}
	h.diffCache.put(task.ID, taskDiff)
	stagedPaths := getStagedPaths(h.worktreeMgr, task.ID)
	return buildDiffManifest(taskDiff, stagedPaths, baseRef), nil
}

// fullLogFormat is the git log pretty-format writeFullGitLog parses: one header
// line of pipe-separated fields, then the body between the BODY/END markers.
const fullLogFormat = `--format=%H|%h|%s|%aN|%aI|%P%n---BODY---%n%b%n---END---`

// writeFullGitLog writes the ?full=1 commit log for dir: every commit with author, date, parents and
// body, plus the current branch, the origin tip and a hasMore flag. Both log endpoints answer ?full=1
// with exactly this, and callers validate branch and path first.
func (h *GitHandler) writeFullGitLog(w http.ResponseWriter, dir, branch, path string, limit, offset int) {
	args := []string{"-C", dir, "log"}
	if offset > 0 {
		args = append(args, "--skip", strconv.Itoa(offset))
	}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit+1))
	}
	// --end-of-options: the branch is a client value.
	args = append(args, fullLogFormat, "--end-of-options", branch)
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := git.Command("", args...).CombinedOutput()
	if err != nil {
		httputil.Fault(w, "git log", err)
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

	branchMap := h.getBranchMap(dir)
	for i := range commits {
		if branches, ok := branchMap[commits[i].Hash]; ok {
			commits[i].Branches = branches
		}
	}

	originTip := ""
	if currentBranch != "" && currentBranch != "HEAD" {
		originTip = h.getOriginTip(dir, currentBranch)
	}

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

// HandleLog returns a project's commit log: hash, author, date, parents and branch refs per commit,
// beside current_branch and origin_tip.
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
		if filepath.IsAbs(path) || strings.Contains(path, "..") || strings.HasPrefix(path, "~") {
			httputil.JSONError(w, http.StatusBadRequest, "invalid path")
			return
		}
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	h.writeFullGitLog(w, project.Path, branch, path, limit, offset)
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

// gitBranchNames runs a branch listing in dir and returns one trimmed entry per output line. The two
// branch endpoints share it; a project passes -a for remote refs too.
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
		httputil.Fault(w, "git branches", err)
		return
	}
	httputil.JSONResponse(w, http.StatusOK, branches)
}

// HandleDiff returns a project's diff, always as the parser's output. Which diff is chosen by:
//   - ?base=A&head=B — git diff A..B (commit-to-commit comparison)
//   - ?cached=1      — git diff --cached (staged changes only)
//   - neither        — git diff (working tree vs index)
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
		c = git.Command("", "-C", project.Path, "diff", "--cached")
	} else if base != "" && head != "" {
		// The empty tree is a virtual constant, not a git object, so a range from it fails; diff-tree
		// --root shows the root commit instead.
		if base == git.EmptyTreeHash {
			c = git.Command("", "-C", project.Path, "diff-tree", "--root", "--format=", "-p", head)
		} else {
			c = git.Command("", "-C", project.Path, "diff", base+".."+head)
		}
	} else {
		// Working tree vs index; staged changes come back separately under cached=1.
		c = git.Command("", "-C", project.Path, "diff")
	}

	out, err := c.CombinedOutput()
	if err != nil {
		// git diff exits 1 when there are changes and the output is still good; every diff call here
		// reads that exit code the same way.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		} else {
			httputil.Fault(w, "git diff", err)
			return
		}
	}
	// The wire carries the one diff grammar in the product; the panel never parses patch text.
	files := git.ParseUnifiedDiff(string(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		logging.For(logging.Git).Infof("encoding parsed diff for project %s: %v", projectID, err)
	}
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
		httputil.Fault(w, "git status", err)
		return
	}
	clean := len(strings.TrimSpace(string(out))) == 0
	// HEAD is what the client compares its own copy against.
	var headHash string
	if headOut, err := git.Command("", "-C", project.Path, "rev-parse", "HEAD").CombinedOutput(); err == nil {
		headHash = strings.TrimSpace(string(headOut))
	}
	var authorName, authorEmail string
	if nameOut, err := git.Command("", "-C", project.Path, "config", "user.name").CombinedOutput(); err == nil {
		authorName = strings.TrimSpace(string(nameOut))
	}
	if emailOut, err := git.Command("", "-C", project.Path, "config", "user.email").CombinedOutput(); err == nil {
		authorEmail = strings.TrimSpace(string(emailOut))
	}
	ahead, behind := remoteAheadBehind(project.Path)
	httputil.JSONResponse(w, http.StatusOK, map[string]any{
		"clean":       clean,
		"status":      string(out),
		"head":        headHash,
		"authorName":  authorName,
		"authorEmail": authorEmail,
		"ahead":       ahead,
		"behind":      behind,
	})
}

// remoteAheadBehind reads refs/remotes/…, so the count moves only when something fetches and never
// contacts the remote; the caller marks it approximate. A missing upstream, a detached HEAD and an
// unborn branch all answer 0/0.
func remoteAheadBehind(dir string) (ahead, behind int) {
	c := git.Command("", "-C", dir, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	// stdout only, so a git warning on stderr cannot reach the parse.
	out, err := c.Output()
	if err != nil {
		return 0, 0
	}
	// Tab-separated, HEAD's side first.
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0
	}
	a, aerr := strconv.Atoi(fields[0])
	b, berr := strconv.Atoi(fields[1])
	if aerr != nil || berr != nil {
		return 0, 0
	}
	return a, b
}

// HandleProjectDiffFile returns the diff for one project file plus its working-tree content. Query
// params: path relative to the project root, and meta=true for metadata only — the cheap way to list
// untracked files.
func (h *GitHandler) HandleProjectDiffFile(w http.ResponseWriter, r *http.Request) {
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
	// git contains the diff itself, but the working-tree read is ours: jail it here and open only what
	// the jail returned.
	worktreeFile, err := repoFilePath(project.Path, filePath)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	metaOnly := r.URL.Query().Get("meta") == "1"

	c := git.Command("", "-C", project.Path, "diff", "--", filePath)
	out, err := c.CombinedOutput()
	if err != nil {
		// Exit 1 means changes exist: see HandleDiff.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		} else {
			httputil.Fault(w, "git diff", err)
			return
		}
	}

	// Stat before reading: this is a file the request named.
	var fileSize int64
	if st, err := os.Stat(worktreeFile); err == nil {
		fileSize = st.Size()
	}

	// The cap tested before the read is what bounds it.
	var fileContent string
	var lineCount int
	var isBinary bool
	if fileSize <= maxDiffFileBytes {
		contentOut, contentErr := os.ReadFile(worktreeFile)
		if contentErr == nil {
			fileContent = string(contentOut)
			lineCount = strings.Count(fileContent, "\n")
			if fileContent != "" && fileContent[len(fileContent)-1] != '\n' {
				lineCount++
			}
		}
	}

	if fileContent != "" {
		for i := 0; i < len(fileContent) && i < 8192; i++ {
			if fileContent[i] == 0 {
				isBinary = true
				break
			}
		}
	}

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

	var lines []string
	if !isBinary && fileContent != "" {
		lines = strings.Split(fileContent, "\n")
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

// maxDiffFileBytes is the largest working-tree file a diff read here pulls into memory, refused rather
// than tried when the stat already shows it bigger.
const maxDiffFileBytes int64 = 1_000_000

// repoFilePath jails a repository-relative path to the checkout in dir and returns the host file it
// names, symlink-resolved. The symlink check is the point: the working-tree read follows links, so a
// committed `key.pem -> ../../.ssh/id_rsa` would read any file the server user can open.
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
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	if !runCheckout(w, project.Path, branch) {
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"branch": branch})
}

// checkoutBranchFrom reads and validates the branch out of a checkout request body, writes the refusal
// and returns false when it is rejected. Both checkout endpoints share it, and the branch is a
// `git checkout` argument, so the validation lives here rather than per endpoint.
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

// runCheckout switches the checkout in dir to branch. A refusal answers with git's own reason, capped:
// git's full output is pages of server-side paths, and that goes to the log.
func runCheckout(w http.ResponseWriter, dir, branch string) bool {
	out, err := git.Command("", "-C", dir, "checkout", branch).CombinedOutput()
	if err != nil {
		logging.For(logging.Managers).Errorf("git checkout failed in %s: %v: %s", dir, err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git checkout failed: "+gitErrorSummary(out))
		return false
	}
	return true
}

// gitErrorSummary returns a trimmed, length-capped summary of git's output.
func gitErrorSummary(out []byte) string {
	s := strings.TrimSpace(string(out))
	const maxLen = 300
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

// handleGitRemote runs a project-level pull or push under the per-project mutex. op must be "pull" or
// "push".
func (h *GitHandler) handleGitRemote(w http.ResponseWriter, r *http.Request, op string) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	// Bounded to the request: an unreachable remote would otherwise hold the per-project mutex, and a
	// server-side git process, for the full ssh/TCP timeout.
	c := git.CommandContext(r.Context(), "", "-C", project.Path, op)
	// No c.Env: an extra list would replace the environment git.Command sets, which carries the prompt
	// refusal and the hook fence — that fence is why a pre-push hook does not run on Push.
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

// HandleTaskCommits returns the commits on a task's own branch, past its divergence point, plus the base
// commit for the diff selector. A task whose worktree is gone is answered from the project repo.
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
	baseRef := h.worktreeMgr.TaskDiffBaseline(task)

	result, err := h.worktreeMgr.GetTaskCommits(taskID, baseRef)
	if err != nil {
		if task.Branch != "" && errors.Is(err, git.ErrWorktreeNotFound) {
			result, err = h.getTaskCommitsFromProject(projectID, task, baseRef)
		}
		if code, ok := git.GitExitCode(err); ok && code == 128 {
			httputil.JSONError(w, http.StatusNotFound, "worktree not found")
			return
		}
		if err != nil {
			httputil.Fault(w, "git log", err)
			return
		}
	}
	if result.Commits == nil {
		result.Commits = []git.TaskCommit{}
	}
	httputil.JSONResponse(w, http.StatusOK, result)
}

// getTaskCommitsFromProject answers the commit list from the project checkout for a task whose worktree
// is gone, with the same refs and parser the worktree uses, so switching between them cannot show a
// different history. Head is BranchTip when captured at commit time, else Branch.
func (h *GitHandler) getTaskCommitsFromProject(projectID string, task *types.Task, baseRef string) (*git.TaskCommitsResult, error) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}

	target := task.BranchTip
	if target == "" {
		target = task.Branch
	}

	result, err := git.TaskCommitsIn(project.Path, baseRef, target)
	if err != nil {
		logging.For(logging.Git).With(logging.TaskID, task.ID).Infof("branch %s not found in project, returning empty commits: %v", target, err)
		return &git.TaskCommitsResult{Commits: []git.TaskCommit{}}, nil
	}
	return result, nil
}

// HandleTaskDiff returns the diff and untracked files for a task's worktree, falling back to the project
// repo when the worktree is gone. Query params:
//   - base: custom base ref (overrides the resolved baseline)
//   - head: second ref for a commit-to-commit diff
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
	// checkpointIdx is read as a flag only: a checkpoint diff must never come from the cached done-task
	// snapshot.

	// The canonical done-task diff (BaseCommit..BranchTip) is immutable, so it is served from the
	// persistent snapshot instead of re-running git on every open. Custom refs, checkpoint diffs and full
	// requests go through the live computation.
	if task.Status == types.StatusDone && task.BranchTip != "" && customBase == "" && checkpointIdxStr == "" && !fullDiff &&
		(headRef == "" || headRef == task.BranchTip) {
		if d, err := stream.MaterializeDoneTaskDiff(h.store, h.worktreeMgr, projectID, task); err == nil && d != nil && d.HasChanges {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(d.DiffJSON))
			return
		}
	}

	// A done task shows the committed diff, so with no head given BranchTip makes this a commit-to-commit
	// diff — a raw diff string that works even when the worktree is gone.
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
		// BaseCommit, not the branch name: after a fast-forward merge the pointer has moved forward and
		// the diff would come back empty. This is why the commit was captured at task start.
		baseRef = task.BaseCommit
	} else {
		baseRef = h.worktreeMgr.TaskDiffBaseline(task)
	}

	// A checkpoint headRef is a commit whose tree holds the whole working state, so baseRef..headRef is
	// the change up to and including it. A checkpoint that resolves to the baseline has no changes of its
	// own, and the honest diff for that is empty.
	taskDiff, err := h.worktreeMgr.GetTaskDiff(taskID, baseRef, headRef, fullDiff)
	if err != nil {
		if task.Branch != "" && errors.Is(err, git.ErrWorktreeNotFound) {
			taskDiff, err = h.getTaskDiffFromProject(projectID, task, baseRef, headRef)
		}
		if code, ok := git.GitExitCode(err); ok && code == 128 {
			httputil.JSONError(w, http.StatusNotFound, "worktree not found")
			return
		}
		if err != nil {
			httputil.Fault(w, "git diff", err)
			return
		}
	}
	if taskDiff.Untracked == nil {
		taskDiff.Untracked = []git.UntrackedFile{}
	}
	// Only the working-tree comparison belongs in diffCache; a ref comparison answers with its own content.
	if headRef == "" && customBase == "" && checkpointIdxStr == "" {
		h.diffCache.put(taskID, taskDiff)
	}

	// The default working-tree diff answers as a manifest of per-file metadata; ref comparisons and
	// full=1 return full content.
	if headRef == "" && customBase == "" && !fullDiff {
		stagedPaths := getStagedPaths(h.worktreeMgr, taskID)
		manifest := buildDiffManifest(taskDiff, stagedPaths, baseRef)
		httputil.JSONResponse(w, http.StatusOK, manifest)
		return
	}

	taskDiff = stripUntrackedLines(taskDiff)
	// ?parsed=1 attaches the per-file structure to a ref-comparison response, so the browser never
	// re-parses patch text with a second grammar; Go already parsed this diff for hashes and truncation.
	if r.URL.Query().Get("parsed") == "1" {
		taskDiff.ParsedFiles = git.SortedParsedFiles(taskDiff.Tracked)
	}
	httputil.JSONResponse(w, http.StatusOK, taskDiff)
}

// stripUntrackedLines copies d with Lines removed from the untracked entries, setting LineCount so the
// frontend knows the size without fetching content.
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

// diffManifest is GET /diff's per-file metadata (hashes, stats) without diff content.
type diffManifest struct {
	Files     map[string]diffManifestFile `json:"files"`
	ShortStat string                      `json:"short_stat"`
	BaseRef   string                      `json:"base_ref,omitempty"`
}

// getStagedPaths returns a set of file paths that are staged in the index — the index against HEAD,
// never against the diff's base ref: a base ref may be a different branch, and comparing the index to
// it would report all branch divergence as "staged".
func getStagedPaths(wm *git.WorktreeManager, taskID string) map[string]struct{} {
	if wm == nil {
		return nil
	}
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		return nil
	}
	cmd := git.Command("", "-C", worktreePath, "diff", "--staged", "--name-only", "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Exit 1 means changes exist: see HandleDiff.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
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
		// A new file is both an addition in git diff and untracked; the tracked section wins.
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

// HandleTaskDiffFile returns one file from the cached diff, tracked or untracked. Query param path is
// relative to the worktree; a task with no cached diff, or a path outside it, is an error.
func (h *GitHandler) HandleTaskDiffFile(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	// The cache holds only that task's working-tree diff (see taskDiffCache), so the pair in the path is
	// settled before it is read.
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	filePath := r.URL.Query().Get("path")
	fullFile := r.URL.Query().Get("full") == "1"
	if filePath == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	taskDiff, ok := h.diffCache.get(taskID)
	if !ok {
		httputil.JSONError(w, http.StatusNotFound, "diff not available (open the Changes panel first)")
		return
	}
	if f, ok := taskDiff.Tracked[filePath]; ok {
		if fullFile && f.Truncated {
			// Recomputed the way the panel's manifest was, so its rows match the list above.
			if fullDiff, _, err := h.currentTaskDiff(task, true); err == nil {
				if ff, ok := fullDiff.Tracked[filePath]; ok {
					f = ff
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
			if fullFile && u.Truncated && h.worktreeMgr != nil {
				worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
				if err == nil {
					// The cached path came from git status, which lists a symlink the way it lists a file, so
					// the re-read needs that listing's jail and the size cap; past the cap the cached lines
					// stand and truncated says so.
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

// getTaskDiffFromProject runs git diff in the project repo for a task whose worktree is gone, diffing
// baseRef against BranchTip when it was captured at commit time, else Branch.
func (h *GitHandler) getTaskDiffFromProject(projectID string, task *types.Task, baseRef, headRef string) (*git.TaskDiff, error) {
	project, err := h.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}

	// A done task is diffed from the commit captured at task start; see HandleTaskDiff.
	if task.Status == types.StatusDone && task.BaseCommit != "" {
		baseRef = task.BaseCommit
	}

	var refSpec string
	if headRef != "" {
		refSpec = fmt.Sprintf("%s..%s", baseRef, headRef)
	} else {
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

// stageOrUnstage applies one file's staged state in dir, with every path after `--` so a path like "-f"
// is never an option. On failure the response is written and false is returned, so callers just return.
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
	// A tracked file missing from disk comes back from HEAD. One staged as new then deleted is in no
	// commit, so git fails there and that error is ignored.
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
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	if !h.stageOrUnstage(w, project.Path, req.Path, req.Stage) {
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "action": stageActionName(req.Stage)})
}

// discardFile reverts one file in dir to HEAD, or removes it when HEAD holds no such path.
//
// Removal is `git clean` rather than deleting the path: clean leaves ignored files alone, and a folder
// another repository owns is not this one's to delete.
func discardFile(dir, path string) error {
	inHead, err := headHasPath(dir, path)
	if err != nil {
		return err
	}
	if inHead {
		// checkout writes the index along with the worktree, so it drops the staged half too.
		if out, err := git.Command("", "-C", dir, "checkout", "HEAD", "--", path).CombinedOutput(); err != nil {
			return gitStepError("git checkout", err, out)
		}
		return nil
	}
	// A file staged as new leaves the index here; an untracked path is not in the index at all.
	if out, err := git.Command("", "-C", dir, "reset", "-q", "HEAD", "--", path).CombinedOutput(); err != nil {
		return gitStepError("git reset", err, out)
	}
	if out, err := git.Command("", "-C", dir, "clean", "-f", "-q", "--", path).CombinedOutput(); err != nil {
		return gitStepError("git clean", err, out)
	}
	return nil
}

// headHasPath reports whether HEAD holds path, which is what decides a revert from a removal.
func headHasPath(dir, path string) (bool, error) {
	// An unborn HEAD fails this with exit 1, which is an answer rather than a fault.
	if _, err := git.Command("", "-C", dir, "rev-parse", "--verify", "--quiet", "HEAD").Output(); err != nil {
		if code, ok := git.GitExitCode(err); ok && code == 1 {
			return false, nil
		}
		return false, gitStepError("git rev-parse", err, nil)
	}
	out, err := git.Command("", "-C", dir, "ls-tree", "--name-only", "HEAD", "--", path).Output()
	if err != nil {
		return false, gitStepError("git ls-tree", err, nil)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// gitStepError names the failed git step and carries git's own words for the log; the response never
// repeats them.
func gitStepError(step string, err error, out []byte) error {
	if len(out) == 0 {
		return fmt.Errorf("%s: %w", step, err)
	}
	return fmt.Errorf("%s: %w: %s", step, err, strings.TrimSpace(string(out)))
}

// HandleProjectDiscardFile reverts one file in the project repo to HEAD, removing it when HEAD holds no
// path of that name.
// POST /api/projects/{projectID}/git/discard
// Body: {"path": "file.c"}
func (h *GitHandler) HandleProjectDiscardFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, err := h.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path required")
		return
	}
	// A path outside the repository is refused here, as a 400 naming the field.
	if _, err := repoFilePath(project.Path, req.Path); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A discard writes the index and worktree that stage and commit use, so it shares their lock.
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	if err := discardFile(project.Path, req.Path); err != nil {
		httputil.Fault(w, "git discard", err)
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
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
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	cmd := git.Command("", "-C", project.Path, "diff", "--cached", "--quiet")
	if err := cmd.Run(); err == nil {
		// Exit 0 is the refusal: nothing is staged.
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

// HandleTaskLog returns a task's commit log, in the same shape as the project log. The branch defaults
// to the task's own.
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

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	h.writeFullGitLog(w, worktreePath, branch, "", limit, offset)
}
