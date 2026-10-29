package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"mike/pkg/subproc"
)

// GitHandler handles git operations.
type GitHandler struct {
	store       *store.Store
	worktreeMgr *git.WorktreeManager
	diffCache   taskDiffCache
	// per-task worktree locks.
	getWorktreeMutex func(taskID string) func()
	// per-project git locks.
	getProjectMutex func(projectID string) func()
}

// taskDiffCache caches task worktree diffs against HEAD.
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

func (h *GitHandler) SetWorktreeMutex(fn func(taskID string) func()) {
	h.getWorktreeMutex = fn
}

func (h *GitHandler) SetProjectMutex(fn func(projectID string) func()) {
	h.getProjectMutex = fn
}

func (h *GitHandler) UpdateDiffCache(taskID string, taskDiff *git.TaskDiff) {
	h.diffCache.put(taskID, taskDiff)
}

// currentTaskDiff computes diff for Changes panel.
func (h *GitHandler) currentTaskDiff(task *types.Task, skipTruncate bool) (*git.TaskDiff, string, error) {
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

// GetDiffManifest builds diff manifest for WebSocket broadcast.
func (h *GitHandler) GetDiffManifest(task *types.Task) (any, error) {
	taskDiff, baseRef, err := h.currentTaskDiff(task, false)
	if err != nil {
		return nil, err
	}
	h.diffCache.put(task.ID, taskDiff)
	stagedPaths := getStagedPaths(h.worktreeMgr, task.ID)
	return buildDiffManifest(taskDiff, stagedPaths, baseRef), nil
}

// fullLogFormat git log pretty-format for parsed commits.
const fullLogFormat = `--format=%H|%h|%s|%aN|%aI|%P%n---BODY---%n%b%n---END---`

func (h *GitHandler) writeFullGitLog(w http.ResponseWriter, dir, branch, path string, limit, offset int) {
	args := []string{"-C", dir, "log"}
	if offset > 0 {
		args = append(args, "--skip", strconv.Itoa(offset))
	}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit+1))
	}
	// branch is a client value.
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

// HandleLog returns project commit log.
func (h *GitHandler) HandleLog(w http.ResponseWriter, r *http.Request) {
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	branch, ok := refParam(w, r, "branch", "HEAD")
	if !ok {
		return
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

func (h *GitHandler) getCurrentBranch(path string) string {
	c := git.Command("", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

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

func (h *GitHandler) getOriginTip(path, branch string) string {
	c := git.Command("", "-C", path, "rev-parse", "origin/"+branch)
	out, err := c.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// branchRef is one row of the branch listing: the short ref, and the worktree holding it checked out.
type branchRef struct {
	Name     string `json:"name"`
	Worktree string `json:"worktree,omitempty"`
	// TaskID is the task whose worktree holds this branch.
	TaskID string `json:"task_id,omitempty"`
}

// attachTaskIDs names the task whose row claims each branch, so a screen can offer the task for a branch
// this checkout cannot take. A store failure leaves the rows unlabelled rather than failing the listing.
func attachTaskIDs(refs []branchRef, s *store.Store, projectID string) {
	tasks, err := s.GetTasksByProject(projectID)
	if err != nil {
		logging.For(logging.Managers).Warnf("branches: task lookup for project %s failed: %v", projectID, err)
		return
	}
	byBranch := make(map[string]string, len(tasks))
	for _, task := range tasks {
		if task.Branch == "" {
			continue
		}
		if _, seen := byBranch[task.Branch]; !seen {
			byBranch[task.Branch] = task.ID
		}
	}
	for i := range refs {
		refs[i].TaskID = byBranch[refs[i].Name]
	}
}

// sameCheckout reports whether git's worktree path is the checkout this project is. Git prints its own
// resolved path, so a symlinked or trailing-separator path must not read as a second worktree.
func sameCheckout(worktreePath, projectPath string) bool {
	a, b := filepath.Clean(worktreePath), filepath.Clean(projectPath)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func gitBranchRefs(dir string, args ...string) ([]branchRef, error) {
	out, err := git.Command("", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return nil, err
	}
	var refs []branchRef
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, holder, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if name == "" {
			continue
		}
		if holder != "" && sameCheckout(holder, dir) {
			holder = ""
		}
		refs = append(refs, branchRef{Name: name, Worktree: holder})
	}
	return refs, nil
}

// HandleBranches lists project branches with the worktree holding each one, if any.
func (h *GitHandler) HandleBranches(w http.ResponseWriter, r *http.Request) {
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	refs, err := gitBranchRefs(project.Path, "branch", "-a", "--format=%(refname:short)%09%(worktreepath)")
	if err != nil {
		httputil.Fault(w, "git branches", err)
		return
	}
	attachTaskIDs(refs, h.store, project.ID)
	httputil.JSONResponse(w, http.StatusOK, refs)
}

// HandleDiff returns project diff as parsed output.
func (h *GitHandler) HandleDiff(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}

	base, ok := refParam(w, r, "base", "")
	if !ok {
		return
	}
	head, ok := refParam(w, r, "head", "")
	if !ok {
		return
	}
	cached := r.URL.Query().Get("cached") == "1"

	var c *exec.Cmd
	if cached {
		c = git.Command("", "-C", project.Path, "diff", "--cached")
	} else if base != "" && head != "" {
		// handle empty tree as root.
		if base == git.EmptyTreeHash {
			c = git.Command("", "-C", project.Path, "diff-tree", "--root", "--format=", "-p", head)
		} else {
			c = git.Command("", "-C", project.Path, "diff", base+".."+head)
		}
	} else {
		// working tree vs index.
		c = git.Command("", "-C", project.Path, "diff")
	}

	out, err := c.CombinedOutput()
	if err != nil && !diffExitedWithChanges(err) {
		httputil.Fault(w, "git diff", err)
		return
	}
	// returns parsed diff grammar.
	files := git.ParseUnifiedDiff(string(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(files); err != nil {
		logging.For(logging.Git).Infof("encoding parsed diff for project %s: %v", projectID, err)
	}
}

// HandleStatus returns working tree status, HEAD, and author.
func (h *GitHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	c := git.Command("", "-C", project.Path, "status", "--porcelain")
	out, err := c.CombinedOutput()
	if err != nil {
		httputil.Fault(w, "git status", err)
		return
	}
	clean := len(strings.TrimSpace(string(out))) == 0
	// HEAD hash for client comparison.
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

// remoteAheadBehind counts ahead/behind relative to origin.
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

// HandleProjectDiffFile returns one file's diff and content.
func (h *GitHandler) HandleProjectDiffFile(w http.ResponseWriter, r *http.Request) {
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	// jail the working-tree read.
	worktreeFile, err := repoFilePath(project.Path, filePath)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	metaOnly := r.URL.Query().Get("meta") == "1"

	c := git.Command("", "-C", project.Path, "diff", "--", filePath)
	out, err := c.CombinedOutput()
	if err != nil && !diffExitedWithChanges(err) {
		httputil.Fault(w, "git diff", err)
		return
	}

	// stat before reading.
	var fileSize int64
	before, statErr := os.Lstat(worktreeFile)
	if statErr == nil {
		fileSize = before.Size()
	}

	// apply size cap before reading.
	var fileContent string
	var lineCount int
	var isBinary bool
	if statErr == nil && fileSize <= maxDiffFileBytes {
		if f, _, err := fsutil.OpenVerified(worktreeFile, before); err == nil {
			contentOut, contentErr := io.ReadAll(io.LimitReader(f, maxDiffFileBytes))
			f.Close()
			if contentErr == nil {
				fileContent = string(contentOut)
				lineCount = strings.Count(fileContent, "\n")
				if fileContent != "" && fileContent[len(fileContent)-1] != '\n' {
					lineCount++
				}
			}
		}
	}

	if fileContent != "" {
		for i := range min(len(fileContent), 8192) {
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

// maxDiffFileBytes max working-tree file size for memory reads.
const maxDiffFileBytes int64 = 1_000_000

// repoFilePath jails repo-relative path and resolves symlinks.
func repoFilePath(dir, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the repository", rel)
	}
	return fsutil.Contained(filepath.Join(dir, rel), []string{filepath.Clean(dir)})
}

// HandleCheckout checks out branch in project checkout.
func (h *GitHandler) HandleCheckout(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, ok := resolveProject(w, r, h.store)
	if !ok {
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

// diffExitedWithChanges reports git diff exit 1 as success.
func diffExitedWithChanges(err error) bool {
	exitErr, ok := err.(*exec.ExitError)
	return ok && exitErr.ExitCode() == 1
}

// answerTaskGitFault handles task-scoped git faults.
func answerTaskGitFault(w http.ResponseWriter, err error, operation string) bool {
	if err == nil {
		return false
	}
	if code, ok := git.GitExitCode(err); ok && code == 128 {
		httputil.JSONError(w, http.StatusNotFound, "worktree not found")
		return true
	}
	httputil.Fault(w, operation, err)
	return true
}

// refParam reads and validates git ref from query.
func refParam(w http.ResponseWriter, r *http.Request, queryKey, def string) (string, bool) {
	ref := r.URL.Query().Get(queryKey)
	if ref == "" {
		ref = def
	}
	if ref != "" {
		if err := types.ValidateBranchName(ref); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return "", false
		}
	}
	return ref, true
}

// checkoutBranchFrom reads and validates branch from request body.
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

// runCheckout switches checkout to branch.
func runCheckout(w http.ResponseWriter, dir, branch string) bool {
	out, err := git.Command("", "-C", dir, "checkout", branch).CombinedOutput()
	if err != nil {
		logging.For(logging.Managers).Errorf("git checkout failed in %s: %v: %s", dir, err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git checkout failed: "+subproc.Summarize(out, subproc.AnswerMax))
		return false
	}
	return true
}

// handleGitRemote runs project-level pull/push under lock.
func (h *GitHandler) handleGitRemote(w http.ResponseWriter, r *http.Request, op string) {
	projectID := r.PathValue("projectID")
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	// bound to request to avoid lock-holding timeouts.
	c := git.CommandContext(r.Context(), "", "-C", project.Path, op)
	// do not override git.Command environment.
	if out, err := c.CombinedOutput(); err != nil {
		logging.For(logging.Managers).Errorf("git %s failed: %v: %s", op, err, out)
		httputil.JSONError(w, http.StatusInternalServerError, "git "+op+" failed: "+subproc.Summarize(out, subproc.AnswerMax))
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

// HandleTaskCommits returns task commits past divergence point; falls back to project repo if worktree is gone.
func (h *GitHandler) HandleTaskCommits(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, ok := resolveTaskWithWorktree(w, r, h.store, h.worktreeMgr)
	if !ok {
		return
	}
	if task.Status == types.StatusBacklog {
		httputil.JSONResponse(w, http.StatusOK, git.TaskCommitsResult{Commits: []git.TaskCommit{}})
		return
	}
	baseRef := h.worktreeMgr.TaskDiffBaseline(task)

	result, err := h.worktreeMgr.GetTaskCommits(taskID, baseRef)
	if err != nil && task.Branch != "" && errors.Is(err, git.ErrWorktreeNotFound) {
		result, err = h.getTaskCommitsFromProject(projectID, task, baseRef)
	}
	if answerTaskGitFault(w, err, "git log") {
		return
	}
	if result.Commits == nil {
		result.Commits = []git.TaskCommit{}
	}
	httputil.JSONResponse(w, http.StatusOK, result)
}

// getTaskCommitsFromProject returns task commits from project repo if worktree is gone; uses same refs/parser as worktree.
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

// HandleTaskDiff returns task diff and untracked files; falls back to project repo if worktree is gone.
func (h *GitHandler) HandleTaskDiff(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, ok := resolveTaskWithWorktree(w, r, h.store, h.worktreeMgr)
	if !ok {
		return
	}

	headRef, ok := refParam(w, r, "head", "")
	if !ok {
		return
	}
	customBase, ok := refParam(w, r, "base", "")
	if !ok {
		return
	}
	fullDiff := r.URL.Query().Get("full") == "1"

	// The canonical done-task diff (BaseCommit..BranchTip) is immutable, so it is served from the
	// persistent snapshot instead of re-running git on every open. A custom base, a head that is not the
	// tip, and full requests go through the live computation.
	if task.Status == types.StatusDone && task.BranchTip != "" && customBase == "" && !fullDiff &&
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

	// Every comparison here asks TaskDiffBaseline, so a checkpoint's diff measures from the same commit as
	// the Changes panel, the commit list and the card stats — the fork point in a forked thread. A checkpoint
	// headRef is a commit whose tree holds the whole working state, so the answer is the change up to and
	// including it; a checkpoint that resolves to the baseline has no changes of its own, and the honest diff
	// for that is empty.
	taskDiff, baseRef, err := h.taskDiffBetween(task, customBase, headRef, fullDiff)
	if answerTaskGitFault(w, err, "git diff") {
		return
	}
	if taskDiff.Untracked == nil {
		taskDiff.Untracked = []git.UntrackedFile{}
	}
	// Only the working-tree comparison belongs in diffCache; a ref comparison answers with its own content.
	if headRef == "" && customBase == "" {
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

// stripUntrackedLines copies d without untracked file content, keeping LineCount for size reporting.
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

// diffManifest is per-file metadata without diff content.
type diffManifest struct {
	Files     map[string]diffManifestFile `json:"files"`
	ShortStat string                      `json:"short_stat"`
	BaseRef   string                      `json:"base_ref,omitempty"`
}

// getStagedPaths returns paths staged in index against HEAD.
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
	if err != nil && !diffExitedWithChanges(err) {
		return nil
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

// trackedFileResponse returns a tracked file's diff response.
func trackedFileResponse(f *git.TrackedDiffFile) map[string]any {
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
	return resp
}

// diffBaseline returns the commit to measure from: customBase or task's pinned base.
func (h *GitHandler) diffBaseline(task *types.Task, customBase string) string {
	if customBase != "" {
		return customBase
	}
	return h.worktreeMgr.TaskDiffBaseline(task)
}

// taskDiffBetween computes diff between two trees, using task's baseline if base is empty; falls back to project repo.
func (h *GitHandler) taskDiffBetween(task *types.Task, baseRef, headRef string, full bool) (*git.TaskDiff, string, error) {
	base := h.diffBaseline(task, baseRef)
	taskDiff, err := h.worktreeMgr.GetTaskDiff(task.ID, base, headRef, full)
	if err != nil && task.Branch != "" && errors.Is(err, git.ErrWorktreeNotFound) {
		taskDiff, err = h.getTaskDiffFromProject(task, base, headRef, full)
	}
	if taskDiff != nil {
		taskDiff.BaseRef = base
	}
	return taskDiff, base, err
}

// writeRefFileDiff returns diff for one file between two refs.
func (h *GitHandler) writeRefFileDiff(w http.ResponseWriter, task *types.Task, filePath, baseRef, headRef string, full bool) {
	taskDiff, _, err := h.taskDiffBetween(task, baseRef, headRef, full)
	if answerTaskGitFault(w, err, "git diff") {
		return
	}
	f, ok := taskDiff.Tracked[filePath]
	if !ok {
		httputil.JSONError(w, http.StatusNotFound, "file not found in diff")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, trackedFileResponse(f))
}

// HandleTaskDiffFile returns diff for one file, tracked or untracked.
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
	headRef, ok := refParam(w, r, "head", "")
	if !ok {
		return
	}
	customBase, ok := refParam(w, r, "base", "")
	if !ok {
		return
	}
	if headRef != "" {
		h.writeRefFileDiff(w, task, filePath, customBase, headRef, fullFile)
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
		httputil.JSONResponse(w, http.StatusOK, trackedFileResponse(f))
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
					defer target.Close()
					if size <= maxDiffFileBytes {
						if data, readErr := io.ReadAll(io.LimitReader(target, maxDiffFileBytes)); readErr == nil {
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

// getTaskDiffFromProject answers the diff of a task whose worktree is gone.
func (h *GitHandler) getTaskDiffFromProject(task *types.Task, baseRef, headRef string, fullDiff bool) (*git.TaskDiff, error) {
	project, err := h.store.GetProject(task.ProjectID)
	if err != nil {
		return nil, err
	}

	// A done task's canonical diff is the pinned BaseCommit..BranchTip — the body the stored snapshot
	// serves too (stream.resolveDoneTaskBaseRef). Pinning keeps this fallback answering the same
	// comparison the board's stats count.
	if task.Status == types.StatusDone && task.BaseCommit != "" {
		baseRef = task.BaseCommit
	}

	target := headRef
	if target == "" {
		target = task.BranchTip
	}
	if target == "" {
		target = task.Branch
	}
	return h.worktreeMgr.TaskDiffFromRepo(project.Path, baseRef, target, fullDiff)
}

// gitBatchFailure is one path a batch write did not land on. Error is MIKE's own sentence; git's words go to
// the log.
type gitBatchFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// gitBatchAnswer is what a batch write answers. A refusal on some paths is still a 200, because the panel
// reports the counts and rereads git's own state; Applied and Failed are never null.
type gitBatchAnswer struct {
	Status  string            `json:"status"`
	Action  string            `json:"action"`
	Applied []string          `json:"applied"`
	Failed  []gitBatchFailure `json:"failed"`
}

func answerBatch(w http.ResponseWriter, action string, applied []string, failed []gitBatchFailure) {
	if applied == nil {
		applied = []string{}
	}
	if failed == nil {
		failed = []gitBatchFailure{}
	}
	httputil.JSONResponse(w, http.StatusOK, gitBatchAnswer{Status: "ok", Action: action, Applied: applied, Failed: failed})
}

// batchPaths is a request's path list as this package uses it: at least one entry, none empty, duplicates
// collapsed so a repeated path is one write and one entry in the answer.
func batchPaths(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	paths := make([]string, 0, len(raw))
	for _, path := range raw {
		if path == "" {
			return nil, fmt.Errorf("paths must not contain an empty path")
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths required")
	}
	return paths, nil
}

// stageOnce moves the paths between the index and HEAD in one git call. On failure the string is the sentence
// for the response and the error carries git's words for the log.
func stageOnce(dir string, paths []string, stage bool) (string, error) {
	step, argv := "add", []string{"-C", dir, "add", "--"}
	if !stage {
		step, argv = "reset", []string{"-C", dir, "reset", "HEAD", "--"}
	}
	out, err := git.Command("", append(argv, paths...)...).CombinedOutput()
	if err != nil {
		return "git " + step + " failed", gitStepError("git "+step, err, out)
	}
	return "", nil
}

// stagePaths applies one stage decision to every path in dir and says which ones landed. git refuses `add`
// outright when any pathspec matches nothing, so a refused group is retried path by path.
func stagePaths(dir string, paths []string, stage bool) ([]string, []gitBatchFailure) {
	log := logging.For(logging.Managers)
	var applied []string
	var failed []gitBatchFailure
	if _, err := stageOnce(dir, paths, stage); err != nil {
		log.Warnf("git stage refused the group, retrying each path: %v", err)
		for _, path := range paths {
			msg, err := stageOnce(dir, []string{path}, stage)
			if err != nil {
				log.Errorf("git stage failed: %v", err)
				failed = append(failed, gitBatchFailure{Path: path, Error: msg})
				continue
			}
			applied = append(applied, path)
		}
	} else {
		applied = append(applied, paths...)
	}
	// Unstaging a tracked file that is gone from disk brings it back from HEAD. A path staged as new is in no
	// commit, so git fails there and that error is ignored.
	if !stage {
		for _, path := range applied {
			if _, statErr := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(statErr) {
				continue
			}
			_, _ = git.Command("", "-C", dir, "checkout", "HEAD", "--", path).CombinedOutput()
		}
	}
	return applied, failed
}

// stageActionName is the "action" field both stage endpoints report.
func stageActionName(stage bool) string {
	if stage {
		return "staged"
	}
	return "unstaged"
}

// stageBody reads the body both stage endpoints answer. A false ok means the refusal is already written.
func stageBody(w http.ResponseWriter, r *http.Request) (paths []string, stage bool, ok bool) {
	var req struct {
		Paths []string `json:"paths"`
		Stage bool     `json:"stage"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return nil, false, false
	}
	paths, err := batchPaths(req.Paths)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return nil, false, false
	}
	return paths, req.Stage, true
}

// answerStage moves the paths in dir and writes the outcome. The caller holds the lock for that directory.
func answerStage(w http.ResponseWriter, dir string, paths []string, stage bool) {
	applied, failed := stagePaths(dir, paths, stage)
	answerBatch(w, stageActionName(stage), applied, failed)
}

// HandleStagePaths stages or unstages every named file in the task's worktree.
// POST /api/projects/{projectID}/tasks/{taskID}/diff/stage
// Body: {"paths": ["file.c", "other.c"], "stage": true}
func (h *GitHandler) HandleStagePaths(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	paths, stage, ok := stageBody(w, r)
	if !ok {
		return
	}
	task, ok := resolveTaskWithWorktree(w, r, h.store, h.worktreeMgr)
	if !ok {
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
	answerStage(w, worktreePath, paths, stage)
}

// HandleProjectStagePaths stages or unstages every named file in the project repo.
// POST /api/projects/{projectID}/git/stage
// Body: {"paths": ["file.c", "other.c"], "stage": true}
func (h *GitHandler) HandleProjectStagePaths(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	paths, stage, ok := stageBody(w, r)
	if !ok {
		return
	}
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	answerStage(w, project.Path, paths, stage)
}

// discardFault is what every refused discard answers with; git's own words stay in the log.
const discardFault = "git discard failed"

// discardPaths reverts each path to HEAD, or removes the ones HEAD never held (docs/git-operations.md →
// Discard). A git call fails the whole group without naming the path that caused it, so a refused group is
// retried path by path.
func discardPaths(dir string, paths []string) ([]string, []gitBatchFailure) {
	log := logging.For(logging.Managers)
	inHead, err := headPaths(dir, paths)
	if err != nil {
		log.Errorf("git discard could not read HEAD: %v", err)
		// With HEAD unreadable nothing can be told a revert from a removal, so no path is touched.
		failed := make([]gitBatchFailure, 0, len(paths))
		for _, path := range paths {
			failed = append(failed, gitBatchFailure{Path: path, Error: discardFault})
		}
		return nil, failed
	}
	var revert, remove []string
	for _, path := range paths {
		if inHead[path] {
			revert = append(revert, path)
		} else {
			remove = append(remove, path)
		}
	}
	err = discardGrouped(dir, revert, remove)
	if err == nil {
		return paths, nil
	}
	log.Warnf("git discard refused the group, retrying each path: %v", err)
	var applied []string
	var failed []gitBatchFailure
	for _, path := range paths {
		if err := discardOne(dir, path, inHead[path]); err != nil {
			log.Errorf("git discard failed: %v", err)
			failed = append(failed, gitBatchFailure{Path: path, Error: discardFault})
			continue
		}
		applied = append(applied, path)
	}
	return applied, failed
}

// discardOne is a group of one: the per-path retry of whatever discardGrouped refused.
func discardOne(dir, path string, inHead bool) error {
	if inHead {
		return discardGrouped(dir, []string{path}, nil)
	}
	return discardGrouped(dir, nil, []string{path})
}

// discardGrouped reverts the inHead paths with `checkout HEAD`, which writes the index along with the
// worktree, so the staged half of an edit goes too. The rest are removed by `reset -q HEAD` (a file staged as
// new leaves the index here) and then `clean -f -q`.
func discardGrouped(dir string, inHead, notInHead []string) error {
	if len(inHead) > 0 {
		if out, err := git.Command("", append([]string{"-C", dir, "checkout", "HEAD", "--"}, inHead...)...).CombinedOutput(); err != nil {
			return gitStepError("git checkout", err, out)
		}
	}
	if len(notInHead) == 0 {
		return nil
	}
	// clean rather than deleting the path: it leaves ignored files alone, and a folder another repository
	// owns is not this one's to delete.
	for _, step := range [][]string{{"reset", "-q", "HEAD", "--"}, {"clean", "-f", "-q", "--"}} {
		argv := append([]string{"-C", dir}, step...)
		if out, err := git.Command("", append(argv, notInHead...)...).CombinedOutput(); err != nil {
			return gitStepError("git "+step[0], err, out)
		}
	}
	return nil
}

// headPaths reports which of the paths HEAD holds, which is what decides a revert from a removal; the index
// cannot answer it, because a staged deletion holds no entry there. `-z` keeps the names comparable: ls-tree
// quotes a path outside ASCII, and a quoted name never equals the path that was asked for.
func headPaths(dir string, paths []string) (map[string]bool, error) {
	// An unborn HEAD fails this with exit 1, which is an answer rather than a fault.
	if _, err := git.Command("", "-C", dir, "rev-parse", "--verify", "--quiet", "HEAD").Output(); err != nil {
		if code, ok := git.GitExitCode(err); ok && code == 1 {
			return map[string]bool{}, nil
		}
		return nil, gitStepError("git rev-parse", err, nil)
	}
	out, err := git.Command("", append([]string{"-C", dir, "ls-tree", "-z", "--name-only", "HEAD", "--"}, paths...)...).Output()
	if err != nil {
		return nil, gitStepError("git ls-tree", err, nil)
	}
	inHead := make(map[string]bool, len(paths))
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			inHead[name] = true
		}
	}
	return inHead, nil
}

// gitStepError names the failed git step and carries git's own words for the log; the response never
// repeats them.
func gitStepError(step string, err error, out []byte) error {
	if len(out) == 0 {
		return fmt.Errorf("%s: %w", step, err)
	}
	return fmt.Errorf("%s: %w: %s", step, err, strings.TrimSpace(string(out)))
}

// HandleProjectDiscardPaths reverts every named file in the project repo to HEAD, removing the ones HEAD
// does not hold.
// POST /api/projects/{projectID}/git/discard
// Body: {"paths": ["file.c", "other.c"]}
func (h *GitHandler) HandleProjectDiscardPaths(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	var req struct {
		Paths []string `json:"paths"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	paths, err := batchPaths(req.Paths)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A path outside the repository is refused here, as a 400 naming the field, before any path is written.
	for _, path := range paths {
		if _, err := repoFilePath(project.Path, path); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// A discard writes the index and worktree that stage and commit use, so it shares their lock.
	if h.getProjectMutex != nil {
		defer h.getProjectMutex(projectID)()
	}
	applied, failed := discardPaths(project.Path, paths)
	answerBatch(w, "discarded", applied, failed)
}

// HandleProjectCommit commits staged changes in the project repo.
// POST /api/projects/{projectID}/git/commit
// Body: {"message": "commit message"}
func (h *GitHandler) HandleProjectCommit(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	project, ok := resolveProject(w, r, h.store)
	if !ok {
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

	branch, ok := refParam(w, r, "branch", task.Branch)
	if !ok {
		return
	}
	if branch == "" {
		branch = "HEAD"
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	h.writeFullGitLog(w, worktreePath, branch, "", limit, offset)
}
