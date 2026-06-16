package managers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

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
}

// NewTaskHandler creates a new TaskHandler.
func NewTaskHandler(store *store.Store, boardStreamMgr *stream.BoardStreamManager, configStore *config.ConfigStore, terminalMgr *term.TerminalManager, executor *exec.TaskExecutor, worktreeMgr *git.WorktreeManager) *TaskHandler {
	return &TaskHandler{store: store, boardStreamMgr: boardStreamMgr, configStore: configStore, terminalMgr: terminalMgr, executor: executor, worktreeMgr: worktreeMgr}
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
			_, err := h.store.GetTask(projectID, task.ParentTaskID)
			if err != nil {
				httputil.JSONError(w, http.StatusBadRequest, "parent task not found")
				return
			}
			// Prevent self-reference (won't happen for new tasks, but guard anyway)
			if task.ParentTaskID == task.ID {
				httputil.JSONError(w, http.StatusBadRequest, "task cannot be its own parent")
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
		defaultThreadID := GenerateTaskID()
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
		h.boardStreamMgr.Broadcast(projectID)
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
		h.boardStreamMgr.Broadcast(projectID)
		httputil.JSONResponse(w, http.StatusOK, task)
	case http.MethodDelete:
		if _, err := h.store.GetTask(projectID, taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		h.cleanupTask(taskID)
		h.boardStreamMgr.Broadcast(projectID)
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
		deleted++
	}

	h.boardStreamMgr.Broadcast(projectID)
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
	task.Status = req.Status
	task.UpdatedAt = time.Now()

	if req.Status == "done" {
		task.StatusState = "completed"
	} else if task.StatusState == "completed" {
		// Task moved away from Done — reset status so it can run again
		task.StatusState = "idle"
	}
	if req.Status == "in_progress" && task.StatusState != "running" {
		// If this task is a subtask, use subtask-aware start (moves parent + siblings)
		if task.ParentTaskID != "" {
			if err := h.StartSubtaskWithParent(projectID, taskID); err != nil {
				log.Printf("[move] task=%s subtask start failed: %v", taskID, err)
				task.StatusState = "failed"
				task.Output = err.Error()
			}
		} else {
			// Check if task has subtasks — if so, use LLM to pick one
			children, _ := h.store.GetChildren(taskID)
			if len(children) > 0 {
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
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID)
	}

	h.boardStreamMgr.Broadcast(projectID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// autoStartNextSibling finds the next incomplete sibling of the given task and starts it.
// Only runs if the parent is in_progress and not paused.
func (h *TaskHandler) autoStartNextSibling(projectID, finishedTaskID, parentTaskID string) error {
	parent, err := h.store.GetTask(projectID, parentTaskID)
	if err != nil {
		return fmt.Errorf("get parent task: %w", err)
	}
	// Only auto-start if parent is actively in progress (not paused, not done)
	if parent.Status != "in_progress" || parent.StatusState == "paused" {
		return nil
	}

	children, err := h.store.GetChildren(parentTaskID)
	if err != nil {
		return fmt.Errorf("get children: %w", err)
	}

	// Find incomplete siblings (excluding the task that just finished)
	var incomplete []types.Task
	for _, c := range children {
		if c.ID != finishedTaskID && c.Status != "done" {
			incomplete = append(incomplete, c)
		}
	}
	if len(incomplete) == 0 {
		return nil
	}

	// Select the deepest incomplete subtask
	selectedID := h.SelectSubtaskToStart(parent, incomplete)
	if selectedID == "" {
		return nil
	}

	log.Printf("[auto-start] task=%s finished, starting next sibling %s (parent=%s)", finishedTaskID, selectedID, parentTaskID)
	return h.StartSubtaskWithParent(projectID, selectedID)
}

// HandleCommitAndDone commits the worktree changes, merges into the origin branch, and moves the task to Done.
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

	// Try to commit worktree changes (skip if worktree doesn't exist)
	diffStat, wtErr := h.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat")
	worktreeExists := wtErr == nil
	if worktreeExists {
		// Worktree exists — stage and commit
		_ = h.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
		commitMsg := task.Title
		if strings.TrimSpace(diffStat) != "" {
			commitMsg += "\n\n" + strings.TrimSpace(diffStat)
		}
		_ = h.worktreeMgr.GitRunInWorktree(taskID, "commit", "-m", commitMsg, "--allow-empty")
	}

	// Merge worktree into origin branch
	mergeErr := h.mergeWorktreeIntoOrigin(projectID, taskID, task)
	if mergeErr != nil {
		log.Printf("[commit-and-done] task=%s merge failed: %v", taskID, mergeErr)
		// Log merge error as system message in the task thread
		h.logSystemMessage(taskID, task.CurrentThreadID, fmt.Sprintf("Merge failed: %v", mergeErr))
		// Continue to move task to done even if merge fails
	}

	// Clean up worktree after merge attempt
	if worktreeExists {
		_ = h.worktreeMgr.RemoveWorktreeDir(projectID, taskID)
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
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID)
	}

	h.boardStreamMgr.Broadcast(projectID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// mergeWorktreeIntoOrigin merges the task's worktree branch into the project's current branch.
// On merge conflict, it uses the LLM to resolve conflicts.
func (h *TaskHandler) mergeWorktreeIntoOrigin(projectID, taskID string, task *types.Task) error {
	// Use the base ref captured at worktree creation time.
	// Fall back to querying live HEAD for tasks created before this field was added.
	originBranch := task.BaseRef
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

// cleanupTask performs all cleanup steps for a deleted task.
func (h *TaskHandler) cleanupTask(taskID string) {
	if h.executor != nil {
		_ = h.executor.StopTask(taskID)
		h.executor.RemoveStream(taskID)
	}
	if h.worktreeMgr != nil {
		// Best-effort, find project from task
		task, err := h.store.GetTaskByTaskID(taskID)
		if err == nil && task.Branch != "" {
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
		// Task has subtasks — use LLM to pick the best one
		selectedID := h.SelectSubtaskToStart(task, children)
		if selectedID == "" {
			// All subtasks are done — mark parent as done
			task.Status = "done"
			task.StatusState = "completed"
			task.UpdatedAt = time.Now().UTC()
			_ = h.store.UpdateTask(task)
			h.boardStreamMgr.Broadcast(projectID)
			httputil.JSONResponse(w, http.StatusOK, task)
			return
		}
		log.Printf("[start-parent] task=%s selected subtask %s to start", taskID, selectedID)
		if err := h.StartSubtaskWithParent(projectID, selectedID); err != nil {
			log.Printf("[start-parent] task=%s error: %v", taskID, err)
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.boardStreamMgr.Broadcast(projectID)
		startedTask, _ := h.store.GetTask(projectID, selectedID)
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
	h.boardStreamMgr.Broadcast(projectID)
	task, _ = h.store.GetTask(projectID, taskID)
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

	h.boardStreamMgr.Broadcast(projectID)
	task, _ := h.store.GetTask(projectID, taskID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStartSubtask explicitly starts a subtask, moving parent + siblings to in_progress.
func (h *TaskHandler) HandleStartSubtask(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.StartSubtaskWithParent(projectID, taskID); err != nil {
		log.Printf("[start-subtask] task=%s error: %v", taskID, err)
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.boardStreamMgr.Broadcast(projectID)
	task, _ := h.store.GetTask(projectID, taskID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// findDeepestIncompleteSubtasks recursively finds all incomplete subtasks at the deepest level.
// A subtask is at the deepest level if it is incomplete and has no incomplete children of its own.
func (h *TaskHandler) findDeepestIncompleteSubtasks(projectID string, tasks []types.Task) []types.Task {
	var incomplete []types.Task
	for _, t := range tasks {
		if t.Status != "done" {
			incomplete = append(incomplete, t)
		}
	}
	if len(incomplete) == 0 {
		return nil
	}

	// Check if any incomplete subtask has its own incomplete children
	var hasNestedIncomplete []types.Task
	for _, t := range incomplete {
		children, err := h.store.GetChildren(t.ID)
		if err != nil {
			continue
		}
		var childIncomplete []types.Task
		for _, c := range children {
			if c.Status != "done" {
				childIncomplete = append(childIncomplete, c)
			}
		}
		if len(childIncomplete) > 0 {
			hasNestedIncomplete = append(hasNestedIncomplete, t)
		}
	}

	// If none have incomplete children, these are the deepest
	if len(hasNestedIncomplete) == 0 {
		return incomplete
	}

	// Otherwise, recurse into those that have incomplete children
	var deepest []types.Task
	for _, t := range hasNestedIncomplete {
		children, _ := h.store.GetChildren(t.ID)
		deepest = append(deepest, h.findDeepestIncompleteSubtasks(projectID, children)...)
	}
	return deepest
}

// SelectSubtaskToStart finds the deepest incomplete subtask(s) and uses the LLM to pick one if there are multiple.
// Returns the ID of the selected subtask, or empty string if all are done.
func (h *TaskHandler) SelectSubtaskToStart(parent *types.Task, subtasks []types.Task) string {
	// Find the deepest incomplete subtasks (those with no incomplete children)
	deepest := h.findDeepestIncompleteSubtasks(parent.ProjectID, subtasks)
	if len(deepest) == 0 {
		return ""
	}
	if len(deepest) == 1 {
		return deepest[0].ID
	}

	// Multiple deepest subtasks — use LLM to pick
	var subtaskParts []string
	for i, s := range deepest {
		promptPreview := s.Prompt
		if len(promptPreview) > 300 {
			promptPreview = promptPreview[:300] + "..."
		}
		subtaskParts = append(subtaskParts, fmt.Sprintf("Subtask %d (ID: %s):\n  Title: %s\n  Description: %s",
			i+1, s.ID, s.Title, promptPreview))
	}

	prompt := fmt.Sprintf(`You are a task scheduler. Given a parent task and its deepest-level incomplete subtasks, select the ONE subtask that should be started first. Consider dependencies, complexity, and logical ordering.

Parent task:
  Title: %s
  Description: %s

Incomplete subtasks (deepest level):
%s

Return ONLY the subtask ID (e.g. "frost-amber-quartz"), nothing else.`,
		parent.Title, parent.Prompt[:minLen(len(parent.Prompt), 500)], strings.Join(subtaskParts, "\n\n"))

	serverURL := parent.LlamaServerURL
	modelName := parent.ModelName
	if serverURL == "" || modelName == "" {
		log.Printf("[subtask-select] task=%s no LLM config, using fallback", parent.ID)
		return deepest[0].ID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	messages := []types.ChatMessage{
		{Role: "system", Content: "You are a task scheduler. Return only a task ID."},
		{Role: "user", Content: prompt},
	}

	_, response, err := h.executor.ChatCompletion(ctx, serverURL, modelName, messages)
	if err != nil {
		log.Printf("[subtask-select] task=%s LLM call failed: %v, using fallback", parent.ID, err)
		return deepest[0].ID
	}

	selectedID := strings.TrimSpace(response)
	// Validate the returned ID matches one of the deepest subtasks
	for _, s := range deepest {
		if s.ID == selectedID {
			log.Printf("[subtask-select] task=%s LLM selected subtask %s", parent.ID, selectedID)
			return selectedID
		}
	}

	log.Printf("[subtask-select] task=%s LLM returned invalid ID %q, using fallback", parent.ID, selectedID)
	return deepest[0].ID
}

// StartSubtaskWithParent moves the parent and all sibling subtasks to in_progress,
// then starts the executor for the specified subtask only.
func (h *TaskHandler) StartSubtaskWithParent(projectID, subtaskID string) error {
	subtask, err := h.store.GetTask(projectID, subtaskID)
	if err != nil {
		return fmt.Errorf("get subtask: %w", err)
	}
	if subtask.ParentTaskID == "" {
		return fmt.Errorf("task has no parent")
	}

	parent, err := h.store.GetTask(projectID, subtask.ParentTaskID)
	if err != nil {
		return fmt.Errorf("get parent: %w", err)
	}

	// Move parent to in_progress (no auto-start)
	parent.Status = "in_progress"
	parent.StatusState = "idle"
	parent.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateTask(parent); err != nil {
		return fmt.Errorf("update parent: %w", err)
	}

	// Move all sibling subtasks to in_progress (no auto-start)
	siblings, err := h.store.GetChildren(parent.ID)
	if err != nil {
		return fmt.Errorf("get siblings: %w", err)
	}
	for _, sibling := range siblings {
		if sibling.ID == subtaskID {
			continue // the target subtask is handled below
		}
		sibling.Status = "in_progress"
		sibling.StatusState = "idle"
		sibling.UpdatedAt = time.Now().UTC()
		if err := h.store.UpdateTask(&sibling); err != nil {
			log.Printf("[start-subtask] failed to update sibling %s: %v", sibling.ID, err)
		}
	}

	h.boardStreamMgr.Broadcast(projectID)

	// Start the subtask executor
	return h.executor.StartTask(projectID, subtaskID)
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

// CheckParentCompletion checks if all subtasks of a task are done.
// If so, moves the parent to done (recursively for nested parents).
func (h *TaskHandler) CheckParentCompletion(projectID, taskID string) {
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil || task.ParentTaskID == "" {
		return
	}

	parent, err := h.store.GetTask(projectID, task.ParentTaskID)
	if err != nil {
		return
	}

	children, err := h.store.GetChildren(parent.ID)
	if err != nil {
		return
	}
	if len(children) == 0 {
		return
	}

	// Check if all children are done
	allDone := true
	for _, child := range children {
		if child.Status != "done" {
			allDone = false
			break
		}
	}
	if !allDone {
		return
	}

	// All children done — move parent to done
	parent.Status = "done"
	parent.StatusState = "completed"
	parent.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateTask(parent); err != nil {
		log.Printf("[parent-completion] failed to mark parent %s as done: %v", parent.ID, err)
		return
	}
	log.Printf("[parent-completion] task=%s all subtasks done, marking parent %s as done", taskID, parent.ID)
	h.boardStreamMgr.Broadcast(projectID)

	// Recursively check grandparent
	h.CheckParentCompletion(projectID, parent.ID)
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
