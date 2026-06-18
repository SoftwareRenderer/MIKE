package managers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	bkg "kanban/backend"
	"kanban/backend/config"
	"kanban/backend/exec"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/stream"
	"kanban/backend/store"
	"kanban/backend/term"
	"kanban/backend/types"
)

// TaskHandler handles task CRUD operations.
type TaskHandler struct {
	store          *store.Store
	boardStreamMgr *stream.BoardStreamManager
	configStore    *config.ConfigStore
	terminalMgr    *term.TerminalManager
	executor       *exec.TaskExecutor
	worktreeMgr    *git.WorktreeManager
	commitQueue    *bkg.CommitQueue
}

// NewTaskHandler creates a new TaskHandler.
func NewTaskHandler(store *store.Store, boardStreamMgr *stream.BoardStreamManager, configStore *config.ConfigStore, terminalMgr *term.TerminalManager, executor *exec.TaskExecutor, worktreeMgr *git.WorktreeManager, commitQueue *bkg.CommitQueue) *TaskHandler {
	return &TaskHandler{store: store, boardStreamMgr: boardStreamMgr, configStore: configStore, terminalMgr: terminalMgr, executor: executor, worktreeMgr: worktreeMgr, commitQueue: commitQueue}
}

// EnrichWorktreeExists sets the WorktreeExists field on tasks based on filesystem state.
// It uses the workspace_dir from config to build the worktree path.
func EnrichWorktreeExists(tasks []types.Task, workspaceDir string) {
	for i := range tasks {
		path := filepath.Join(workspaceDir, tasks[i].ID)
		_, err := os.Stat(path)
		tasks[i].WorktreeExists = err == nil
	}
}

// enrichTaskWorktree sets the WorktreeExists field on a single task.
func enrichTaskWorktree(task *types.Task, workspaceDir string) {
	path := filepath.Join(workspaceDir, task.ID)
	_, err := os.Stat(path)
	task.WorktreeExists = err == nil
}

// HandleList lists or creates tasks for a project.
func (h *TaskHandler) HandleList(w http.ResponseWriter, r *http.Request, projectID string) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := h.store.GetTasksByProject(projectID)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				EnrichWorktreeExists(tasks, cfg.WorkspaceDir)
			}
		}
		httputil.JSONResponse(w, http.StatusOK, tasks)
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var task types.Task
		if err := json.Unmarshal(body, &task); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		task.ID = GenerateTaskID()
		task.ProjectID = projectID
		if task.Status == "" {
			task.Status = "backlog"
		}
		now := time.Now().UTC()
		task.CreatedAt = now
		task.UpdatedAt = now
		if task.Title == "" && task.Prompt != "" {
			task.Title = AutoGenerateTitle(task.Prompt)
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				if cfg.DefaultAutoCommit != nil && task.AutoCommit == false {
					task.AutoCommit = *cfg.DefaultAutoCommit
				}
				if cfg.DefaultPlanMode != nil && task.PlanMode == false {
					task.PlanMode = *cfg.DefaultPlanMode
				}
				if cfg.DefaultMaxFailedTurns != nil && task.MaxConsecutiveFailedTurns == 0 {
					task.MaxConsecutiveFailedTurns = *cfg.DefaultMaxFailedTurns
				}
			}
		}
		// Validate parent_task_id if provided
		if task.ParentTaskID != "" {
			parent, err := h.store.GetTask(projectID, task.ParentTaskID)
			if err != nil {
				httputil.JSONError(w, http.StatusBadRequest, "parent task not found")
				return
			}
			// Prevent self-reference (won't happen for new tasks, but guard anyway)
			if task.ParentTaskID == task.ID {
				httputil.JSONError(w, http.StatusBadRequest, "task cannot be its own parent")
				return
			}
			// Only tasks in backlog can accept subtasks
			if parent.Status != "backlog" {
				httputil.JSONError(w, http.StatusBadRequest, "parent task must be in backlog to accept subtasks")
				return
			}
		}
		// If the first line is short (≤5 words), use it as the title directly
		if IsShortTitle(task.Prompt) {
			task.TitleGenerating = false
		} else {
			task.TitleGenerating = true
		}
		if err := h.store.CreateTask(&task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Create default thread for the task
		defaultThreadID := GenerateThreadID()
		if err := h.store.CreateThread(&types.TaskThread{
			ID:        defaultThreadID,
			TaskID:    task.ID,
			Name:      "Main",
			IsActive:  true,
			GitBranch: task.Branch,
			CreatedAt: now,
		}); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create default thread: %v", err))
			return
		}
		task.CurrentThreadID = defaultThreadID
		if err := h.store.UpdateTask(&task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Store the task prompt as the first user message in the conversation
		if task.Prompt != "" {
			_ = h.store.CreateTaskMessage(&types.TaskMessage{
				ID:        uuid.New().String(),
				TaskID:    task.ID,
				ThreadID:  defaultThreadID,
				Role:      "user",
				Content:   task.Prompt,
				CreatedAt: now,
			})
		}
		// Spawn async goroutine to generate a better title via LLM
		// (skip if first line was already a short, good-enough title)
		if task.TitleGenerating {
			h.startTitleGeneration(projectID, task.ID, task.Prompt)
		}
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusCreated, task)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleByID handles GET/PUT/DELETE for a single task.
func (h *TaskHandler) HandleByID(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	switch r.Method {
	case http.MethodGet:
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				enrichTaskWorktree(task, cfg.WorkspaceDir)
			}
		}
		httputil.JSONResponse(w, http.StatusOK, task)
	case http.MethodPut:
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, task)
		task.ID = taskID
		task.ProjectID = projectID
		task.UpdatedAt = time.Now()
		if err := h.store.UpdateTask(task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				enrichTaskWorktree(task, cfg.WorkspaceDir)
			}
		}
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusOK, task)
	case http.MethodDelete:
		if _, err := h.store.GetTask(projectID, taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		h.cleanupTask(taskID)
		h.broadcastTaskDeleted(projectID, taskID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleClearDone deletes all tasks with status 'done' for a project.
func (h *TaskHandler) HandleClearDone(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tasks, err := h.store.GetTasksByProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to get tasks")
		return
	}

	var deleted int
	for _, task := range tasks {
		if task.Status != "done" {
			continue
		}
		taskID := task.ID
		h.cleanupTask(taskID)
		h.broadcastTaskDeleted(projectID, taskID)
		deleted++
	}

	httputil.JSONResponse(w, http.StatusOK, map[string]interface{}{"deleted": deleted})
}

// HandleMove moves a task to a different column.
func (h *TaskHandler) HandleMove(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req types.MoveTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	// Block subtasks from moving to Backlog when an ancestor is In Progress.
	if req.Status == "backlog" && task.ParentTaskID != "" {
		if err := h.checkAncestorInProgress(task); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	task.Status = req.Status
	task.UpdatedAt = time.Now()

	if req.Status == "done" {
		task.StatusState = "completed"
	} else if task.StatusState == "completed" {
		// Task moved away from Done — reset status so it can run again
		task.StatusState = "idle"
	}
	if req.Status == "in_progress" && task.StatusState != "running" {
		// If this task is a subtask, use subtask-aware start (moves parent only, not siblings)
		if task.ParentTaskID != "" {
			if err := h.StartSubtaskWithParent(projectID, taskID); err != nil {
				log.Printf("[move] task=%s subtask start failed: %v", taskID, err)
				task.StatusState = "failed"
				task.Output = err.Error()
			}
		} else {
			// Check if task has subtasks — if so, move all incomplete children and start at deepest level
			children, _ := h.store.GetChildren(taskID)
			if len(children) > 0 {
				// Move all incomplete descendants (children, grandchildren, etc.) to in_progress
				h.moveAllDescendantsToInProgress(taskID)
				selectedID := h.SelectSubtaskToStart(task, children)
				if selectedID != "" {
					log.Printf("[move] task=%s has subtasks, starting subtask %s via LLM selection", taskID, selectedID)
					if err := h.StartSubtaskWithParent(projectID, selectedID); err != nil {
						log.Printf("[move] task=%s subtask start failed: %v", taskID, err)
						task.StatusState = "failed"
						task.Output = err.Error()
					}
				}
			} else {
				if err := h.executor.StartTask(projectID, taskID); err != nil {
					log.Printf("[move] task=%s auto-start failed: %v", taskID, err)
					task.StatusState = "failed"
					task.Output = err.Error()
				}
			}
		}
	}
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If a subtask moved to review/done, auto-start the next sibling
	if task.ParentTaskID != "" && (req.Status == "review" || req.Status == "done") {
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID, req.Status)
	}

	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// autoStartNextSibling delegates to the executor's sibling auto-start logic.
func (h *TaskHandler) autoStartNextSibling(projectID, finishedTaskID, parentTaskID, finishedStatus string) error {
	return h.executor.AutoStartNextSibling(projectID, finishedTaskID, parentTaskID, finishedStatus)
}

// moveAllDescendantsToInProgress recursively moves all descendant tasks (children, grandchildren, etc.)
// to in_progress/idle. Only tasks in backlog or in_progress+idle are moved.
func (h *TaskHandler) moveAllDescendantsToInProgress(taskID string) {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[move-descendants] failed to get descendants of %s: %v", taskID, err)
		return
	}
	for i := range descendants {
		if descendants[i].Status == "backlog" || (descendants[i].Status == "in_progress" && descendants[i].StatusState == "idle") {
			descendants[i].Status = "in_progress"
			descendants[i].StatusState = "idle"
			descendants[i].UpdatedAt = time.Now().UTC()
			if err := h.store.UpdateTask(&descendants[i]); err != nil {
				log.Printf("[move-descendants] failed to update descendant %s: %v", descendants[i].ID, err)
			}
		}
	}
}

// checkAncestorInProgress walks up the ancestor chain and returns an error
// if any ancestor (parent, grandparent, etc.) has status "in_progress".
func (h *TaskHandler) checkAncestorInProgress(task *types.Task) error {
	visited := make(map[string]struct{})
	currentID := task.ParentTaskID
	for currentID != "" {
		if _, ok := visited[currentID]; ok {
			break // avoid infinite loop on circular references
		}
		visited[currentID] = struct{}{}
		ancestor, err := h.store.GetTaskByTaskID(currentID)
		if err != nil {
			break // ancestor not found, stop walking
		}
		if ancestor.Status == "in_progress" {
			return fmt.Errorf("cannot move to Backlog: parent task is In Progress")
		}
		currentID = ancestor.ParentTaskID
	}
	return nil
}

// HandleCommitAndDone commits the worktree changes, merges into the origin branch, and moves the task to Done.
// Operations are serialized per project to prevent git race conditions.
func (h *TaskHandler) HandleCommitAndDone(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

	// Serialize commit-and-done per project to prevent git race conditions
	h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "queued")
	release := h.commitQueue.Acquire(projectID)
	defer release()
	h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "processing")

	// Try to commit worktree changes (skip if worktree doesn't exist)
	_, wtErr := h.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat")
	worktreeExists := wtErr == nil
	if worktreeExists {
		// Worktree exists — stage and commit
		_ = h.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
		_ = h.worktreeMgr.GitRunInWorktree(taskID, "commit", "-m", task.Title, "--allow-empty")
	}

	// Capture branch tip before merge (for post-commit diff comparison)
	if task.Branch != "" {
		project, pErr := h.store.GetProject(projectID)
		if pErr == nil {
			c := osexec.Command("git", "-C", project.Path, "rev-parse", task.Branch)
			if out, err := c.CombinedOutput(); err == nil {
				task.BranchTip = strings.TrimSpace(string(out))
			}
		}
	}

	// Merge worktree into origin branch
	mergeErr := h.mergeWorktreeIntoOrigin(projectID, taskID, task)
	if mergeErr != nil {
		log.Printf("[commit-and-done] task=%s merge failed: %v", taskID, mergeErr)
		// Log merge error as system message in the task thread
		h.logSystemMessage(taskID, task.CurrentThreadID, fmt.Sprintf("Merge failed: %v", mergeErr))
		// Continue to move task to done even if merge fails
	}

	// Move task to Done
	task.Status = "done"
	task.StatusState = "completed"
	task.UpdatedAt = time.Now()
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// If a subtask was marked done, auto-start the next sibling
	if task.ParentTaskID != "" {
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID, "done")
	}

	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// mergeWorktreeIntoOrigin merges the task's worktree branch into the project's current branch.
// On merge conflict, it uses the LLM to resolve conflicts.
func (h *TaskHandler) mergeWorktreeIntoOrigin(projectID, taskID string, task *types.Task) error {
	// BaseRef is a commit hash. Find which branch it belongs to.
	originBranch := ""
	if task.BaseRef != "" {
		// Check if BaseRef is a commit hash (40 hex chars)
		if len(task.BaseRef) == 40 && isHex(task.BaseRef) {
			// Find the branch that points to this commit (or a descendant)
			project, pErr := h.store.GetProject(projectID)
			if pErr == nil {
				// Try project's stored origin_branch first
				if project.OriginBranch != "" {
					originBranch = project.OriginBranch
				}
				// Verify: check if originBranch is a descendant of BaseRef
				if originBranch != "" {
					c := osexec.Command("git", "-C", project.Path, "merge-base", "--is-ancestor", task.BaseRef, originBranch)
					if c.Run() != nil {
						originBranch = "" // BaseRef is not an ancestor, don't use this branch
					}
				}
			}
		}
	}
	// Fall back to live HEAD
	if originBranch == "" {
		var err error
		originBranch, err = h.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			log.Printf("[merge] task=%s not a git repo, skipping merge: %v", taskID, err)
			return nil
		}
	}
	originBranch = strings.TrimSpace(originBranch)
	if originBranch == "" || originBranch == "HEAD" {
		log.Printf("[merge] task=%s no branch (detached HEAD), skipping merge", taskID)
		return nil
	}
	taskBranch := task.Branch
	if taskBranch == "" {
		log.Printf("[merge] task=%s no task branch set, skipping merge", taskID)
		return nil
	}

	// Stash any working tree changes to allow clean checkout
	hadStash := false
	if err := h.worktreeMgr.GitRunInProject(projectID, "stash", "push", "-m", "kanban merge stash"); err == nil {
		hadStash = true
	}
	defer func() {
		if hadStash {
			_ = h.worktreeMgr.GitRunInProject(projectID, "stash", "pop")
		}
	}()

	// Checkout origin branch in the project directory (force to handle dirty working tree)
	if err := h.worktreeMgr.GitRunInProject(projectID, "checkout", "-f", originBranch); err != nil {
		return fmt.Errorf("checkout origin branch %s: %w", originBranch, err)
	}

	// Merge the task branch
	mergeOut, mergeErr := h.worktreeMgr.GitRunInProjectOutput(projectID, "merge", taskBranch)
	if mergeErr == nil {
		log.Printf("[merge] task=%s merged into %s successfully: %s", taskID, originBranch, strings.TrimSpace(mergeOut))
		return nil
	}

	// Merge conflict detected — resolve with LLM
	log.Printf("[merge] task=%s conflict merging into %s, resolving with LLM", taskID, originBranch)
	conflictedFiles := h.getConflictedFiles(projectID)
	if len(conflictedFiles) == 0 {
		// Abort merge if no conflicted files found (shouldn't happen)
		_ = h.worktreeMgr.GitRunInProject(projectID, "merge", "--abort")
		return fmt.Errorf("merge conflict but no conflicted files found")
	}

	// Log conflict detection as system message
	fileList := strings.Join(conflictedFiles, ", ")
	h.logSystemMessage(taskID, task.CurrentThreadID, fmt.Sprintf("Merge conflict detected in: %s. Resolving with LLM...", fileList))

	// Resolve conflicts with LLM
	resolutionMsg, resolveErr := h.resolveMergeConflicts(projectID, taskID, task, conflictedFiles)
	if resolveErr != nil {
		log.Printf("[merge] task=%s LLM conflict resolution failed: %v", taskID, resolveErr)
		// Abort merge and return error
		_ = h.worktreeMgr.GitRunInProject(projectID, "merge", "--abort")
		return fmt.Errorf("LLM conflict resolution failed: %w", resolveErr)
	}

	// Stage resolved files and commit
	for _, f := range conflictedFiles {
		_ = h.worktreeMgr.GitRunInProject(projectID, "add", f)
	}
	commitMsg := fmt.Sprintf("Resolve merge conflicts for task: %s\n\n%s", task.Title, resolutionMsg)
	if err := h.worktreeMgr.GitRunInProject(projectID, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("commit resolved merge: %w", err)
	}

	log.Printf("[merge] task=%s conflicts resolved and committed", taskID)
	return nil
}

// getConflictedFiles returns a list of files with merge conflicts.
func (h *TaskHandler) getConflictedFiles(projectID string) []string {
	statusOut, err := h.worktreeMgr.GitRunInProjectOutput(projectID, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(statusOut, "\n") {
		f := strings.TrimSpace(line)
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}

// resolveMergeConflicts uses the LLM to resolve merge conflicts in the given files.
func (h *TaskHandler) resolveMergeConflicts(projectID, taskID string, task *types.Task, conflictedFiles []string) (string, error) {
	serverURL := task.LlamaServerURL
	modelName := task.ModelName
	if serverURL == "" || modelName == "" {
		return "", fmt.Errorf("no LLM config on task")
	}

	// Build conflict context for each file
	var conflictParts []string
	for _, f := range conflictedFiles {
		project, _ := h.store.GetProject(projectID)
		filePath := filepath.Join(project.Path, f)
		content, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("[merge] task=%s failed to read conflicted file %s: %v", taskID, f, err)
			continue
		}
		conflictParts = append(conflictParts, fmt.Sprintf("FILE: %s\n---BEGIN---\n%s\n---END---", f, string(content)))
	}

	if len(conflictParts) == 0 {
		return "", fmt.Errorf("no conflict content to resolve")
	}

	conflictContext := strings.Join(conflictParts, "\n\n")
	prompt := fmt.Sprintf(`You are resolving merge conflicts. The following files have merge conflict markers (<<<<<<< , =======, >>>>>>> ).

Task title: %s
Task description: %s

Conflicted files:

%s

For each file, provide the fully resolved content WITHOUT any conflict markers. Maintain the intent of the newer changes. If any functionality was lost during resolution, note it.

Output format: For each file, output:
RESOLVE <filepath>
<full resolved file content>
END_RESOLVE

Then output a summary:
SUMMARY
<Brief summary of changes made and any lost functionality>
END_SUMMARY`, task.Title, task.Prompt, conflictContext)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	messages := []types.ChatMessage{
		{Role: "system", Content: "You are an expert developer resolving git merge conflicts. Output the fully resolved file contents and a summary."},
		{Role: "user", Content: prompt},
	}

	_, response, err := h.executor.ChatCompletion(ctx, serverURL, modelName, messages)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	// Parse the response and write resolved files
	project, _ := h.store.GetProject(projectID)
	var summary string
	currentFile := ""
	var currentContent strings.Builder
	inResolve := false
	inSummary := false

	for _, line := range strings.Split(response, "\n") {
		if strings.HasPrefix(line, "RESOLVE ") {
			// Write previous file if any
			if currentFile != "" && inResolve {
				fullPath := filepath.Join(project.Path, currentFile)
				if err := os.WriteFile(fullPath, []byte(currentContent.String()), 0644); err != nil {
					log.Printf("[merge] task=%s failed to write resolved file %s: %v", taskID, currentFile, err)
				}
			}
			currentFile = strings.TrimPrefix(line, "RESOLVE ")
			currentContent.Reset()
			inResolve = true
			inSummary = false
			continue
		}
		if line == "END_RESOLVE" {
			// Write current file
			if currentFile != "" && inResolve {
				fullPath := filepath.Join(project.Path, currentFile)
				if err := os.WriteFile(fullPath, []byte(currentContent.String()), 0644); err != nil {
					log.Printf("[merge] task=%s failed to write resolved file %s: %v", taskID, currentFile, err)
				}
				inResolve = false
			}
			continue
		}
		if strings.HasPrefix(line, "SUMMARY") {
			inSummary = true
			inResolve = false
			continue
		}
		if line == "END_SUMMARY" {
			inSummary = false
			continue
		}
		if inResolve {
			currentContent.WriteString(line + "\n")
		} else if inSummary {
			if summary != "" {
				summary += "\n"
			}
			summary += line
		}
	}
	// Write last file if any
	if currentFile != "" && inResolve {
		fullPath := filepath.Join(project.Path, currentFile)
		if err := os.WriteFile(fullPath, []byte(currentContent.String()), 0644); err != nil {
			log.Printf("[merge] task=%s failed to write resolved file %s: %v", taskID, currentFile, err)
		}
	}

	if summary == "" {
		summary = "Conflicts resolved (no summary provided by LLM)"
	}

	// Log resolution as a merge_resolution message (persisted + broadcast)
	h.logMergeResolution(taskID, task.CurrentThreadID, conflictedFiles, summary)

	return summary, nil
}

// logSystemMessage creates a system message in the task's conversation thread.
func (h *TaskHandler) logSystemMessage(taskID, threadID, content string) {
	if threadID == "" {
		return
	}
	if err := h.store.CreateTaskMessage(&types.TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "system",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		log.Printf("[system-msg] task=%s failed to log message: %v", taskID, err)
	}
}

// logMergeResolution persists a merge_resolution message and broadcasts it to task stream clients.
func (h *TaskHandler) logMergeResolution(taskID, threadID string, conflictedFiles []string, summary string) {
	if threadID == "" {
		return
	}
	content := fmt.Sprintf("Merge conflicts resolved in %d file(s): %s\n\n%s",
		len(conflictedFiles), strings.Join(conflictedFiles, ", "), summary)
	msgID := uuid.New().String()
	if err := h.store.CreateTaskMessage(&types.TaskMessage{
		ID:        msgID,
		TaskID:    taskID,
		ThreadID:  threadID,
		Role:      "merge_resolution",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		log.Printf("[merge-msg] task=%s failed to log message: %v", taskID, err)
		return
	}
	// Broadcast to task stream clients for real-time display
	b := h.executor.GetOrCreateStream(taskID)
	b.Send(types.StreamEvent{Type: types.WSTypeMergeResolution, Data: map[string]interface{}{
		"id":              msgID,
		"conflicted_files": conflictedFiles,
		"summary":         summary,
		"content":         content,
		"created_at":      time.Now().UTC().Format(time.RFC3339),
	}})
}

// broadcastTaskDeleted sends a task_deleted event for the given task and all its descendants.
func (h *TaskHandler) broadcastTaskDeleted(projectID, taskID string) {
	if h.boardStreamMgr == nil {
		return
	}
	// Collect all descendants first
	descendants, _ := h.store.GetAllDescendants(taskID)
	for _, d := range descendants {
		h.boardStreamMgr.BroadcastTaskDeleted(projectID, d.ID)
	}
	h.boardStreamMgr.BroadcastTaskDeleted(projectID, taskID)
}

// cleanupTask performs all cleanup steps for a deleted task,
// including recursively cleaning up all descendant subtasks.
func (h *TaskHandler) cleanupTask(taskID string) {
	// Collect all descendants first (before any deletions)
	descendants, _ := h.store.GetAllDescendants(taskID)
	// Clean up descendants deep-first (reverse order ensures children before parents)
	for i := len(descendants) - 1; i >= 0; i-- {
		h.cleanupSingleTask(descendants[i].ID)
	}
	// Clean up the parent task last
	h.cleanupSingleTask(taskID)
}

// cleanupSingleTask performs cleanup for a single task (no recursion).
func (h *TaskHandler) cleanupSingleTask(taskID string) {
	if h.executor != nil {
		_ = h.executor.StopTask(taskID)
		h.executor.StopSandbox(taskID)
		h.executor.RemoveStream(taskID)
	}
	if h.worktreeMgr != nil {
		task, err := h.store.GetTaskByTaskID(taskID)
		if err == nil && task.Branch != "" {
			// Clean up stash entries belonging to this task's branch
			_ = h.worktreeMgr.DropStashEntriesForBranch(task.ProjectID, task.Branch)
			if err := h.worktreeMgr.RemoveWorktree(task.ProjectID, taskID, task.Branch); err != nil {
				log.Printf("task delete: worktree cleanup failed for %s: %v", taskID, err)
			}
		}
	}
	h.terminalMgr.RemoveSession(taskID)
	// Delete from database - find project first
	task, err := h.store.GetTaskByTaskID(taskID)
	if err == nil {
		_ = h.store.DeleteTask(task.ProjectID, taskID)
	}
}

// startTitleGeneration spawns a goroutine that uses the LLM to generate
// a concise title for the task. On success, it updates the task title.
// On failure or timeout, it keeps the existing (first-line) title.
func (h *TaskHandler) startTitleGeneration(projectID, taskID, prompt string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Get LLM config from the task itself
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil || task.LlamaServerURL == "" || task.ModelName == "" {
			log.Printf("[title-gen] task=%s skipping: no LLM config on task (%v)", taskID, err)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a title generator. Given a task description, generate a short, descriptive title (max 50 characters). Return only the title, nothing else. No quotes, no explanation."},
			{Role: "user", Content: prompt},
		}

		usage, title, err := h.executor.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, messages)
		if err != nil {
			log.Printf("[title-gen] task=%s LLM call failed: %v", taskID, err)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		if title == "" {
			log.Printf("[title-gen] task=%s empty title from LLM", taskID)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		// Store title generation usage as an invisible message (role='title')
		// so it appears in project stats but not in the chat UI.
		if usage != nil {
			if err := h.store.CreateTaskMessage(&types.TaskMessage{
				ID:              uuid.New().String(),
				TaskID:          taskID,
				ThreadID:        task.CurrentThreadID,
				Role:            "title",
				Content:         title,
				ModelName:       task.ModelName,
				ProviderName:    "title_generation",
				PromptTokens:    usage.PromptTokens,
				PredictedTokens: usage.CompletionTokens,
				CacheTokens:     usage.PromptTokensDetails.CachedTokens,
				PromptMs:        int(usage.PromptMs),
				PredictedMs:     int(usage.PredictedMs),
				CreatedAt:       time.Now().UTC(),
			}); err != nil {
				log.Printf("[title-gen] task=%s failed to store usage message: %v", taskID, err)
			}
		}

		// Update the task with the generated title
		task.Title = title
		task.TitleGenerating = false
		task.UpdatedAt = time.Now().UTC()
		if err := h.store.UpdateTask(task); err != nil {
			log.Printf("[title-gen] task=%s update failed: %v", taskID, err)
			return
		}

		log.Printf("[title-gen] task=%s title updated to: %q", taskID, title)
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, *task)
	}()
}

// finishTitleGeneration sets title_generating to false without changing the title.
func (h *TaskHandler) finishTitleGeneration(projectID, taskID string, broadcast bool) {
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		return
	}
	if !task.TitleGenerating {
		return // already finished
	}
	task.TitleGenerating = false
	task.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateTask(task); err != nil {
		log.Printf("[title-gen] task=%s finish update failed: %v", taskID, err)
		return
	}
	if broadcast {
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, *task)
	}
}

// HandleStartWithSubtasks is the HTTP handler for starting a task.
// If the task has incomplete subtasks, it uses the LLM to pick one and starts it.
// If the task has no subtasks, it starts normally.
func (h *TaskHandler) HandleStartWithSubtasks(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}

	// Check if task has children
	children, err := h.store.GetChildren(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("get children: %v", err))
		return
	}

	if len(children) > 0 {
		// Move all incomplete descendants (children, grandchildren, etc.) to in_progress
		h.moveAllDescendantsToInProgress(taskID)
		// Move the parent itself to in_progress
		task.Status = "in_progress"
		task.StatusState = "idle"
		task.UpdatedAt = time.Now().UTC()
		_ = h.store.UpdateTask(task)
		// Task has subtasks — use LLM to pick the best one
		selectedID := h.SelectSubtaskToStart(task, children)
		if selectedID == "" {
			// All subtasks are done — start the parent's own execution
			if err := h.executor.StartTask(projectID, taskID); err != nil {
				log.Printf("[start-parent] task=%s all subtasks done, start failed: %v", taskID, err)
				httputil.JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			task, _ = h.store.GetTask(projectID, taskID)
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			httputil.JSONResponse(w, http.StatusOK, task)
			return
		}
		log.Printf("[start-parent] task=%s selected subtask %s to start", taskID, selectedID)
		if err := h.StartSubtaskWithParent(projectID, selectedID); err != nil {
			log.Printf("[start-parent] task=%s error: %v", taskID, err)
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		startedTask, _ := h.store.GetTask(projectID, selectedID)
		if startedTask != nil {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, startedTask)
		}
		httputil.JSONResponse(w, http.StatusOK, startedTask)
		return
	}

	// No subtasks — start normally
	if err := h.executor.StartTask(projectID, taskID); err != nil {
		log.Printf("[start] task=%s error: %v", taskID, err)
		if strings.Contains(err.Error(), "already running") {
			httputil.JSONError(w, http.StatusConflict, err.Error())
		} else {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	log.Printf("[start] task=%s started", taskID)
	task, _ = h.store.GetTask(projectID, taskID)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStopWithCascade is the HTTP handler for stopping a task.
// If the task has running descendants, it stops them all.
func (h *TaskHandler) HandleStopWithCascade(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if task has descendants
	descendants, _ := h.store.GetAllDescendants(taskID)
	hasDescendants := len(descendants) > 0

	if hasDescendants {
		if err := h.StopTaskAndDescendants(projectID, taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, err.Error())
			return
		}
	} else {
		if err := h.executor.StopTask(taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, err.Error())
			return
		}
	}

	task, _ := h.store.GetTask(projectID, taskID)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStartSubtask explicitly starts a single subtask without moving the parent.
func (h *TaskHandler) HandleStartSubtask(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	task.Status = "in_progress"
	task.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.executor.StartTask(projectID, taskID); err != nil {
		log.Printf("[start-subtask] task=%s error: %v", taskID, err)
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, _ = h.store.GetTask(projectID, taskID)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// SelectSubtaskToStart delegates to the executor's subtask selection logic.
func (h *TaskHandler) SelectSubtaskToStart(parent *types.Task, subtasks []types.Task) string {
	return h.executor.SelectSubtaskToStart(parent, subtasks)
}

// StartSubtaskWithParent delegates to the executor.
func (h *TaskHandler) StartSubtaskWithParent(projectID, subtaskID string) error {
	return h.executor.StartSubtaskWithParent(projectID, subtaskID)
}

// StopTaskAndDescendants stops a task and all its running descendants.
func (h *TaskHandler) StopTaskAndDescendants(projectID, taskID string) error {
	// Get all descendants
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[stop-cascade] failed to get descendants of %s: %v", taskID, err)
	}

	// Stop all running descendants (leaf-first order: reverse the list)
	for i := len(descendants) - 1; i >= 0; i-- {
		d := descendants[i]
		if d.StatusState == "running" {
			if err := h.executor.StopTask(d.ID); err != nil {
				log.Printf("[stop-cascade] failed to stop descendant %s: %v", d.ID, err)
			}
			d.StatusState = "stopped"
			d.Output = "Stopped by parent"
			d.UpdatedAt = time.Now().UTC()
			_ = h.store.UpdateTask(&d)
		}
	}

	// Stop the task itself
	if err := h.executor.StopTask(taskID); err != nil {
		// Task might not be running (parent may be idle while subtask runs)
		task, _ := h.store.GetTask(projectID, taskID)
		if task != nil && task.StatusState == "running" {
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			task.UpdatedAt = time.Now().UTC()
			_ = h.store.UpdateTask(task)
		}
		return nil // executor not running is OK
	}

	task, _ := h.store.GetTask(projectID, taskID)
	if task != nil {
		task.StatusState = "stopped"
		task.Output = "Stopped by user"
		task.UpdatedAt = time.Now().UTC()
		_ = h.store.UpdateTask(task)
	}

	return nil
}

// isHex checks if a string contains only hexadecimal characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}


