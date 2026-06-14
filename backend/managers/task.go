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
	"kanban/backend/llama"
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

// HandleList lists or creates tasks for a project.
func (h *TaskHandler) HandleList(w http.ResponseWriter, r *http.Request, projectID string) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := h.store.GetTasksByProject(projectID)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
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
			if parent.Status != "backlog" {
				httputil.JSONError(w, http.StatusBadRequest, "parent task must be in backlog")
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
		if err := h.executor.StartTask(projectID, taskID); err != nil {
			log.Printf("[move] task=%s auto-start failed: %v", taskID, err)
			task.StatusState = "failed"
			task.Output = err.Error()
		}
	}
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.boardStreamMgr.Broadcast(projectID)
	httputil.JSONResponse(w, http.StatusOK, task)
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

	h.boardStreamMgr.Broadcast(projectID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// mergeWorktreeIntoOrigin merges the task's worktree branch into the project's current branch.
// On merge conflict, it uses the LLM to resolve conflicts.
func (h *TaskHandler) mergeWorktreeIntoOrigin(projectID, taskID string, task *types.Task) error {
	// Query the live current branch from the project's git repo
	originBranch, err := h.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		log.Printf("[merge] task=%s not a git repo, skipping merge: %v", taskID, err)
		return nil
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

	client := llama.NewLlamaClient(serverURL, modelName, "off")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	messages := []types.ChatMessage{
		{Role: "system", Content: "You are an expert developer resolving git merge conflicts. Output the fully resolved file contents and a summary."},
		{Role: "user", Content: prompt},
	}

	_, response, err := client.ChatCompletion(ctx, messages)
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

	// Log resolution as system message
	h.logSystemMessage(taskID, task.CurrentThreadID, fmt.Sprintf("Merge conflicts resolved:\n\n%s", summary))

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

// cleanupTask performs all cleanup steps for a deleted task.
func (h *TaskHandler) cleanupTask(taskID string) {
	if h.executor != nil {
		_ = h.executor.StopTask(taskID)
		h.executor.RemoveStream(taskID)
	}
	if h.worktreeMgr != nil {
		// Best-effort, find project from task
		task, err := h.store.GetTaskByTaskID(taskID)
		if err == nil {
			if err := h.worktreeMgr.RemoveWorktreeDir(task.ProjectID, taskID); err != nil {
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

		client := llama.NewLlamaClient(task.LlamaServerURL, task.ModelName, "off")
		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a title generator. Given a task description, generate a short, descriptive title (max 50 characters). Return only the title, nothing else. No quotes, no explanation."},
			{Role: "user", Content: prompt},
		}

		usage, title, err := client.ChatCompletion(ctx, messages)
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
