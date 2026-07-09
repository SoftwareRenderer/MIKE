package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	osexec "os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/llama"
	sandboxmgr "kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"

	sandbox "kanban/pkg/bridge"

	"github.com/google/uuid"
)

// TaskExecutor handles task execution endpoints and orchestrates AI task execution.
type TaskExecutor struct {
	store        *store.Store
	configStore  *config.ConfigStore
	appStreamMgr *stream.AppStreamManager
	worktreeMgr  *git.WorktreeManager
	sandboxMgr   *sandboxmgr.SandboxManager
	commitQueue  *CommitQueue
	gate         *ConcurrencyGate
	taskMu       sync.Mutex
	tasks        map[string]*executionState
}

type executionState struct {
	ctx          context.Context
	cancel       context.CancelFunc
	userMsgChan  chan stream.UserMessage
	summarizeCh  chan chan error // send a channel, receives result (ok/err) from the loop
	projectName  string
	createdIDs   []string // task IDs created by create_task during this execution
	executorType string   // "llm" or "pi" — tracks which executor type is active
	taskStatus   string   // task status at start; cleared by StopTask so HasRunningTasks can skip it
	// lastDiffHash tracks the last broadcast diff hash to avoid duplicate broadcasts
	// when fsnotify already caught the same change.
	lastDiffHash string
	// Sandbox I/O callbacks — nil means use os.Root fallback inside executeTool.
	sandboxExec       execFunc
	sandboxExecStream execStreamFunc
	sandboxRead       readFunc
	sandboxWrite      writeFunc
	// Sandbox client reference for liveness checks (nil when sandbox is disabled).
	sandboxClient *sandbox.Client
	// os.Root handle for non-sandbox write isolation.
	root *os.Root
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
func NewTaskExecutor(store *store.Store, configStore *config.ConfigStore, appStreamMgr *stream.AppStreamManager, worktreeMgr *git.WorktreeManager, sandboxMgr *sandboxmgr.SandboxManager, commitQueue *CommitQueue) *TaskExecutor {
	te := &TaskExecutor{
		store:        store,
		configStore:  configStore,
		appStreamMgr: appStreamMgr,
		worktreeMgr:  worktreeMgr,
		sandboxMgr:   sandboxMgr,
		commitQueue:  commitQueue,
		gate:         NewConcurrencyGate(),
		tasks:        make(map[string]*executionState),
	}
	te.appStreamMgr.SetLazyStartHandler(te.handleLazyStart)
	return te
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

// ChatCompletionOpts holds optional parameters for ChatCompletion.
type ChatCompletionOpts struct {
	MaxTokens       *int              // MaxTokens caps the response. nil = server default (no cap).
	ReasoningEffort string            // ReasoningEffort overrides the default ("off"). "" = provider default.
	Thinking        *llama.ThinkingParams // Thinking sets reasoning params directly, overriding ReasoningEffort.
}

// ChatCompletion sends a non-streaming chat request through the concurrency gate.
func (te *TaskExecutor) ChatCompletion(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage, opts *ChatCompletionOpts) (*types.ChatUsage, string, error) {
	var tokens *int
	reasoningEffort := "off"
	var thinking *llama.ThinkingParams
	if opts != nil {
		tokens = opts.MaxTokens
		if opts.ReasoningEffort != "" {
			reasoningEffort = opts.ReasoningEffort
		}
		thinking = opts.Thinking
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	var usage *types.ChatUsage
	var text string
	err := te.RunWithGate(ctx, serverURL, func() error {
		var err error
		if tokens != nil && thinking != nil {
			usage, text, err = client.ChatCompletion(ctx, messages, *tokens, thinking)
		} else if tokens != nil {
			usage, text, err = client.ChatCompletion(ctx, messages, *tokens)
		} else if thinking != nil {
			usage, text, err = client.ChatCompletion(ctx, messages, thinking)
		} else {
			usage, text, err = client.ChatCompletion(ctx, messages)
		}
		return err
	})
	return usage, text, err
}

// StreamCompletion sends a streaming chat request through the concurrency gate.
// The caller passes messages and receives tokens via the callback — queue management
// is fully transparent. Uses the same provider concurrency limits as ChatCompletion.
func (te *TaskExecutor) StreamCompletion(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage, cb types.StreamCallback, onProgress types.PromptProgressCallback) (*types.ChatUsage, error) {
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, "off", apiKey)
	var usage *types.ChatUsage
	err := te.RunWithGate(ctx, serverURL, func() error {
		var err error
		usage, err = client.ChatStream(ctx, messages, nil, cb, onProgress)
		return err
	})
	return usage, err
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
	if t.Status == "in_progress" && t.ExecutionState == "idle" {
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

	_, response, err := te.ChatCompletion(ctx, serverURL, modelName, messages, nil)
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

// StartSubtaskWithParent starts the executor for the specified subtask.
// The parent stays in its current column (e.g., backlog) while subtasks run.
// Siblings are NOT moved — that is handled by HandleMove when a parent task
// (not an individual subtask) is moved.
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

	// Ensure the parent has a branch (needed for subtask merge target).
	// This does NOT change the parent's status — it stays in its current column.
	if parent.Branch == "" {
		if err := te.ensureParentBranch(projectID, parent); err != nil {
			return fmt.Errorf("ensure parent branch: %w", err)
		}
		// Broadcast the updated parent (branch now set)
		parent, _ = te.store.GetTaskByTaskID(subtask.ParentTaskID)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, parent)
	}

	te.appStreamMgr.BroadcastTaskUpdated(projectID, subtask)

	// Start the subtask executor
	return te.StartTask(projectID, subtaskID)
}

// ensureParentBranch creates the parent task's git branch if it doesn't exist yet.
// This is called when a subtask is being prepared for execution and the parent
// hasn't started (parent.Branch == ""). The parent's branch is needed as the
// merge target when the subtask completes with auto_merge enabled.
//
// A full worktree is created for the parent (not just a bare branch) so that
// when the parent eventually starts, prepareTaskExecution finds the worktree
// already exists and reuses it. The worktree is created from HEAD (origin).
//
// The parent's status is NOT changed — it stays in its current column (e.g.,
// backlog) while subtasks run. Only the branch field is set.
//
// If the parent already has a branch, this is a no-op.
func (te *TaskExecutor) ensureParentBranch(projectID string, parent *types.Task) error {
	if parent.Branch != "" {
		return nil // already has a branch
	}

	// Generate branch name for the parent (same logic as prepareTaskExecution)
	branchName := fmt.Sprintf("task/%s", parent.ID)
	if !parent.AutoMerge && parent.Title != "" {
		slug := slugifyBranchName(parent.Title)
		if len(slug) <= 50 {
			branchName = "task/" + slug
		}
	}
	parentWtPath, _ := te.worktreeMgr.WorktreePath(parent.ID)
	_, parentWtErr := os.Stat(parentWtPath)
	parentWtExists := parentWtErr == nil
	if !parentWtExists && te.branchExists(projectID, branchName) {
		branchName = branchName + "-" + parent.ID[:8]
	}

	// Create a worktree for the parent from HEAD.
	// This creates the branch AND the worktree directory in one step.
	// When the parent starts later, CreateWorktree will find the directory
	// already exists and return early (reusing this worktree).
	_, _, err := te.worktreeMgr.CreateWorktree(projectID, parent.ID, branchName, "")
	if err != nil {
		return fmt.Errorf("create parent worktree: %w", err)
	}

	// Update parent task with the branch only — do NOT change status.
	// The parent stays in its current column (backlog) while subtasks run.
	parent.Branch = branchName
	parent.UpdatedAt = time.Now().UTC()
	if err := te.store.UpdateTask(parent); err != nil {
		// Clean up the worktree if DB update fails
		_ = te.worktreeMgr.RemoveWorktree(projectID, parent.ID, branchName)
		return fmt.Errorf("update parent task with branch: %w", err)
	}

	log.Printf("[ensure-parent-branch] task=%s created worktree with branch %s for parent", parent.ID, branchName)
	return nil
}

// autoStartNextSibling finds the next incomplete sibling of the given task and starts it.
// Runs if the parent is in_progress or backlog (parent stays in backlog while subtasks run).
// When all siblings are done, the parent is only auto-started if it was already moved
// to In Progress by the user. If the parent is in Backlog, it stays there.
// Ancestors in "backlog" are never auto-started (they are their own tasks).
func (te *TaskExecutor) AutoStartNextSibling(projectID, finishedTaskID, parentTaskID, finishedStatus string) error {
	parent, err := te.store.GetTaskByTaskID(parentTaskID)
	if err != nil {
		return fmt.Errorf("get parent task: %w", err)
	}
	// Auto-start if parent is in_progress or backlog (parent stays in backlog while subtasks run).
	// Skip if paused or already done.
	if parent.Status != "in_progress" && parent.Status != "backlog" {
		return nil
	}
	if parent.ExecutionState == "paused" || parent.ExecutionState == "completed" {
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
		// All children done — only start the parent if the user already moved it
		// to In Progress. If the parent is in Backlog, leave it there; the user
		// needs to explicitly start or move it.
		if parent.Status == "in_progress" {
			log.Printf("[auto-start] task=%s all children done, starting parent %s", finishedTaskID, parent.ID)
			if err := te.StartTask(projectID, parent.ID); err != nil {
				log.Printf("[auto-start] task=%s failed to start parent %s: %v", finishedTaskID, parent.ID, err)
			} else {
				started, _ := te.store.GetTaskByTaskID(parent.ID)
				if started != nil {
					te.appStreamMgr.BroadcastTaskUpdated(projectID, started)
				}
			}
		} else {
			log.Printf("[auto-start] task=%s all children done, parent %s in backlog — waiting for user to start", finishedTaskID, parent.ID)
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
	task                  *types.Task
	project               *types.Project
	worktreePath          string
	serverURL             string
	modelName             string
	ctx                   context.Context
	cancel                context.CancelFunc
	userMsgChan           chan stream.UserMessage
	summarizeCh           chan chan error // channel for manual summarization requests
	initialContent        string          // user message content for lazy-start (empty for normal start)
	sandboxEnabled        bool
	networkIsolation      bool
	baseImage             string
	sandboxDockerfilePath string
	memoryLimit           int
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
	if task.ExecutionState == "running" {
		te.taskMu.Lock()
		_, activelyRunning := te.tasks[taskID]
		te.taskMu.Unlock()
		if activelyRunning {
			return nil, fmt.Errorf("task already running")
		}
		log.Printf("[start] task=%s stale running state detected, resetting", taskID)
		task.ExecutionState = "idle"
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
	if !task.AutoMerge && task.Title != "" {
		slug := slugifyBranchName(task.Title)
		// If the slug is too long, fall back to the task ID (always short, ~22 chars)
		if len(slug) <= 50 {
			branchName = "task/" + slug
		}
	}
	// Ensure uniqueness: if the branch already exists (e.g., from a deleted task)
	// AND the worktree doesn't exist yet (first run), append a short suffix.
	// If the worktree already exists, the branch was created with this name — keep it.
	wtPath, wtErr := te.worktreeMgr.WorktreePath(taskID)
	_, wtStatErr := os.Stat(wtPath)
	wtExists := wtErr == nil && wtStatErr == nil
	if !wtExists && te.branchExists(projectID, branchName) {
		branchName = branchName + "-" + taskID[:8]
	}
	// If this is a child task, ensure the parent has a branch first.
	// The parent's branch is the merge target for subtask auto-merge.
	// If the parent hasn't started yet (no branch), create one now.
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil && parent.Branch == "" {
			if err := te.ensureParentBranch(projectID, parent); err != nil {
				return nil, fmt.Errorf("ensure parent branch: %w", err)
			}
		}
	}

	// If this is a child task, branch from the parent's branch
	sourceBranch := ""
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil && parent.Branch != "" {
			sourceBranch = parent.Branch
		}
	}
	worktreePath, baseRef, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, sourceBranch)
	if err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	task.Branch = branchName
	// Preserve existing BaseRef on restart: CreateWorktree returns the main repo's
	// branch name (e.g. "master") when the worktree already exists, but we need
	// the original commit SHA for correct diff computation.
	if task.BaseRef == "" {
		task.BaseRef = baseRef
	}
	te.appStreamMgr.WatchWorktree(taskID, projectID, task.BaseRef)

	// If the task has a current thread with a specific git branch, checkout that branch
	if task.CurrentThreadID != "" {
		thread, terr := te.store.GetThread(task.CurrentThreadID)
		if terr == nil && thread.GitBranch != "" && thread.GitBranch != branchName {
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "checkout", "-B", thread.GitBranch)
			task.Branch = thread.GitBranch
		}
	}

	// Apply model/provider overrides to the task before persisting.
	// This ensures the DB has the correct URL/model even if the frontend's
	// updateTask call hasn't arrived yet (race condition during executor switch).
	if serverURL != "" {
		task.LlamaServerURL = serverURL
	}
	if modelName != "" {
		task.ModelName = modelName
	}
	task.ExecutionState = "running"
	task.Status = "in_progress"
	if err := te.store.UpdateTask(task); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	userMsgChan := make(chan stream.UserMessage, 16)
	summarizeCh := make(chan chan error, 16)

	project, err := te.store.GetProject(projectID)
	if err != nil {
		cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		return nil, fmt.Errorf("get project for sandbox check: %w", err)
	}

	// Block agent providers when network isolation is enabled
	if project.SandboxNetworkIsolation && te.configStore.IsAgentProvider(serverURL) {
		cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		// Clean up resources created before this check
		task.ExecutionState = "idle"
		task.Status = "backlog"
		task.UpdatedAt = time.Now().UTC()
		_ = te.store.UpdateTask(task)
		te.appStreamMgr.UnwatchWorktree(taskID)
		_ = te.worktreeMgr.RemoveWorktree(projectID, taskID, task.Branch)
		return nil, fmt.Errorf("agent requires network to be enabled")
	}

	te.taskMu.Lock()
	execType := "llm"
	if te.configStore.IsAgentProvider(serverURL) {
		execType = "pi"
	}
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan, summarizeCh: summarizeCh, projectName: project.Name, executorType: execType, taskStatus: task.Status}
	te.taskMu.Unlock()

	return &taskExecSetup{
		task:                  task,
		project:               project,
		worktreePath:          worktreePath,
		serverURL:             serverURL,
		modelName:             modelName,
		ctx:                   ctx,
		cancel:                cancel,
		userMsgChan:           userMsgChan,
		summarizeCh:           summarizeCh,
		sandboxEnabled:        project.SandboxEnabled,
		networkIsolation:      project.SandboxNetworkIsolation,
		baseImage:             project.SandboxImage,
		sandboxDockerfilePath: project.SandboxDockerfilePath,
		memoryLimit:           project.SandboxMemoryLimit,
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

	// Agents send their own status:running (Pi via agent_start).
	if !te.configStore.IsAgentProvider(execSetup.serverURL) {
		te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})
	}

	go te.executeTask(execSetup.ctx, task, projectID, execSetup.project.Name, execSetup.worktreePath, execSetup.project.Path, execSetup.serverURL, execSetup.modelName, execSetup.userMsgChan, execSetup.summarizeCh, execSetup.sandboxEnabled, execSetup.networkIsolation, execSetup.baseImage, execSetup.sandboxDockerfilePath, execSetup.memoryLimit, "", execSetup.project.ForwardPorts, execSetup.project.SandboxMounts)
	return nil
}
func (te *TaskExecutor) StopTask(taskID string) error {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	state, ok := te.tasks[taskID]
	if !ok {
		return fmt.Errorf("task not running")
	}
	state.taskStatus = ""
	state.cancel()
	delete(te.tasks, taskID)
	return nil
}

// StopProjectTasks stops all actively running executors for tasks in a project.
// Used when project-level settings (e.g., sandbox) change while tasks are running.
// Returns the count of tasks that were stopped.
func (te *TaskExecutor) StopProjectTasks(projectID string) int {
	tasks, err := te.store.GetTasksByProject(projectID)
	if err != nil {
		return 0
	}
	stopped := 0
	for _, task := range tasks {
		te.taskMu.Lock()
		if _, ok := te.tasks[task.ID]; ok {
			state := te.tasks[task.ID]
			state.cancel()
			delete(te.tasks, task.ID)
			stopped++
			log.Printf("[stop-project-tasks] task=%s project=%s stopped (settings change)", task.ID, projectID)
			// Reset only the running task to idle; preserve its column (status)
			task.ExecutionState = "idle"
			te.taskMu.Unlock()
			_ = te.store.UpdateTask(&task)
		} else {
			te.taskMu.Unlock()
		}
	}
	return stopped
}

// HasRunningTasks returns true if any task in the given project has an active
// executor with taskStatus == "in_progress". Uses only the in-memory map
// (no DB call) — taskStatus is stored in executionState at creation time and
// cleared by StopTask, so there is no race between DB status and map state.
func (te *TaskExecutor) HasRunningTasks(projectID string) bool {
	tasks, err := te.store.GetTasksByProject(projectID)
	if err != nil {
		return false
	}
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	for _, id := range taskIDs {
		if state, ok := te.tasks[id]; ok && state.taskStatus == "in_progress" {
			return true
		}
	}
	return false
}

// forwardUserMessage forwards a user message to the running executor or lazy-starts one.
// If the requested executor type (llm vs pi) differs from the currently running executor,
// it stops the old executor and lazy-starts a new one with the correct type.
func (te *TaskExecutor) forwardUserMessage(projectID, taskID string, um stream.UserMessage) {
	requestedType := "llm"
	if te.configStore.IsAgentProvider(um.ServerURL) {
		requestedType = "pi"
	}

	te.taskMu.Lock()
	state, exists := te.tasks[taskID]
	if exists {
		if state.executorType != requestedType {
			// Executor type switch: stop old executor, start new one
			log.Printf("[executor-switch] task=%s switching from %s to %s", taskID, state.executorType, requestedType)
			state.cancel()
			delete(te.tasks, taskID)
			te.taskMu.Unlock()
			// Brief drain to let old executor finish its select loop
			time.Sleep(100 * time.Millisecond)
			go te.handleLazyStart(projectID, taskID, um.Content, um.ModelName, um.ServerURL, um.ReasoningEffort)
			return
		}
		select {
		case state.userMsgChan <- um:
		default:
			log.Printf("[user-msg] task=%s channel full, dropping message", taskID)
		}
		te.taskMu.Unlock()
		return
	}
	te.taskMu.Unlock()
	go te.handleLazyStart(projectID, taskID, um.Content, um.ModelName, um.ServerURL, um.ReasoningEffort)
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
	task.ExecutionState = "idle"
	te.store.UpdateTask(task)
	return te.StartTask(projectID, taskID)
}

// handleLazyStart is called when a user message arrives for a task with no active executor.
func (te *TaskExecutor) handleLazyStart(projectID, taskID, content, modelName, serverURL, reasoningEffort string) {
	log.Printf("[lazy-start] task=%s project=%s (override: model=%s, url=%s, effort=%s)", taskID, projectID, modelName, serverURL, reasoningEffort)

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
	// Apply model override from the user message (may arrive before updateTask completes)
	if reasoningEffort != "" {
		task.ReasoningEffort = reasoningEffort
		te.store.UpdateTask(task)
	}

	execSetup, err := te.prepareTaskExecution(projectID, taskID, serverURL, modelName)
	if err != nil {
		log.Printf("[lazy-start] task=%s setup failed: %v", taskID, err)
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = err.Error()
		_ = te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(task.ID, err.Error(), "setup_failed")
		return
	}
	task = execSetup.task
	execSetup.initialContent = content // pass user message to executor
	log.Printf("[lazy-start] task=%s worktree=%s", taskID, execSetup.worktreePath)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Agent responding"}})

	log.Printf("[lazy-start] task=%s starting executor", taskID)

	go te.executeTask(execSetup.ctx, task, projectID, execSetup.project.Name, execSetup.worktreePath, execSetup.project.Path, execSetup.serverURL, execSetup.modelName, execSetup.userMsgChan, execSetup.summarizeCh, execSetup.sandboxEnabled, execSetup.networkIsolation, execSetup.baseImage, execSetup.sandboxDockerfilePath, execSetup.memoryLimit, content, execSetup.project.ForwardPorts, execSetup.project.SandboxMounts)
}

// CommitAndMerge commits worktree changes and optionally merges the task branch.
// Operations are serialized per project via the commit queue to prevent git race conditions.
func (te *TaskExecutor) CommitAndMerge(projectID, taskID, worktreePath, commitMsg string, autoMerge bool) {
	release := te.commitQueue.Acquire(projectID)
	defer release()

	te.gitRun(worktreePath, "add", "-A")
	unstageNewBinaryFilesInDir(worktreePath)
	te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", commitMsg)...)

	if autoMerge {
		task, _ := te.store.GetTaskByTaskID(taskID)
		if err := te.MergeBranch(projectID, taskID, task); err != nil {
			log.Printf("[auto-commit] task=%s merge failed: %v", taskID, err)
		}
	}
}

// mergeBranch merges the task's branch into the target branch.
// For child tasks, merges into the parent's branch. For root tasks, merges into origin.
// Returns nil on success or if merge is not applicable.
//
// If the target branch is checked out in another worktree (e.g., a parent whose
// worktree was created by ensureParentBranch), the merge is performed inside
// that worktree instead of the main repo, since git won't allow checking out
// a branch that's already used by a worktree.
func (te *TaskExecutor) MergeBranch(projectID, taskID string, task *types.Task) error {
	project, err := te.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	taskBranch := task.Branch
	if taskBranch == "" {
		log.Printf("[merge] task=%s no task branch set, skipping merge", taskID)
		return nil
	}

	// Verify the branch points to a valid commit before attempting merge.
	// If the branch ref is broken (e.g., worktree was removed, ref corrupted),
	// skip the merge gracefully rather than failing with a cryptic error.
	cmd := osexec.Command("git", "rev-parse", "--verify", "--quiet", taskBranch)
	cmd.Dir = project.Path
	if err := cmd.Run(); err != nil {
		log.Printf("[merge] task=%s branch %q is not a valid commit, skipping merge: %v", taskID, taskBranch, err)
		return nil
	}

	// Determine target branch and the git directory to run merge in.
	// For child tasks, the target is the parent's branch.
	// If the parent has a worktree, merge inside it (branch is checked out there).
	var targetBranch string
	var mergeInWorktree string // non-empty: run merge in this worktree path
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil && parent.Branch != "" {
			targetBranch = parent.Branch
			// Check if parent has a worktree (created by ensureParentBranch)
			parentWorktreePath, wtErr := te.worktreeMgr.WorktreePath(parent.ID)
			if wtErr == nil {
				if _, statErr := os.Stat(parentWorktreePath); statErr == nil {
					mergeInWorktree = parentWorktreePath
				}
			}
		}
	}
	if targetBranch == "" {
		targetBranch = project.OriginBranch
		if targetBranch == "" {
			targetBranch, err = te.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				log.Printf("[merge] task=%s not a git repo, skipping merge: %v", taskID, err)
				return nil
			}
		}
	}
	targetBranch = strings.TrimSpace(targetBranch)
	if targetBranch == "" || targetBranch == "HEAD" {
		log.Printf("[merge] task=%s no branch (detached HEAD), skipping merge", taskID)
		return nil
	}

	if mergeInWorktree != "" {
		return te.mergeInWorktree(projectID, mergeInWorktree, targetBranch, taskBranch, taskID)
	}
	return te.mergeInProject(projectID, targetBranch, taskBranch, taskID)
}

// mergeInProject performs a merge in the project's main directory.
func (te *TaskExecutor) mergeInProject(projectID, targetBranch, taskBranch, taskID string) error {
	project, _ := te.store.GetProject(projectID)

	// Stash any working tree changes (serialized with shared stash mutex).
	hadStash := false
	te.worktreeMgr.GetProjectStashMu(projectID).Lock()
	if err := te.worktreeMgr.GitRunInProject(projectID, "stash", "push", "-u", "-m", "kanban auto-merge stash"); err == nil {
		hadStash = true
	}
	te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
	defer func() {
		if hadStash {
			te.worktreeMgr.GetProjectStashMu(projectID).Lock()
			_ = te.worktreeMgr.GitRunInProject(projectID, "stash", "pop")
			te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
		}
	}()

	// Checkout target branch
	if err := te.worktreeMgr.GitRunInProject(projectID, "checkout", "-f", targetBranch); err != nil {
		return fmt.Errorf("checkout target branch %s: %w", targetBranch, err)
	}

	// Merge the task branch
	mergeOut, mergeErr := te.worktreeMgr.GitRunInProjectOutput(projectID, "merge", taskBranch, "--no-edit")
	if mergeErr != nil {
		// Merge conflict — try LLM resolution
		task, _ := te.store.GetTaskByTaskID(taskID)
		if resolveErr := te.tryResolveMergeConflicts(project.Path, projectID, taskID, targetBranch, taskBranch, task); resolveErr != nil {
			if errors.Is(resolveErr, ErrNotAMergeConflict) {
				// Not a conflict — git merge failed for another reason (hook, lock, etc.)
				// Include git output so the user sees the actual reason
				mergeDetail := strings.TrimSpace(mergeOut)
				if mergeDetail == "" {
					mergeDetail = mergeErr.Error()
				}
				log.Printf("[merge] task=%s merge failed (not a conflict): %s", taskID, mergeDetail)
				te.logMergeFailed(taskID, task.CurrentThreadID, fmt.Sprintf("Merge failed: %s", mergeDetail))
				return fmt.Errorf("merge failed (not a conflict): %s", mergeDetail)
			}
			_ = te.worktreeMgr.GitRunInProject(projectID, "merge", "--abort")
			te.logMergeFailed(taskID, task.CurrentThreadID, fmt.Sprintf("Merge failed: %v", resolveErr))
			return fmt.Errorf("merge failed and conflict resolution failed: %w", resolveErr)
		}
	} else {
		log.Printf("[merge] task=%s merged %s into %s successfully", taskID, taskBranch, targetBranch)
		task, _ := te.store.GetTaskByTaskID(taskID)
		te.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
	}
	return nil
}

// mergeInWorktree performs a merge inside a specific worktree directory.
// Used when the target branch is checked out in another worktree (e.g., parent's worktree).
func (te *TaskExecutor) mergeInWorktree(projectID, worktreePath, targetBranch, taskBranch, taskID string) error {
	// Stash any working tree changes in the worktree (serialized with shared stash mutex).
	hadStash := false
	te.worktreeMgr.GetProjectStashMu(projectID).Lock()
	if err := te.worktreeMgr.GitRunInDir(worktreePath, "stash", "push", "-u", "-m", "kanban auto-merge stash"); err == nil {
		hadStash = true
	}
	te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
	defer func() {
		if hadStash {
			te.worktreeMgr.GetProjectStashMu(projectID).Lock()
			_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "pop")
			te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
		}
	}()

	// Ensure we're on the target branch
	if err := te.worktreeMgr.GitRunInDir(worktreePath, "checkout", "-f", targetBranch); err != nil {
		return fmt.Errorf("checkout target branch %s in worktree: %w", targetBranch, err)
	}

	// Merge the task branch
	mergeOut, mergeErr := te.worktreeMgr.GitRunInDirOutput(worktreePath, "merge", taskBranch, "--no-edit")
	if mergeErr != nil {
		// Merge conflict — try LLM resolution
		task, _ := te.store.GetTaskByTaskID(taskID)
		if resolveErr := te.tryResolveMergeConflicts(worktreePath, projectID, taskID, targetBranch, taskBranch, task); resolveErr != nil {
			if errors.Is(resolveErr, ErrNotAMergeConflict) {
				mergeDetail := strings.TrimSpace(mergeOut)
				if mergeDetail == "" {
					mergeDetail = mergeErr.Error()
				}
				log.Printf("[merge] task=%s merge failed in worktree (not a conflict): %s", taskID, mergeDetail)
				te.logMergeFailed(taskID, task.CurrentThreadID, fmt.Sprintf("Merge failed: %s", mergeDetail))
				return fmt.Errorf("merge failed in worktree (not a conflict): %s", mergeDetail)
			}
			_ = te.worktreeMgr.GitRunInDir(worktreePath, "merge", "--abort")
			te.logMergeFailed(taskID, task.CurrentThreadID, fmt.Sprintf("Merge failed: %v", resolveErr))
			return fmt.Errorf("merge failed and conflict resolution failed: %w", resolveErr)
		}
	} else {
		log.Printf("[merge] task=%s merged %s into %s (in worktree) successfully", taskID, taskBranch, targetBranch)
		task, _ := te.store.GetTaskByTaskID(taskID)
		te.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
	}
	return nil
}

// -- Helper functions --

var wordRe = regexp.MustCompile(`\S+`)

// buildMessagesFromDB loads all persisted messages. LLM context is assembled via buildLLMContext.
func buildMessagesFromDB(task *types.Task, taskMsgs []types.TaskMessage, worktreePath, projectName string, sandboxEnabled bool) []types.ChatMessage {
	cwd := worktreePath
	if sandboxEnabled {
		cwd = "/home/sandbox/" + projectName
	}
	var systemPrompt string
	if task.PlanMode {
		systemPrompt = llama.BuildSystemPrompt(worktreePath, cwd, true)
	} else {
		systemPrompt = llama.BuildSystemPrompt(worktreePath, cwd, false)
	}

	messages := []types.ChatMessage{{Role: "system", Content: systemPrompt}}

	for _, m := range taskMsgs {
		switch m.Role {
		case "summarization":
			// Keep as-is with role "summarization" — buildLLMContext formats it for the LLM
			messages = append(messages, types.ChatMessage{Role: "summarization", Content: m.Content})
		case "user":
			if m.Attachments != "" {
				var atts []struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				}
				if err := json.Unmarshal([]byte(m.Attachments), &atts); err == nil {
					parts := []types.ContentPart{{Type: "text", Text: m.Content}}
					for _, att := range atts {
						if att.Data != "" {
							mediaType := att.MimeType
							if mediaType == "" {
								mediaType = "image/png"
							}
							base64Data := att.Data
							if idx := strings.Index(base64Data, ","); idx >= 0 {
								base64Data = base64Data[idx+1:]
							}
							parts = append(parts, types.ContentPart{
								Type: "image",
								ImageSource: &struct {
									Type      string `json:"type"`
									MediaType string `json:"media_type"`
									Data      string `json:"data"`
								}{Type: "base64", MediaType: mediaType, Data: base64Data},
							})
						}
					}
					messages = append(messages, types.ChatMessage{Role: m.Role, ContentParts: parts})
					continue
				}
			}
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: m.Content})
		case "assistant":
			content := m.Content
			if m.ReasoningContent != "" {
				content = m.ReasoningContent + "\n" + m.Content
			}
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: content})
		case "tool_call":
			if m.ToolResult != "" {
				var resultMap map[string]any
				if err := json.Unmarshal([]byte(m.ToolResult), &resultMap); err == nil {
					output, _ := resultMap["output"].(string)
					toolID := ""
					if m.ToolCalls != "" {
						var tcMap map[string]any
						if err2 := json.Unmarshal([]byte(m.ToolCalls), &tcMap); err2 == nil {
							toolID, _ = tcMap["tool_id"].(string)
						}
					}
					messages = append(messages, types.ChatMessage{Role: "tool", Content: output, ToolCallID: toolID})
				}
			}
		}
	}

	return messages
}

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
	// Truncate to 50 chars. If the slug exceeds this, the caller falls back
	// to the task ID (e.g., "frost-amber-quartz"), which is always short.
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}
