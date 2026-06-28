package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/llama"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// TaskExecutor handles task execution endpoints and orchestrates AI task execution.
type TaskExecutor struct {
	store        *store.Store
	configStore  *config.ConfigStore
	appStreamMgr *stream.AppStreamManager
	worktreeMgr  *git.WorktreeManager
	sandboxMgr   *sandbox.SandboxManager
	gate         *ConcurrencyGate
	taskMu       sync.Mutex
	tasks        map[string]*executionState
}

type executionState struct {
	ctx         context.Context
	cancel      context.CancelFunc
	userMsgChan chan stream.UserMessage
	createdIDs  []string // task IDs created by create_task during this execution
}

// modelConfig holds mutable model/provider settings for an executor.
type modelConfig struct {
	mu        sync.Mutex
	serverURL string
	modelName string
}

func (mc *modelConfig) get() (string, string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.serverURL, mc.modelName
}

func (mc *modelConfig) set(serverURL, modelName string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.serverURL = serverURL
	mc.modelName = modelName
}

// NewTaskExecutor creates a new TaskExecutor.
func NewTaskExecutor(store *store.Store, configStore *config.ConfigStore, appStreamMgr *stream.AppStreamManager, worktreeMgr *git.WorktreeManager, sandboxMgr *sandbox.SandboxManager) *TaskExecutor {
	te := &TaskExecutor{
		store:        store,
		configStore:  configStore,
		appStreamMgr: appStreamMgr,
		worktreeMgr:  worktreeMgr,
		sandboxMgr:   sandboxMgr,
		gate:         NewConcurrencyGate(),
		tasks:        make(map[string]*executionState),
	}
	te.appStreamMgr.SetLazyStartHandler(te.handleLazyStart)
	te.appStreamMgr.SetUserMessageHandler(func(um stream.AppStreamUserMessage) {
		te.forwardUserMessage(um.ProjectID, um.TaskID, stream.UserMessage{
			Content:     um.Content,
			ModelName:   um.ModelName,
			ServerURL:   um.ServerURL,
			Attachments: um.Attachments,
		})
	})
	return te
}

// HandleStart starts a task's AI execution.
func (te *TaskExecutor) HandleStart(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Printf("[start] task=%s project=%s", taskID, projectID)
	if err := te.StartTask(projectID, taskID); err != nil {
		log.Printf("[start] task=%s error: %v", taskID, err)
		if strings.Contains(err.Error(), "already running") {
			httputil.JSONError(w, http.StatusConflict, err.Error())
		} else {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	log.Printf("[start] task=%s started", taskID)
	task, _ := te.store.GetTask(projectID, taskID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStop stops a running task.
func (te *TaskExecutor) HandleStop(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.StopTask(taskID); err != nil {
		httputil.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	task, _ := te.store.GetTask(projectID, taskID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleRetry retries a failed task.
func (te *TaskExecutor) HandleRetry(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.RetryTask(projectID, taskID); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, _ := te.store.GetTask(projectID, taskID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleResume resumes a paused task (same as retry for now).
func (te *TaskExecutor) HandleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	te.HandleRetry(w, r)
}

// HandleSend accepts a user message via HTTP POST and triggers task execution.
// Returns the created message on success.
func (te *TaskExecutor) HandleSend(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Content        string `json:"content"`
		ModelName      string `json:"model_name"`
		LlamaServerURL string `json:"llama_server_url"`
		Attachments    string `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content == "" {
		httputil.JSONError(w, http.StatusBadRequest, "content is required")
		return
	}

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	threadID := task.CurrentThreadID

	// Move task to in_progress if it's in review or done
	if task.Status == "review" || task.Status == "done" {
		task.Status = "in_progress"
		task.StatusState = "idle"
		task.UpdatedAt = time.Now().UTC()
		_ = te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(task.ProjectID, task)
	}

	// Check for duplicate (same content as last user message in thread)
	msgID := uuid.New().String()
	existingID, lookupErr := te.store.GetLastUserMessageInThread(threadID)
	isDuplicate := false
	if lookupErr == nil && existingID != "" {
		existingMsgs, _ := te.store.GetTaskMessages(taskID, threadID)
		for i := len(existingMsgs) - 1; i >= 0; i-- {
			if existingMsgs[i].ID == existingID {
				if existingMsgs[i].Content == req.Content {
					isDuplicate = true
					msgID = existingID
				}
				break
			}
		}
	}

	if !isDuplicate {
		tm := &types.TaskMessage{
			ID:        msgID,
			TaskID:    taskID,
			ThreadID:  threadID,
			Role:      "user",
			Content:   req.Content,
			CreatedAt: time.Now().UTC(),
		}
		if req.Attachments != "" {
			tm.Attachments = req.Attachments
		}
		te.store.CreateTaskMessage(tm)
	}

	// Broadcast echo to WS clients
	echoData := map[string]any{"id": msgID, "role": "user", "content": req.Content}
	if threadID != "" {
		echoData["thread_id"] = threadID
	}
	if req.Attachments != "" {
		var atts []any
		if err := json.Unmarshal([]byte(req.Attachments), &atts); err == nil && len(atts) > 0 {
			echoData["attachments"] = atts
		}
	}
	// Broadcast user_message echo to task subscribers
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeUserMessage, Data: echoData})

	// Forward to executor or lazy-start
	te.forwardUserMessage(projectID, taskID, stream.UserMessage{
		Content:     req.Content,
		ModelName:   req.ModelName,
		ServerURL:   req.LlamaServerURL,
		Attachments: req.Attachments,
	})

	// Return the message
	result := map[string]any{
		"id":      msgID,
		"role":    "user",
		"content": req.Content,
	}
	if threadID != "" {
		result["thread_id"] = threadID
	}
	httputil.JSONResponse(w, http.StatusCreated, result)
}

// WorktreeMgr returns the WorktreeManager. Used by server wiring.
func (te *TaskExecutor) WorktreeMgr() *git.WorktreeManager {
	return te.worktreeMgr
}

// RunWithGate executes fn while holding a concurrency gate slot for the given provider.
// Blocks until a slot is available, respecting per-provider concurrency limits.
// Queueing is transparent to the caller — if the gate can't be acquired (e.g. context
// timeout), the function is not run and the gate error is returned. Any error from fn
// is returned directly. The caller treats both cases identically.
func (te *TaskExecutor) RunWithGate(ctx context.Context, serverURL string, fn func() error) error {
	concurrency := te.configStore.GetProviderConcurrency(serverURL)
	if _, err := te.gate.Acquire(ctx, serverURL, concurrency); err != nil {
		return err
	}
	defer te.gate.Release(serverURL)
	return fn()
}

// ChatCompletion sends a non-streaming chat request through the concurrency gate.
// The caller passes messages and gets a result — queue management is fully transparent.
func (te *TaskExecutor) ChatCompletion(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage) (*types.ChatUsage, string, error) {
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, "off", apiKey)
	var usage *types.ChatUsage
	var text string
	err := te.RunWithGate(ctx, serverURL, func() error {
		var err error
		usage, text, err = client.ChatCompletion(ctx, messages)
		return err
	})
	return usage, text, err
}

// ResetForTest stops all running executors and clears state.
func (te *TaskExecutor) ResetForTest() {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	for taskID, state := range te.tasks {
		state.cancel()
		delete(te.tasks, taskID)
	}
	te.appStreamMgr.ResetForTest()
}

// -- Subtask auto-start methods --

// findDeepestIncompleteSubtasks recursively finds all incomplete subtasks at the deepest level.
// A subtask is at the deepest level if it is incomplete and has no incomplete children of its own.
func (te *TaskExecutor) FindDeepestIncompleteSubtasks(tasks []types.Task) []types.Task {
	var incomplete []types.Task
	for _, t := range tasks {
		if isIncompleteForStart(t) {
			incomplete = append(incomplete, t)
		}
	}
	if len(incomplete) == 0 {
		return nil
	}

	// Check if any incomplete subtask has its own incomplete children
	var hasNestedIncomplete []types.Task
	for _, t := range incomplete {
		children, err := te.store.GetChildren(t.ID)
		if err != nil {
			continue
		}
		var childIncomplete []types.Task
		for _, c := range children {
			if isIncompleteForStart(c) {
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
		children, _ := te.store.GetChildren(t.ID)
		deepest = append(deepest, te.FindDeepestIncompleteSubtasks(children)...)
	}
	return deepest
}

// isIncompleteForStart returns true if a task is a candidate for auto-start.
// Only backlog or in_progress+idle tasks qualify.
func isIncompleteForStart(t types.Task) bool {
	if t.Status == "backlog" {
		return true
	}
	if t.Status == "in_progress" && t.StatusState == "idle" {
		return true
	}
	return false
}

// selectSubtaskToStart finds the deepest incomplete subtask(s) and uses the LLM to pick one if there are multiple.
// Returns the ID of the selected subtask, or empty string if all are done.
func (te *TaskExecutor) SelectSubtaskToStart(parent *types.Task, subtasks []types.Task) string {
	deepest := te.FindDeepestIncompleteSubtasks(subtasks)
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

	promptPreview := parent.Prompt
	if len(promptPreview) > 500 {
		promptPreview = promptPreview[:500]
	}
	prompt := fmt.Sprintf(`You are a task scheduler. Given a parent task and its deepest-level incomplete subtasks, select the ONE subtask that should be started first. Consider dependencies, complexity, and logical ordering.

Parent task:
  Title: %s
  Description: %s

Incomplete subtasks (deepest level):
%s

Return ONLY the subtask ID (e.g. "frost-amber-quartz"), nothing else.`,
		parent.Title, promptPreview, strings.Join(subtaskParts, "\n\n"))

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

	_, response, err := te.ChatCompletion(ctx, serverURL, modelName, messages)
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

// StartSubtaskWithParent moves the parent to in_progress and starts the executor
// for the specified subtask only. Siblings are NOT moved — that is handled by
// HandleMove when a parent task (not an individual subtask) is moved.
func (te *TaskExecutor) StartSubtaskWithParent(projectID, subtaskID string) error {
	subtask, err := te.store.GetTaskByTaskID(subtaskID)
	if err != nil {
		return fmt.Errorf("get subtask: %w", err)
	}
	if subtask.ParentTaskID == "" {
		return fmt.Errorf("task has no parent")
	}

	parent, err := te.store.GetTaskByTaskID(subtask.ParentTaskID)
	if err != nil {
		return fmt.Errorf("get parent: %w", err)
	}

	// Move parent to in_progress (no auto-start)
	parent.Status = "in_progress"
	parent.StatusState = "idle"
	parent.UpdatedAt = time.Now().UTC()
	if err := te.store.UpdateTask(parent); err != nil {
		return fmt.Errorf("update parent: %w", err)
	}

	te.appStreamMgr.BroadcastTaskUpdated(projectID, parent)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, subtask)

	// Start the subtask executor

	// Start the subtask executor
	return te.StartTask(projectID, subtaskID)
}

// autoStartNextSibling finds the next incomplete sibling of the given task and starts it.
// Only runs if the parent is in_progress and not paused.
// When all siblings are done and finishedStatus is "done", recurses up the ancestor chain.
// Ancestors in "backlog" are never auto-started (they are their own tasks).
func (te *TaskExecutor) AutoStartNextSibling(projectID, finishedTaskID, parentTaskID, finishedStatus string) error {
	parent, err := te.store.GetTaskByTaskID(parentTaskID)
	if err != nil {
		return fmt.Errorf("get parent task: %w", err)
	}
	// Only auto-start if parent is actively in progress (not paused, not done)
	if parent.Status != "in_progress" || parent.StatusState == "paused" {
		return nil
	}

	children, err := te.store.GetChildren(parent.ID)
	if err != nil {
		return fmt.Errorf("get children: %w", err)
	}

	// Find incomplete siblings (excluding the task that just finished)
	var incomplete []types.Task
	for _, c := range children {
		if c.ID == finishedTaskID {
			continue
		}
		if isIncompleteForStart(c) {
			incomplete = append(incomplete, c)
		}
	}
	if len(incomplete) == 0 {
		// No incomplete siblings at this level.
		// Only recurse up if ALL children are in Done (parent completes when all done).
		allDone := true
		for _, c := range children {
			if c.Status != "done" {
				allDone = false
				break
			}
		}
		if !allDone {
			// Some children still in review/in_progress — parent stays.
			// But recurse up to check if the parent itself has incomplete siblings to start.
			if parent.ParentTaskID == "" {
				return nil // root parent, no more levels
			}
			grandparent, err := te.store.GetTaskByTaskID(parent.ParentTaskID)
			if err != nil {
				return nil
			}
			if grandparent.Status == "backlog" {
				log.Printf("[auto-start] ancestor %s is in backlog, skipping auto-start", grandparent.ID)
				return nil
			}
			log.Printf("[auto-start] task=%s no incomplete siblings, parent %s not all done, recursing up to %s", finishedTaskID, parent.ID, parent.ParentTaskID)
			return te.AutoStartNextSibling(projectID, parent.ID, parent.ParentTaskID, finishedStatus)
		}
		// All children done — start the parent's own execution.
		// When the parent was first started, only children ran. Now it gets its turn.
		log.Printf("[auto-start] task=%s all children done, starting parent %s", finishedTaskID, parent.ID)
		if err := te.StartTask(projectID, parent.ID); err != nil {
			log.Printf("[auto-start] task=%s failed to start parent %s: %v", finishedTaskID, parent.ID, err)
		} else {
			started, _ := te.store.GetTaskByTaskID(parent.ID)
			if started != nil {
				te.appStreamMgr.BroadcastTaskUpdated(projectID, started)
			}
		}
		return nil
	}

	// Select the deepest incomplete subtask
	selectedID := te.SelectSubtaskToStart(parent, incomplete)
	if selectedID == "" {
		return nil
	}

	log.Printf("[auto-start] task=%s finished, starting next sibling %s (parent=%s)", finishedTaskID, selectedID, parentTaskID)
	return te.StartSubtaskWithParent(projectID, selectedID)
}

// taskExecSetup holds the shared state prepared for task execution.
type taskExecSetup struct {
	task               *types.Task
	project            *types.Project
	worktreePath       string
	serverURL          string
	modelName          string
	ctx                context.Context
	cancel             context.CancelFunc
	userMsgChan        chan stream.UserMessage
	sandboxEnabled     bool
	networkIsolation   bool
	baseImage          string
	sandboxDockerfilePath string
	memoryLimit        int
}

// prepareTaskExecution sets up the shared infrastructure for task execution.
// Both StartTask and handleLazyStart use this to avoid duplicating worktree
// creation, branch watching, status updates, and sandbox configuration.
// Returns nil if the task is already actively running (caller should check execSetup == nil).
func (te *TaskExecutor) prepareTaskExecution(projectID, taskID string, overrideServerURL, overrideModelName string) (*taskExecSetup, error) {
	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil {
		return nil, err
	}

	// Reset stale running state (executor crashed/server restarted)
	if task.StatusState == "running" {
		te.taskMu.Lock()
		_, activelyRunning := te.tasks[taskID]
		te.taskMu.Unlock()
		if activelyRunning {
			return nil, fmt.Errorf("task already running")
		}
		log.Printf("[start] task=%s stale running state detected, resetting", taskID)
		task.StatusState = "idle"
	}

	serverURL := overrideServerURL
	if serverURL == "" {
		serverURL = task.LlamaServerURL
	}
	if serverURL == "" {
		return nil, fmt.Errorf("llama server URL not configured")
	}

	modelName := overrideModelName
	if modelName == "" {
		modelName = task.ModelName
	}

	// Generate branch name: use slugified title for separate branches, task ID for merge-to-main
	branchName := fmt.Sprintf("task/%s", task.ID)
	if !task.MergeOnCommit && task.Title != "" {
		branchName = "task/" + slugifyBranchName(task.Title)
		// Ensure uniqueness: append task ID suffix if branch already exists
		if te.branchExists(projectID, branchName) {
			branchName = branchName + "-" + taskID[:8]
		}
	}
	worktreePath, baseRef, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName)
	if err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	task.Branch = branchName
	task.BaseRef = baseRef
	te.appStreamMgr.WatchWorktree(taskID, projectID, baseRef)

	// If the task has a current thread with a specific git branch, checkout that branch
	if task.CurrentThreadID != "" {
		thread, terr := te.store.GetThread(task.CurrentThreadID)
		if terr == nil && thread.GitBranch != "" && thread.GitBranch != branchName {
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "checkout", "-B", thread.GitBranch)
			task.Branch = thread.GitBranch
		}
	}

	task.StatusState = "running"
	task.Status = "in_progress"
	if err := te.store.UpdateTask(task); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	userMsgChan := make(chan stream.UserMessage, 16)
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan}
	te.taskMu.Unlock()

	project, err := te.store.GetProject(projectID)
	if err != nil {
		cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		return nil, fmt.Errorf("get project for sandbox check: %w", err)
	}

	return &taskExecSetup{
		task:                task,
		project:             project,
		worktreePath:        worktreePath,
		serverURL:           serverURL,
		modelName:           modelName,
		ctx:                 ctx,
		cancel:              cancel,
		userMsgChan:         userMsgChan,
		sandboxEnabled:      project.SandboxEnabled,
		networkIsolation:    project.SandboxNetworkIsolation,
		baseImage:           project.SandboxImage,
		sandboxDockerfilePath: project.SandboxDockerfilePath,
		memoryLimit:         project.SandboxMemoryLimit,
	}, nil
}

// StartTask begins AI execution for a task.
func (te *TaskExecutor) StartTask(projectID, taskID string) error {
	execSetup, err := te.prepareTaskExecution(projectID, taskID, "", "")
	if err != nil {
		return err
	}
	task := execSetup.task

	existingMsgs, _ := te.store.GetTaskMessages(taskID, task.CurrentThreadID)
	if len(existingMsgs) == 0 {
		_ = te.store.CreateTaskMessage(&types.TaskMessage{
			ID:        uuid.New().String(),
			TaskID:    taskID,
			ThreadID:  task.CurrentThreadID,
			Role:      "user",
			Content:   task.Prompt,
			CreatedAt: time.Now().UTC(),
		})
	}

	// If the last message is already from the assistant, just update status without re-triggering the LLM
	if len(existingMsgs) > 0 && existingMsgs[len(existingMsgs)-1].Role == "assistant" {
		log.Printf("[start] task=%s last message is from assistant, skipping LLM call", taskID)
		execSetup.cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		return nil
	}

	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})

	go te.executeTask(execSetup.ctx, task, projectID, execSetup.project.Name, execSetup.worktreePath, execSetup.serverURL, execSetup.modelName, execSetup.userMsgChan, execSetup.sandboxEnabled, execSetup.networkIsolation, execSetup.baseImage, execSetup.sandboxDockerfilePath, execSetup.memoryLimit)
	return nil
}
func (te *TaskExecutor) StopTask(taskID string) error {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	state, ok := te.tasks[taskID]
	if !ok {
		return fmt.Errorf("task not running")
	}
	state.cancel()
	delete(te.tasks, taskID)
	return nil
}

// forwardUserMessage forwards a user message to the running executor or lazy-starts one.
func (te *TaskExecutor) forwardUserMessage(projectID, taskID string, um stream.UserMessage) {
	te.taskMu.Lock()
	state, exists := te.tasks[taskID]
	if exists {
		select {
		case state.userMsgChan <- um:
		default:
			log.Printf("[user-msg] task=%s channel full, dropping message", taskID)
		}
		te.taskMu.Unlock()
		return
	}
	te.taskMu.Unlock()
	go te.handleLazyStart(projectID, taskID, um.Content, um.ModelName, um.ServerURL)
}

// StopSandbox removes the sandbox container for a task (called on task deletion).
func (te *TaskExecutor) StopSandbox(taskID string) {
	if te.sandboxMgr != nil {
		if err := te.sandboxMgr.Stop(taskID); err != nil {
			log.Printf("[sandbox] task=%s stop error: %v", taskID, err)
		}
	}
}

// RetryTask retries a failed task.
func (te *TaskExecutor) RetryTask(projectID, taskID string) error {
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return err
	}
	task.StatusState = "idle"
	te.store.UpdateTask(task)
	return te.StartTask(projectID, taskID)
}

// handleLazyStart is called when a user message arrives for a task with no active executor.
func (te *TaskExecutor) handleLazyStart(projectID, taskID, content, modelName, serverURL string) {
	log.Printf("[lazy-start] task=%s project=%s (override: model=%s, url=%s)", taskID, projectID, modelName, serverURL)

	te.taskMu.Lock()
	if _, exists := te.tasks[taskID]; exists {
		te.taskMu.Unlock()
		return
	}
	te.taskMu.Unlock()

	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil {
		log.Printf("[lazy-start] task=%s get error: %v", taskID, err)
		return
	}

	execSetup, err := te.prepareTaskExecution(projectID, taskID, serverURL, modelName)
	if err != nil {
		log.Printf("[lazy-start] task=%s setup failed: %v", taskID, err)
		task.StatusState = "failed"
		task.Status = "review"
		task.Output = err.Error()
		_ = te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(task.ID, err.Error(), "setup_failed")
		return
	}
	task = execSetup.task
	log.Printf("[lazy-start] task=%s worktree=%s", taskID, execSetup.worktreePath)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Agent responding"}})

	log.Printf("[lazy-start] task=%s starting executor", taskID)

	go te.executeTask(execSetup.ctx, task, projectID, execSetup.project.Name, execSetup.worktreePath, execSetup.serverURL, execSetup.modelName, execSetup.userMsgChan, execSetup.sandboxEnabled, execSetup.networkIsolation, execSetup.baseImage, execSetup.sandboxDockerfilePath, execSetup.memoryLimit)
}

// mergeBranch merges the task's branch into the origin branch.
// Returns nil on success or if merge is not applicable.
func (te *TaskExecutor) mergeBranch(projectID, taskID string, task *types.Task) error {
	project, err := te.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	// Determine origin branch
	originBranch := project.OriginBranch
	if originBranch == "" {
		originBranch, err = te.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
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

	// Stash any working tree changes
	hadStash := false
	if err := te.worktreeMgr.GitRunInProject(projectID, "stash", "push", "-m", "kanban auto-merge stash"); err == nil {
		hadStash = true
	}
	defer func() {
		if hadStash {
			_ = te.worktreeMgr.GitRunInProject(projectID, "stash", "pop")
		}
	}()

	// Checkout origin branch
	if err := te.worktreeMgr.GitRunInProject(projectID, "checkout", "-f", originBranch); err != nil {
		return fmt.Errorf("checkout origin branch %s: %w", originBranch, err)
	}

	// Merge the task branch
	mergeOut, mergeErr := te.worktreeMgr.GitRunInProjectOutput(projectID, "merge", taskBranch, "--no-edit")
	if mergeErr != nil {
		_ = te.worktreeMgr.GitRunInProject(projectID, "merge", "--abort")
		return fmt.Errorf("merge failed: %w (output: %s)", mergeErr, mergeOut)
	}

	log.Printf("[merge] task=%s merged %s into %s successfully", taskID, taskBranch, originBranch)
	return nil
}

// -- Helper functions --

var wordRe = regexp.MustCompile(`\S+`)

func estimateTokensFromText(text string) int {
	words := wordRe.FindAllString(text, -1)
	return int(float64(len(words)) * 1.3)
}

func estimatePromptTokens(messages []types.ChatMessage) int {
	total := 0
	for _, m := range messages {
		total += estimateTokensFromText(m.Content)
		total += estimateTokensFromText(m.ReasoningContent)
	}
	return total
}

func estimateTotalTokens(content, reasoning string) int {
	return estimateTokensFromText(content) + estimateTokensFromText(reasoning)
}

// branchExists checks if a git branch exists in the project.
func (te *TaskExecutor) branchExists(projectID, branchName string) bool {
	_, err := te.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--verify", branchName)
	return err == nil
}

// slugifyBranchName converts a task title into a git-friendly branch name.
// E.g. "Fix login bug #123" -> "fix-login-bug-123"
func slugifyBranchName(title string) string {
	// Convert to lowercase
	s := strings.ToLower(title)
	// Replace non-alphanumeric chars (except hyphens) with hyphens
	var result []rune
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result = append(result, r)
		} else if len(result) > 0 && result[len(result)-1] != '-' {
			result = append(result, '-')
		}
	}
	// Trim leading/trailing hyphens
	out := string(result)
	out = strings.Trim(out, "-")
	// Truncate to 78 chars (git branch name limit is 255, leave room for "task/" prefix)
	if len(out) > 78 {
		out = out[:78]
	}
	return out
}


