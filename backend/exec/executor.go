package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	osexec "os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"kanban/backend/agent"
	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/llama"
	"kanban/backend/lsp"
	"kanban/backend/prompts"
	sandboxmgr "kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// TaskExecutor handles task execution endpoints and orchestrates AI task execution.
type TaskExecutor struct {
	store         *store.Store
	configStore   *config.ConfigStore
	appStreamMgr  *stream.AppStreamManager
	worktreeMgr   *git.WorktreeManager
	sandboxMgr    *sandboxmgr.SandboxManager
	agentRegistry *agent.AgentRegistry
	lspMgr        *lsp.ExtensionLSPManager
	commitQueue   *CommitQueue
	gate          *ConcurrencyGate
	taskMu        sync.Mutex
	tasks         map[string]*executionState
	// Per-task mutexes to serialize worktree access across executors and merges.
	worktreeMu sync.Map // taskID -> *sync.Mutex
	// dbSummarizeCancel tracks cancel functions for DB-based background summarization.
	dbSummarizeCancel struct {
		mu    sync.Mutex
		tasks map[string]context.CancelFunc
	}
	// blockedMessages tracks tasks that had user messages blocked during summarization.
	blockedMessages struct {
		mu    sync.Mutex
		tasks map[string]struct{}
	}
}

// AgentRegistry returns the agent registry used by this executor.
func (te *TaskExecutor) AgentRegistry() *agent.AgentRegistry {
	return te.agentRegistry
}

type summarizeRequest struct {
	ctx      context.Context // cancelled when user clicks ✕ or new request arrives
	resultCh chan error      // receives ok/err from the loop
	cancel   context.CancelFunc
}

// CompactResult holds the result of an agent compact command.
type CompactResult struct {
	Summary string
	Error   error
}

type executionState struct {
	ctx          context.Context
	cancel       context.CancelFunc
	userMsgChan  chan stream.UserMessage
	summarizeCh  chan *summarizeRequest // send a request, receives result via resultCh
	activeSummarize struct {
		mu      sync.Mutex
		request *summarizeRequest // currently active summarize request (nil = none)
	}
	compactCh    chan chan *CompactResult      // send a channel, receives result from agent compaction
	agentRunner  *agent.AgentRunner            // Agent runner — kept alive after completion for on-demand compaction
	runnerClosed bool                          // true when agentRunner has been closed (prevents double-close)
	projectName  string
	createdIDs   []string // task IDs created by create_task during this execution
	executorType string   // "llm" or "agent" — tracks which executor type is active
	taskStatus   string   // task status at start; cleared by StopTask so HasRunningTasks can skip it
	// lastDiffHash tracks the last broadcast diff hash to avoid duplicate broadcasts
	// when fsnotify already caught the same change.
	lastDiffHash string
	// Sandbox I/O callbacks — nil means use os.Root fallback inside executeTool.
	sandboxExec       execFunc
	sandboxExecStream execStreamFunc
	sandboxRead       readFunc
	sandboxWrite      writeFunc
	// Operating mode at container creation time — used to detect mode changes
	// and recreate the container with the correct mount mode (ro vs rw).
	sandboxOperatingMode string
	// Agent sandbox config snapshot at container creation time — used to detect
	// extension config changes and recreate the container.
	agentSandboxHash string
	// os.Root handle for non-sandbox write isolation.
	root *os.Root
	// recoverBridge attempts to reconnect the sandbox bridge (client reconnect or
	// full sandbox recreation). Returns true if recovery succeeded (callbacks and
	// client are updated inside the callback). Only non-nil when sandbox is enabled.
	// Guards against infinite loops via bridgeRecoveryAttempts.
	recoverBridge func() bool
	bridgeRecoveryAttempts int
	// Closed by executor defer so ForwardUserMessage type-switch can wait for clean exit.
	done chan struct{}
}

// modelConfig holds mutable model/provider settings for an executor.
type LSPDiagnosticPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type LSPDiagnosticRange struct {
	Start LSPDiagnosticPosition `json:"start"`
}

type LSPDiagnostic struct {
	Range    LSPDiagnosticRange `json:"range"`
	Message  string             `json:"message"`
	Severity int                `json:"severity"`
}

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
func NewTaskExecutor(store *store.Store, configStore *config.ConfigStore, appStreamMgr *stream.AppStreamManager, worktreeMgr *git.WorktreeManager, sandboxMgr *sandboxmgr.SandboxManager, agentRegistry *agent.AgentRegistry, lspMgr *lsp.ExtensionLSPManager, commitQueue *CommitQueue) *TaskExecutor {
	te := &TaskExecutor{
		store:             store,
		configStore:       configStore,
		appStreamMgr:      appStreamMgr,
		worktreeMgr:       worktreeMgr,
		sandboxMgr:        sandboxMgr,
		agentRegistry:     agentRegistry,
		lspMgr:            lspMgr,
		commitQueue:       commitQueue,
		gate:              NewConcurrencyGate(),
		tasks:             make(map[string]*executionState),
	}
	te.dbSummarizeCancel.tasks = make(map[string]context.CancelFunc)
	te.blockedMessages.tasks = make(map[string]struct{})
	te.appStreamMgr.SetLazyStartHandler(te.handleLazyStart)
	return te
}

// GetWorktreeMu returns the per-task mutex for serializing worktree access.
func (te *TaskExecutor) GetWorktreeMu(taskID string) *sync.Mutex {
	mu, _ := te.worktreeMu.LoadOrStore(taskID, &sync.Mutex{})
	return mu.(*sync.Mutex)
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
	if _, _, err := te.gate.Acquire(ctx, serverURL, concurrency); err != nil {
		return err
	}
	defer te.gate.Release(serverURL)
	return fn()
}

// ChatCompletionOpts holds optional parameters for ChatCompletion.
type ChatCompletionOpts struct {
	MaxTokens       *int                  // MaxTokens caps the response. nil = server default (no cap).
	ReasoningEffort string                // ReasoningEffort overrides the default ("off"). "" = provider default.
	Thinking        *llama.ThinkingParams // Thinking sets reasoning params directly, overriding ReasoningEffort.
}

// ChatCompletion sends a non-streaming chat request through the concurrency gate.
func (te *TaskExecutor) ChatCompletion(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage, opts *ChatCompletionOpts) (*types.ChatUsage, string, error) {
	reasoningEffort := "off"
	var llamaOpts *llama.ChatCompletionOptions
	if opts != nil {
		if opts.ReasoningEffort != "" {
			reasoningEffort = opts.ReasoningEffort
		}
		llamaOpts = &llama.ChatCompletionOptions{
			MaxTokens: opts.MaxTokens,
			Thinking:  opts.Thinking,
		}
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	var usage *types.ChatUsage
	var text string
	err := te.RunWithGate(ctx, serverURL, func() error {
		var err error
		usage, text, err = client.ChatCompletion(ctx, messages, llamaOpts)
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
// Tasks in backlog qualify regardless of execution_state.
// Tasks in in_progress qualify when not actively running or failed.
func isIncompleteForStart(t types.Task) bool {
	if t.Status == "backlog" {
		return true
	}
	if t.Status == "in_progress" && t.ExecutionState != "running" && t.ExecutionState != "failed" {
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

	// Inherit LLM config from parent if the subtask doesn't have its own.
	// Subtasks created by plan-mode decomposition may not have model settings.
	if subtask.LlamaServerURL == "" && parent.LlamaServerURL != "" {
		subtask.LlamaServerURL = parent.LlamaServerURL
	}
	if subtask.ModelName == "" && parent.ModelName != "" {
		subtask.ModelName = parent.ModelName
	}
	if subtask.LlamaServerURL != "" || subtask.ModelName != "" {
		if err := te.store.UpdateTask(subtask); err != nil {
			return fmt.Errorf("update subtask model config: %w", err)
		}
	}

	// Ensure the parent has a branch (needed for subtask merge target).
	// This does NOT change the parent's status — it stays in its current column.
	if parent.Branch == "" {
		if err := te.EnsureParentBranch(projectID, parent); err != nil {
			return fmt.Errorf("ensure parent branch: %w", err)
		}
		parent, _ = te.store.GetTaskByTaskID(subtask.ParentTaskID)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, parent)
	} else {
		// Parent has a branch — sync it if the worktree's actual branch differs.
		// The stored branch may be stale (e.g., set before collision resolution).
		if parentWtPath, wtErr := te.worktreeMgr.WorktreePath(parent.ID); wtErr == nil {
			if _, statErr := os.Stat(parentWtPath); statErr == nil {
				if actualBranch, gitErr := te.worktreeMgr.GitRunInDirOutput(parentWtPath, "rev-parse", "--abbrev-ref", "HEAD"); gitErr == nil {
					actualBranch = strings.TrimSpace(actualBranch)
					if actualBranch != "" && actualBranch != parent.Branch {
						parent.Branch = actualBranch
						parent.UpdatedAt = time.Now().UTC()
						_ = te.store.UpdateTask(parent)
						te.appStreamMgr.BroadcastTaskUpdated(projectID, parent)
					}
				}
			}
		}
	}

	te.appStreamMgr.BroadcastTaskUpdated(projectID, subtask)

	// Start the subtask executor
	return te.StartTask(projectID, subtaskID)
}

// EnsureParentBranch creates the parent task's git branch if it doesn't exist yet.
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
func (te *TaskExecutor) EnsureParentBranch(projectID string, parent *types.Task) error {
	if parent.Branch != "" {
		return nil // already has a branch
	}

	branchName := te.generateBranchName(parent, parent.ID, projectID)

	// Atomic set prevents double worktree creation from concurrent callers.
	set, err := te.store.SetTaskBranchIfEmpty(parent.ID, projectID, branchName)
	if err != nil {
		return fmt.Errorf("set parent branch: %w", err)
	}
	if !set {
		latest, err := te.store.GetTaskByTaskID(parent.ID)
		if err != nil {
			return fmt.Errorf("reload parent task: %w", err)
		}
		if latest.Branch != "" {
			parent.Branch = latest.Branch
			return nil
		}
		// Unreachable safety net — SQLite guarantees this won't happen.
	}

	// Create a worktree for the parent from HEAD.
	// This creates the branch AND the worktree directory in one step.
	// When the parent starts later, CreateWorktree will find the directory
	// already exists and return early (reusing this worktree).
	_, _, err = te.worktreeMgr.CreateWorktree(projectID, parent.ID, branchName, "")
	if err != nil {
		return fmt.Errorf("create parent worktree: %w", err)
	}

	// Refresh updatedAt; adopt another caller's branch name if they won the race.
	latest, _ := te.store.GetTaskByTaskID(parent.ID)
	if latest.Branch != "" && latest.Branch != branchName {
		branchName = latest.Branch
	}
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
	if parent.ExecutionState == "completed" {
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

	// If the parent is still in backlog, do NOT auto-start siblings.
	// The user needs to explicitly move the parent to In Progress first.
	if parent.Status == "backlog" {
		log.Printf("[auto-start] parent %s is in backlog, skipping sibling auto-start (%d incomplete siblings)", parentTaskID, len(incomplete))
		return nil
	}

	// Select the deepest incomplete subtask
	selectedID := te.SelectSubtaskToStart(parent, incomplete)
	if selectedID == "" {
		return nil
	}

	// Reload to avoid stale state — prepareTaskExecution has its own guard.
	selected, err := te.store.GetTaskByTaskID(selectedID)
	if err != nil || !isIncompleteForStart(*selected) {
		log.Printf("[auto-start] task=%s selected sibling %s no longer incomplete, skipping", finishedTaskID, selectedID)
		return nil
	}

	log.Printf("[auto-start] task=%s finished, starting next sibling %s (parent=%s)", finishedTaskID, selectedID, parentTaskID)
	return te.StartSubtaskWithParent(projectID, selectedID)
}

// InitTask creates the worktree and branch for a task without starting LLM execution.
// This enables the terminal and file explorer before the task is started.
// It is idempotent — calling it on an already-initialized task is a no-op.
func (te *TaskExecutor) InitTask(projectID, taskID string) (*types.Task, error) {
	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil {
		return nil, err
	}

	// Already initialized
	if task.Branch != "" {
		return task, nil
	}

	branchName := te.generateBranchName(task, taskID, projectID)

	// Atomic set prevents double worktree creation from concurrent callers.
	set, err := te.store.SetTaskBranchIfEmpty(taskID, projectID, branchName)
	if err != nil {
		return nil, fmt.Errorf("set branch: %w", err)
	}
	if !set {
		// Another caller won the race — reload to get their branch name
		task, err = te.store.GetTaskByTaskID(taskID)
		if err != nil {
			return nil, err
		}
		if task.Branch != "" {
			return task, nil
		}
		// Unreachable safety net — SQLite guarantees this won't happen.
		return nil, fmt.Errorf("branch was not set by concurrent caller")
	}

	// If this is a child task, ensure the parent has a branch first.
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil && parent.Branch == "" {
			if err := te.EnsureParentBranch(projectID, parent); err != nil {
				return nil, fmt.Errorf("ensure parent branch: %w", err)
			}
		}
	}

	// If this is a child task, branch from the parent's actual worktree branch.
	sourceBranch := ""
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil {
			if parentWtPath, wtErr := te.worktreeMgr.WorktreePath(parent.ID); wtErr == nil {
				if _, statErr := os.Stat(parentWtPath); statErr == nil {
					if actualBranch, gitErr := te.worktreeMgr.GitRunInDirOutput(parentWtPath, "rev-parse", "--abbrev-ref", "HEAD"); gitErr == nil {
						sourceBranch = strings.TrimSpace(actualBranch)
					}
				}
			}
			if sourceBranch == "" && parent.Branch != "" {
				sourceBranch = parent.Branch
			}
		}
	}

	_, baseRef, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, sourceBranch)
	if err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	task.Branch = branchName
	task.BaseRef = baseRef

	// Capture the base ref's commit hash.
	if task.BaseCommit == "" && task.BaseRef != "" {
		project, _ := te.store.GetProject(projectID)
		if project != nil {
			c := osexec.Command("git", "-C", project.Path, "rev-parse", task.BaseRef)
			if out, err := c.CombinedOutput(); err == nil {
				task.BaseCommit = strings.TrimSpace(string(out))
			}
		}
	}

	watchBaseRef := task.BaseRef
	if task.BaseCommit != "" {
		watchBaseRef = task.BaseCommit
	}
	te.appStreamMgr.WatchWorktree(taskID, projectID, watchBaseRef)

	task.UpdatedAt = time.Now().UTC()
	if err := te.store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}

	log.Printf("[init] task=%s created worktree with branch %s", taskID, branchName)
	return task, nil
}

// taskExecSetup holds the shared state prepared for task execution.
type taskExecSetup struct {
	task                  *types.Task
	project               *types.Project
	projectID             string
	worktreePath          string
	projectPath           string
	serverURL             string
	modelName             string
	ctx                   context.Context
	cancel                context.CancelFunc
	userMsgChan           chan stream.UserMessage
	summarizeCh           chan *summarizeRequest // channel for manual summarization requests
	initialContent        string          // user message content for lazy-start (empty for normal start)
	regenerate            bool
	sandboxEnabled        bool
	sandboxDockerfilePath string
	sandboxOpts           sandboxmgr.SandboxOpts
	done                  chan struct{} // closed by executor defer; see executionState.done
}

// prepareTaskExecution sets up the shared infrastructure for task execution.
// Both StartTask and handleLazyStart use this to avoid duplicating worktree
// creation, branch watching, status updates, and sandbox configuration.
// Returns nil if the task is already actively running (caller should check execSetup == nil).
func (te *TaskExecutor) prepareTaskExecution(projectID, taskID string, overrideServerURL, overrideModelName string, updateStatus bool) (*taskExecSetup, error) {
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

	branchName := task.Branch
	if branchName == "" {
		branchName = te.generateBranchName(task, taskID, projectID)
	}
	// If this is a child task, ensure the parent has a branch first.
	// The parent's branch is the merge target for subtask auto-merge.
	// If the parent hasn't started yet (no branch), create one now.
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil && parent.Branch == "" {
			if err := te.EnsureParentBranch(projectID, parent); err != nil {
				return nil, fmt.Errorf("ensure parent branch: %w", err)
			}
		}
	}

	// If this is a child task, branch from the parent's actual worktree branch.
	// The stored parent.Branch may be stale (e.g., set before collision resolution).
	sourceBranch := ""
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil {
			// Prefer the parent's worktree branch if it exists.
			if parentWtPath, wtErr := te.worktreeMgr.WorktreePath(parent.ID); wtErr == nil {
				if _, statErr := os.Stat(parentWtPath); statErr == nil {
					if actualBranch, gitErr := te.worktreeMgr.GitRunInDirOutput(parentWtPath, "rev-parse", "--abbrev-ref", "HEAD"); gitErr == nil {
						sourceBranch = strings.TrimSpace(actualBranch)
					}
				}
			}
			// Fallback to stored branch if worktree branch unavailable.
			if sourceBranch == "" && parent.Branch != "" {
				sourceBranch = parent.Branch
			}
		}
	}
	worktreePath, baseRef, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, sourceBranch)
	if err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	task.Branch = branchName
	// Set BaseRef on first start (merge target / diff baseline).
	// Preserved across restarts — CreateWorktree returns same value either way.
	if task.BaseRef == "" {
		task.BaseRef = baseRef
	}
	// Capture the base ref's commit hash at task start time.
	// Used for done-task diffs: after a fast-forward merge, the base ref branch
	// pointer moves forward, so we need the original commit to compute the diff.
	if task.BaseCommit == "" && task.BaseRef != "" {
		project, _ := te.store.GetProject(projectID)
		if project != nil {
			c := osexec.Command("git", "-C", project.Path, "rev-parse", task.BaseRef)
			if out, err := c.CombinedOutput(); err == nil {
				task.BaseCommit = strings.TrimSpace(string(out))
			}
		}
	}
	// Use resolved baseline (same as API handler) so card stats match panel display.
	// BaseCommit is a fixed commit hash captured at task start; BaseRef is the branch name.
	watchBaseRef := task.BaseRef
	if task.BaseCommit != "" {
		watchBaseRef = task.BaseCommit
	}
	te.appStreamMgr.WatchWorktree(taskID, projectID, watchBaseRef)

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
	if updateStatus {
		task.ExecutionState = "running"
		task.Status = "in_progress"
		if err := te.store.UpdateTask(task); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	userMsgChan := make(chan stream.UserMessage, 16)
	summarizeCh := make(chan *summarizeRequest, 16)

	project, err := te.store.GetProject(projectID)
	if err != nil {
		cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		return nil, fmt.Errorf("get project for sandbox check: %w", err)
	}

	// Block agent providers when network isolation is enabled.
	// Keep the worktree and branch — the task is being prepared for work,
	// not deleted. The user can fix the config (enable network) and restart.
	if project.SandboxNetworkIsolation && te.configStore.IsAgentProvider(serverURL) {
		cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		task.ExecutionState = "idle"
		task.Status = "backlog"
		task.UpdatedAt = time.Now().UTC()
		_ = te.store.UpdateTask(task)
		return nil, fmt.Errorf("agent requires network to be enabled")
	}

	te.taskMu.Lock()
	// Cancel old executor if this task is being restarted.
	// For agent executors, the event loop stays alive after completion (for follow-ups).
	// We must cancel it to prevent duplicate executors.
	if oldState, exists := te.tasks[taskID]; exists {
		if oldState.agentRunner != nil && !oldState.runnerClosed {
			oldState.agentRunner.Close()
			oldState.runnerClosed = true
		}
		oldState.cancel() // cancel old context so its event loop exits
	}
	execType := "llm"
	if te.configStore.IsAgentProvider(serverURL) {
		execType = "agent"
	}
	doneCh := make(chan struct{})
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan, summarizeCh: summarizeCh, compactCh: make(chan chan *CompactResult, 16), projectName: project.Name, executorType: execType, taskStatus: task.Status, done: doneCh}
	te.taskMu.Unlock()

	return &taskExecSetup{
		task:                  task,
		project:               project,
		projectID:             projectID,
		worktreePath:          worktreePath,
		projectPath:           project.Path,
		serverURL:             serverURL,
		modelName:             modelName,
		ctx:                   ctx,
		cancel:                cancel,
		userMsgChan:           userMsgChan,
		summarizeCh:           summarizeCh,
		sandboxEnabled:        project.SandboxEnabled,
		sandboxDockerfilePath: project.SandboxDockerfilePath,
		sandboxOpts: sandboxmgr.SandboxOpts{
			BaseImage:        project.SandboxImage,
			NetworkIsolation: project.SandboxNetworkIsolation,
			MemoryLimit:      project.SandboxMemoryLimit,
			ForwardPorts:     project.ForwardPorts,
			SandboxMounts:    project.SandboxMounts,
			SandboxEnvs:      project.SandboxEnvs,
			Capabilities:     project.SandboxCapabilities,
		},
		done: doneCh,
	}, nil
}

// getAgentRunner ensures an agent runner is active for the given task,
// starting it if necessary. If started for compaction, it does not update task status.
func (te *TaskExecutor) getAgentRunner(ctx context.Context, projectID, taskID string) (*agent.AgentRunner, error) {
	te.taskMu.Lock()
	state, exists := te.tasks[taskID]
	if exists && state.agentRunner != nil && !state.runnerClosed {
		te.taskMu.Unlock()
		return state.agentRunner, nil
	}
	te.taskMu.Unlock()

	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil {
		return nil, err
	}

	// Agent runner requires agent provider.
	if !te.configStore.IsAgentProvider(task.LlamaServerURL) {
		return nil, fmt.Errorf("compaction only available for agent tasks")
	}

	// Extract agent name from URL: "agent://pi" -> "pi"
	agentName := strings.TrimPrefix(task.LlamaServerURL, "agent://")
	if agentName == "" || agentName == task.LlamaServerURL {
		return nil, fmt.Errorf("invalid agent URL: %s", task.LlamaServerURL)
	}

	// Lookup agent extension via registry
	ext := te.agentRegistry.LookupByName(agentName)
	if ext == nil {
		return nil, fmt.Errorf("agent %q not found in extensions", agentName)
	}

	// Prepare execution environment without updating task status in DB.
	setup, err := te.prepareTaskExecution(projectID, taskID, "", "", false)
	if err != nil {
		return nil, fmt.Errorf("prepare execution: %w", err)
	}

	// Create the sandbox container if needed (for sandbox-enabled agent tasks).
	// The container is normally created during executeTask, but for compaction
	// of idle tasks, we create it here so the agent subprocess can run.
	if setup.sandboxEnabled {
		if !te.sandboxMgr.ContainerIsRunning(task.ID) {
			if _, _, _, err := te.setupSandbox(setup); err != nil {
				return nil, fmt.Errorf("create sandbox for compaction: %w", err)
			}
		}
	}

	// Create the agent runner.
	var runner *agent.AgentRunner
	var runnerErr error
	if setup.sandboxEnabled {
		runner, runnerErr = agent.NewAgentRunner(setup.ctx, ext, te.sandboxMgr, task.ID, setup.worktreePath, fmt.Sprintf("kanban-%s", task.ID), true)
	} else {
		runner, runnerErr = agent.NewAgentRunner(setup.ctx, ext, nil, task.ID, setup.worktreePath, fmt.Sprintf("kanban-%s", task.ID), false)
	}
	if runnerErr != nil {
		return nil, fmt.Errorf("new agent runner: %w", runnerErr)
	}

	// Store it in the executor state.
	te.taskMu.Lock()
	// Re-check to avoid race.
	if state, exists := te.tasks[taskID]; exists && state.agentRunner != nil && !state.runnerClosed {
		runner.Close()
		te.taskMu.Unlock()
		return state.agentRunner, nil
	}
	// Use the setup created above.
	state = &executionState{
		ctx:          setup.ctx,
		cancel:       setup.cancel,
		userMsgChan:  setup.userMsgChan,
		summarizeCh:  setup.summarizeCh,
		compactCh:    make(chan chan *CompactResult, 16),
		agentRunner:  runner,
		projectName:  setup.project.Name,
		executorType: "agent",
		taskStatus:   task.Status,
		done:         setup.done,
	}
	te.tasks[taskID] = state
	te.taskMu.Unlock()

	return runner, nil
}

// StartTask begins AI execution for a task.
func (te *TaskExecutor) StartTask(projectID, taskID string) error {
	execSetup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
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

	go te.executeTask(execSetup)
	return nil
}
func (te *TaskExecutor) StopTask(taskID string) error {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	state, ok := te.tasks[taskID]
	if !ok {
		return fmt.Errorf("task not running")
	}
	// Close agent runner if it's still alive (kept after completion for compaction).
	if state.agentRunner != nil && !state.runnerClosed {
		state.agentRunner.Close()
		state.runnerClosed = true
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
// executor AND is currently in the "in_progress" column. This ensures that
// only truly active tasks block sandbox settings changes, while stale
// executors for tasks that have been moved can be cleaned up automatically.
func (te *TaskExecutor) HasRunningTasks(projectID string) bool {
	tasks, err := te.store.GetTasksByProject(projectID)
	if err != nil {
		return false
	}
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	for _, task := range tasks {
		if _, ok := te.tasks[task.ID]; ok && task.Status == "in_progress" {
			return true
		}
	}
	return false
}

// ForwardUserMessage forwards a user message to the running executor or lazy-starts one.
// If the requested executor type (llm vs pi) differs from the currently running executor,
// it stops the old executor and lazy-starts a new one with the correct type.
func (te *TaskExecutor) ForwardUserMessage(projectID, taskID string, um stream.UserMessage) {
	requestedType := "llm"
	if te.configStore.IsAgentProvider(um.ServerURL) {
		requestedType = "agent"
	}

	te.taskMu.Lock()
	state, exists := te.tasks[taskID]
	if exists {
		if state.ctx.Err() != nil {
			log.Printf("[executor-switch] task=%s executor loop is dead, restarting via lazy-start", taskID)
			if state.agentRunner != nil && !state.runnerClosed {
				state.agentRunner.Close()
				state.runnerClosed = true
			}
			state.cancel()
			delete(te.tasks, taskID)
			te.taskMu.Unlock()
			go te.handleLazyStart(projectID, taskID, um.Content, um.ModelName, um.ServerURL, um.ReasoningEffort, um.Regenerate)
			return
		}
		if state.executorType != requestedType {
			log.Printf("[executor-switch] task=%s switching from %s to %s", taskID, state.executorType, requestedType)
			done := state.done
			if state.agentRunner != nil && !state.runnerClosed {
				state.agentRunner.Close()
				state.runnerClosed = true
			}
			state.cancel()
			delete(te.tasks, taskID)
			te.taskMu.Unlock()
			<-done
			go te.handleLazyStart(projectID, taskID, um.Content, um.ModelName, um.ServerURL, um.ReasoningEffort, um.Regenerate)
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
	// Block lazy-start while summarization is in progress to prevent
	// concurrent LLM calls. Message is already in DB from HandleSend.
	if task, _ := te.store.GetTaskByTaskID(taskID); task != nil && task.Summarizing {
		log.Printf("[user-msg] task=%s blocking lazy-start: summarization in progress", taskID)
		te.blockedMessages.mu.Lock()
		te.blockedMessages.tasks[taskID] = struct{}{}
		te.blockedMessages.mu.Unlock()
		return
	}
	go te.handleLazyStart(projectID, taskID, um.Content, um.ModelName, um.ServerURL, um.ReasoningEffort, um.Regenerate)
}

// NotifyContextChanged tells the running executor that the conversation context has changed
// (e.g., messages were deleted). The executor will reset its loop detector and re-sync
// messages from the DB on the next LLM call.
func (te *TaskExecutor) NotifyContextChanged(projectID, taskID string) {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	state, exists := te.tasks[taskID]
	if !exists {
		return
	}
	select {
	case state.userMsgChan <- stream.UserMessage{ContextChanged: true}:
	default:
		log.Printf("[context-changed] task=%s channel full, dropping notification", taskID)
	}
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
func (te *TaskExecutor) handleLazyStart(projectID, taskID, content, modelName, serverURL, reasoningEffort string, regenerate bool) {
	log.Printf("[lazy-start] task=%s project=%s (override: model=%s, url=%s, effort=%s, regenerate=%v)", taskID, projectID, modelName, serverURL, reasoningEffort, regenerate)

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

	execSetup, err := te.prepareTaskExecution(projectID, taskID, serverURL, modelName, true)
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


	// If task has a merge conflict and the content is not already a conflict prompt,
	// prepend conflict context (covers sync-merge path where no user message was persisted).
	if task.HasMergeConflict && !strings.Contains(content, "merge conflict occurred") {
		conflictedFiles := GetConflictedFiles(execSetup.worktreePath)
		conflictContext, pErr := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
			TaskBranch:      task.Branch,
			TargetBranch:    te.ResolveMergeTarget(projectID, task),
			TaskTitle:       task.Title,
			TaskDescription: task.Prompt,
			ConflictedFiles: conflictedFiles,
		})
		if pErr != nil {
			log.Printf("[lazy-start] task=%s failed to render merge conflict prompt: %v", taskID, pErr)
		}
		conflictContext += "\nAfter resolving the merge, continue with the user's request."
		content = conflictContext + "\n\n--- User message ---\n\n" + content
		log.Printf("[lazy-start] task=%s merge conflict detected, injecting conflict context", taskID)
	}

	execSetup.initialContent = content // pass user message to executor
	execSetup.regenerate = regenerate
	log.Printf("[lazy-start] task=%s worktree=%s", taskID, execSetup.worktreePath)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	log.Printf("[lazy-start] task=%s starting executor", taskID)

	go te.executeTask(execSetup)
}

// CommitAndMerge commits staged worktree changes and optionally merges the task branch.
// Only explicitly staged files are committed; unstaged changes remain in the working tree.
// Operations are serialized per project via the commit queue to prevent git race conditions.
func (te *TaskExecutor) CommitAndMerge(projectID, taskID, worktreePath, commitMsg string, autoMerge bool) error {
	release := te.commitQueue.Acquire(projectID)
	defer release()

	te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", commitMsg, "--allow-empty")...)

	if autoMerge {
		task, _ := te.store.GetTaskByTaskID(taskID)
		if err := te.MergeBranch(projectID, taskID, task); err != nil {
			log.Printf("[auto-commit] task=%s merge failed: %v", taskID, err)
			return err
		}
	} else {
		log.Printf("[auto-commit] task=%s merge skipped (auto_merge=false)", taskID)
	}
	return nil
}

// syncTargetIntoTask merges the target branch into the task's branch (in the task's worktree)
// when the target has new commits since the task branch diverged. This catches conflicts early
// while the LLM still has full context of the task's changes.
//
// Returns nil on success (clean merge or no new commits) or when merge_conflict state is entered.
// The caller (MergeBranch) should check task.ExecutionState after return — if it is
// "merge_conflict", the final merge should be skipped.
func (te *TaskExecutor) syncTargetIntoTask(projectID, taskID, targetBranch, taskBranch string, task *types.Task) error {
	// Serialize against the task's own tool execution.
	te.GetWorktreeMu(taskID).Lock()
	defer te.GetWorktreeMu(taskID).Unlock()

	project, err := te.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project for sync: %w", err)
	}

	// Check if target branch has new commits beyond where task branch diverged.
	// merge-base is the common ancestor; if it equals target HEAD, target hasn't moved.
	mergeBaseCmd := osexec.Command("git", "merge-base", taskBranch, targetBranch)
	mergeBaseCmd.Dir = project.Path
	mergeBaseOut, err := mergeBaseCmd.Output()
	if err != nil {
		log.Printf("[sync-merge] task=%s could not compute merge-base: %v, skipping sync", taskID, err)
		return nil
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))

	targetHeadCmd := osexec.Command("git", "rev-parse", targetBranch)
	targetHeadCmd.Dir = project.Path
	targetHeadOut, err := targetHeadCmd.Output()
	if err != nil {
		log.Printf("[sync-merge] task=%s could not resolve target HEAD: %v, skipping sync", taskID, err)
		return nil
	}
	targetHead := strings.TrimSpace(string(targetHeadOut))

	if mergeBase == targetHead {
		log.Printf("[sync-merge] task=%s target %s has no new commits, skipping sync", taskID, targetBranch)
		return nil
	}

	log.Printf("[sync-merge] task=%s target %s has new commits (merge-base=%s, target=%s), syncing", taskID, targetBranch, mergeBase[:7], targetHead[:7])

	// Get task's worktree path
	worktreePath, err := te.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		return fmt.Errorf("get worktree path for sync: %w", err)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		log.Printf("[sync-merge] task=%s worktree does not exist, skipping sync", taskID)
		return nil
	}

	// Stash any working tree changes in the worktree.
	hadStash := false
	te.worktreeMgr.GetProjectStashMu(projectID).Lock()
	if err := te.worktreeMgr.GitRunInDir(worktreePath, "stash", "push", "-u", "-m", git.StashMessageForSyncMerge(taskID)); err == nil {
		hadStash = true
	}
	te.worktreeMgr.GetProjectStashMu(projectID).Unlock()

	// Ensure we're on the task branch
	if err := te.worktreeMgr.GitRunInDir(worktreePath, "checkout", "-f", taskBranch); err != nil {
		return fmt.Errorf("checkout task branch %s for sync: %w", taskBranch, err)
	}

	// Merge target branch into task branch
	_, mergeErr := te.worktreeMgr.GitRunInDirOutput(worktreePath, "merge", targetBranch, "--no-edit")
	if mergeErr == nil {
		log.Printf("[sync-merge] task=%s synced %s into %s cleanly", taskID, targetBranch, taskBranch)
		te.logSystemMessage(taskID, task.CurrentThreadID,
			fmt.Sprintf("Synced %s into task branch (no conflicts).", targetBranch))
		// Pop stash — restore user's unstaged changes.
		// If stash pop conflicts, drop it to avoid leaving markers.
		if hadStash {
			te.worktreeMgr.GetProjectStashMu(projectID).Lock()
			if err := te.worktreeMgr.GitRunInDir(worktreePath, "stash", "pop"); err != nil {
				log.Printf("[sync-merge] task=%s stash pop failed, dropping stash: %v", taskID, err)
				_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "drop")
			}
			te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
		}
		return nil
	}

	// Merge conflict — set flag so the LLM loop (or HandleCommitAndDone)
	// can trigger the agent to resolve via file tools.
	// Keep user's stash intact — it will be popped after merge --continue.
	log.Printf("[sync-merge] task=%s conflict syncing %s into %s, setting merge conflict flag", taskID, targetBranch, taskBranch)
	_ = te.store.SetTaskMergeConflict(taskID, projectID, true)
	task.HasMergeConflict = true
	te.logSystemMessage(taskID, task.CurrentThreadID,
		fmt.Sprintf("Conflicts detected when syncing %s into task branch. Resolve the conflicted files using your tools — the backend will commit and continue the merge automatically.", targetBranch))
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	// Keep merge in progress — do NOT abort. LLM will resolve with file tools.
	return nil
}

// ResolveMergeTarget determines the branch that changes will be merged into.
// For child tasks, the target is the parent's branch.
// For root tasks, the target is the task's base_ref (if set), otherwise the project's origin branch.
func (te *TaskExecutor) ResolveMergeTarget(projectID string, task *types.Task) string {
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil && parent.Branch != "" {
			return parent.Branch
		}
	}
	if task.BaseRef != "" {
		return task.BaseRef
	}
	project, err := te.store.GetProject(projectID)
	if err != nil || project.OriginBranch == "" {
		return ""
	}
	return project.OriginBranch
}

// mergeBranch merges the task's branch into the target branch.
// For child tasks, merges into the parent's branch. For root tasks, merges into origin.
// Returns nil on success or if merge is not applicable.
//
// If the target branch is checked out in another worktree (e.g., a parent whose
// worktree was created by EnsureParentBranch), the merge is performed inside
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
	// For child tasks, the target is ALWAYS the parent's branch (never origin).
	// For root tasks, the target is the origin branch.
	var targetBranch string
	var mergeInWorktree string // non-empty: run merge in this worktree path
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByTaskID(task.ParentTaskID)
		if err != nil {
			log.Printf("[merge] task=%s parent_task_id=%s not found: %v, aborting merge", taskID, task.ParentTaskID, err)
			te.logSystemMessage(taskID, task.CurrentThreadID,
				fmt.Sprintf("Error: parent task %s not found — merge aborted.", task.ParentTaskID))
			return fmt.Errorf("parent task %s not found", task.ParentTaskID)
		}
		// Child tasks MUST merge into their parent's branch, never origin.
		// If the parent has no branch yet, create it now.
		if parent.Branch == "" {
			log.Printf("[merge] task=%s parent %s has no branch, creating it now", taskID, parent.ID)
			if err := te.EnsureParentBranch(projectID, parent); err != nil {
				log.Printf("[merge] task=%s failed to create parent branch: %v, aborting merge", taskID, err)
				te.logSystemMessage(taskID, task.CurrentThreadID,
					fmt.Sprintf("Error: could not create branch for parent task %q — merge aborted: %v", parent.Title, err))
				return fmt.Errorf("create parent branch: %w", err)
			}
			parent, _ = te.store.GetTaskByTaskID(task.ParentTaskID)
		}
		// Ensure parent has a worktree to merge into.
		parentWorktreePath, wtErr := te.worktreeMgr.WorktreePath(parent.ID)
		hasWorktree := wtErr == nil && parentWorktreePath != ""
		if hasWorktree {
			if _, statErr := os.Stat(parentWorktreePath); statErr != nil {
				hasWorktree = false
				parentWorktreePath = ""
			}
		}
		if !hasWorktree {
			// Parent has no worktree — create one from its branch.
			log.Printf("[merge] task=%s parent %s has no worktree, creating it now", taskID, parent.ID)
			branchName := parent.Branch
			if branchName == "" {
				branchName = te.generateBranchName(parent, parent.ID, projectID)
			}
			if _, _, err := te.worktreeMgr.CreateWorktree(projectID, parent.ID, branchName, ""); err != nil {
				log.Printf("[merge] task=%s failed to create parent worktree: %v", taskID, err)
			} else {
				parentWorktreePath, _ = te.worktreeMgr.WorktreePath(parent.ID)
				hasWorktree = true
			}
		}
		// Use the parent's worktree and the branch it's actually on.
		if hasWorktree && parentWorktreePath != "" {
			mergeInWorktree = parentWorktreePath
			if currentBranch, err := te.worktreeMgr.GitRunInDirOutput(parentWorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
				targetBranch = strings.TrimSpace(currentBranch)
			}
		}
		if targetBranch == "" {
			targetBranch = parent.Branch
		}
	} else {
		targetBranch = te.ResolveMergeTarget(projectID, task)
		if targetBranch == "" {
			var err error
			targetBranch, err = te.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				log.Printf("[merge] task=%s not a git repo, skipping merge: %v", taskID, err)
				return nil
			}
		}
	}
	targetBranch = strings.TrimSpace(targetBranch)
	// Create target branch if it doesn't exist (user-specified base_ref).
	if task.ParentTaskID == "" && task.BaseRef != "" && targetBranch == task.BaseRef {
		cmd := osexec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+targetBranch)
		cmd.Dir = project.Path
		if cmd.Run() != nil {
			// Branch doesn't exist — create it from origin branch
			startPoint := project.OriginBranch
			if startPoint == "" {
				startPoint = "HEAD"
			}
			createCmd := osexec.Command("git", "-C", project.Path, "branch", targetBranch, startPoint)
			if out, err := createCmd.CombinedOutput(); err != nil {
				log.Printf("[merge] task=%s failed to create branch %q from %q: %s", taskID, targetBranch, startPoint, string(out))
				te.logSystemMessage(taskID, task.CurrentThreadID,
					fmt.Sprintf("Error: could not create branch %q from %q — merge skipped.", targetBranch, startPoint))
				return fmt.Errorf("create branch %q: %w", targetBranch, err)
			}
			log.Printf("[merge] task=%s created branch %q from %q", taskID, targetBranch, startPoint)
		}
	}
	if targetBranch == "" || targetBranch == "HEAD" {
		log.Printf("[merge] task=%s no branch (detached HEAD), skipping merge", taskID)
		return nil
	}
	log.Printf("[merge] task=%s merging %s into %s (parent_task_id=%s)", taskID, taskBranch, targetBranch, task.ParentTaskID)

	// Sync target branch into task branch first (catches conflicts early).
	if syncErr := te.syncTargetIntoTask(projectID, taskID, targetBranch, taskBranch, task); syncErr != nil {
		return fmt.Errorf("sync target into task: %w", syncErr)
	}
	// If sync has a merge conflict, skip the final merge.
	task, _ = te.store.GetTaskByTaskID(taskID)
	if task.HasMergeConflict {
		log.Printf("[merge] task=%s sync has merge conflict, skipping final merge", taskID)
		return nil
	}

	if mergeInWorktree != "" {
		return te.mergeInWorktree(projectID, mergeInWorktree, targetBranch, taskBranch, taskID, task.ParentTaskID)
	}
	// For root tasks only: if the target branch is checked out in a worktree, use it.
	// For child tasks, we always merge into the parent's worktree (created above if needed).
	if task.ParentTaskID == "" {
		if wtPath := te.findWorktreeForBranch(projectID, targetBranch); wtPath != "" {
			return te.mergeInWorktree(projectID, wtPath, targetBranch, taskBranch, taskID, "")
		}
	}
	return te.mergeInProject(projectID, targetBranch, taskBranch, taskID)
}

// mergeInProject performs a merge in the project's main directory.
func (te *TaskExecutor) mergeInProject(projectID, targetBranch, taskBranch, taskID string) error {
	project, _ := te.store.GetProject(projectID)
	// Prune stale worktree registrations so a deleted worktree doesn't block checkout.
	te.worktreeMgr.GitRunInProject(projectID, "worktree", "prune")
	return te.mergeInto(projectID, project.Path, targetBranch, taskBranch, taskID)
}

// findWorktreeForBranch returns the path of the worktree that has the given branch
// checked out, or empty string if no worktree holds it. This is needed because git
// won't allow checking out a branch in the main working tree if a worktree already
// has it checked out.
func (te *TaskExecutor) findWorktreeForBranch(projectID, branch string) string {
	project, err := te.store.GetProject(projectID)
	if err != nil {
		return ""
	}
	cmd := osexec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = project.Path
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	worktrees := strings.Split(string(out), "\n")
	var currentPath, currentBranch string
	for _, line := range worktrees {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
			currentBranch = ""
		} else if strings.HasPrefix(line, "branch ") {
			currentBranch = strings.TrimPrefix(line, "branch refs/heads/")
		}
		if currentBranch == branch && currentPath != "" {
			if _, err := os.Stat(currentPath); err == nil {
				return currentPath
			}
		}
	}
	return ""
}

// mergeInWorktree merges inside another task's worktree (e.g. the parent's).
// Acquires ownerTaskID's mutex to serialize against the owner's tool execution.
func (te *TaskExecutor) mergeInWorktree(projectID, worktreePath, targetBranch, taskBranch, taskID, ownerTaskID string) error {
	if ownerTaskID != "" {
		te.GetWorktreeMu(ownerTaskID).Lock()
		defer te.GetWorktreeMu(ownerTaskID).Unlock()
	}
	return te.mergeInto(projectID, worktreePath, targetBranch, taskBranch, taskID)
}

// mergeInto performs a merge of taskBranch into targetBranch inside the given directory.
// Both mergeInProject and mergeInWorktree delegate here — the only difference between them
// is the directory in which git commands are run.
func (te *TaskExecutor) mergeInto(projectID, dir, targetBranch, taskBranch, taskID string) error {
	// Stash any working tree changes (serialized with shared stash mutex).
	hadStash := false
	te.worktreeMgr.GetProjectStashMu(projectID).Lock()
	if err := te.worktreeMgr.GitRunInDir(dir, "stash", "push", "-u", "-m", git.StashMessageForAutoMerge(taskID)); err == nil {
		hadStash = true
	}
	te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
	defer func() {
		if hadStash {
			te.worktreeMgr.GetProjectStashMu(projectID).Lock()
			_ = te.worktreeMgr.GitRunInDir(dir, "stash", "pop")
			te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
		}
	}()

	// Checkout target branch
	if err := te.worktreeMgr.GitRunInDir(dir, "checkout", "-f", targetBranch); err != nil {
		return fmt.Errorf("checkout target branch %s: %w", targetBranch, err)
	}

	// Merge the task branch
	mergeOut, mergeErr := te.worktreeMgr.GitRunInDirOutput(dir, "merge", taskBranch, "--no-edit")
	if mergeErr != nil {
		// Merge conflict — detect, set flag for LLM resolution, then abort
		task, _ := te.store.GetTaskByTaskID(taskID)
		if resolveErr := te.tryResolveMergeConflicts(dir, projectID, taskID, targetBranch, taskBranch, task); resolveErr != nil {
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
			// Real merge conflict — set flag so HandleCommitAndDone triggers LLM resolution
			_ = te.store.SetTaskMergeConflict(taskID, projectID, true)
			_ = te.worktreeMgr.GitRunInDir(dir, "merge", "--abort")
			te.logMergeFailed(taskID, task.CurrentThreadID, fmt.Sprintf("Merge failed: %v", resolveErr))
			return fmt.Errorf("merge conflict detected (will resolve via LLM): %w", resolveErr)
		}
	} else {
		log.Printf("[merge] task=%s merged %s into %s successfully", taskID, taskBranch, targetBranch)
		task, _ := te.store.GetTaskByTaskID(taskID)
		te.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
	}
	return nil
}

// -- Helper functions --

var wordRe = regexp.MustCompile(`\S+`)

// buildMessagesFromDB loads persisted messages and builds the system prompt.
// The most recent summary is injected into the system prompt.
func buildMessagesFromDB(task *types.Task, taskMsgs []types.TaskMessage, worktreePath, projectName string, sandboxEnabled bool) []types.ChatMessage {
	cwd := worktreePath
	if sandboxEnabled {
		cwd = "/home/sandbox/" + projectName
	}

	// Extract most recent summary for the system prompt.
	var summary string
	for i := len(taskMsgs) - 1; i >= 0; i-- {
		if taskMsgs[i].Role == "summarization" {
			summary = taskMsgs[i].Content
			break
		}
	}

	var systemPrompt string
	skillsMount := ""
	if sandboxEnabled {
		skillsMount = sandboxmgr.SkillsMount
	}
	systemPrompt = llama.BuildSystemPrompt(worktreePath, cwd, summary, skillsMount, task.OperatingMode)

	messages := []types.ChatMessage{{Role: "system", Content: systemPrompt}}

	i := 0
	for i < len(taskMsgs) {
		m := taskMsgs[i]
		switch m.Role {
		case "summarization":
			// buildLLMContext filters these out (summary is in system prompt)
			messages = append(messages, types.ChatMessage{Role: "summarization", Content: m.Content})
			i++
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
					i++
					continue
				}
			}
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: m.Content})
			i++
		case "assistant":
			// Collect all tool_call messages for this turn (look ahead).
			i++
			var turnToolCalls []types.TaskMessage
			for i < len(taskMsgs) && taskMsgs[i].Role == "tool_call" {
				turnToolCalls = append(turnToolCalls, taskMsgs[i])
				i++
			}
			// Add assistant message with tool calls merged in.
			msg := types.ChatMessage{Role: m.Role, Content: m.Content}
			if m.ReasoningContent != "" {
				msg.ReasoningContent = m.ReasoningContent
			}
			// Merge tool calls from this turn into the assistant message.
			for _, tc := range turnToolCalls {
				var tcMap map[string]any
				if tc.ToolCalls != "" {
					if err := json.Unmarshal([]byte(tc.ToolCalls), &tcMap); err == nil {
						toolID, _ := tcMap["tool_id"].(string)
						toolName, _ := tcMap["tool_name"].(string)
						rawArgs, _ := tcMap["raw_args"].(string)
						if toolID != "" && toolName != "" {
							msg.ToolCalls = append(msg.ToolCalls, types.ToolCallMsg{ID: toolID, Type: "function"})
							msg.ToolCalls[len(msg.ToolCalls)-1].Function.Name = toolName
							msg.ToolCalls[len(msg.ToolCalls)-1].Function.Arguments = rawArgs
						}
					}
				}
			}
			messages = append(messages, msg)
			// Add tool results for each tool call.
			for _, tc := range turnToolCalls {
				if tc.ToolResult != "" {
					var resultMap map[string]any
					if err := json.Unmarshal([]byte(tc.ToolResult), &resultMap); err == nil {
						output, _ := resultMap["output"].(string)
						toolID := ""
						if tc.ToolCalls != "" {
							var tcMap map[string]any
							if err2 := json.Unmarshal([]byte(tc.ToolCalls), &tcMap); err2 == nil {
								toolID, _ = tcMap["tool_id"].(string)
							}
						}
						messages = append(messages, types.ChatMessage{Role: "tool", Content: output, ToolCallID: toolID})
					}
				}
			}
		default:
			i++
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
		if len(m.ContentParts) > 0 {
			// Multi-modal: text is in ContentParts[0].Text, not Content
			for _, p := range m.ContentParts {
				if p.Type == "text" {
					total += estimateTokensFromText(p.Text)
				}
			}
		} else {
			total += estimateTokensFromText(m.Content)
		}
		total += estimateTokensFromText(m.ReasoningContent)
	}
	return total
}

// getCompactCh returns the compact channel for a running task, or nil if not found.
func (te *TaskExecutor) getCompactCh(taskID string) chan chan *CompactResult {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	if state, ok := te.tasks[taskID]; ok {
		return state.compactCh
	}
	return nil
}

// branchExists checks if a git branch exists in the project.
func (te *TaskExecutor) branchExists(projectID, branchName string) bool {
	_, err := te.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--verify", branchName)
	return err == nil
}

// maxBranchName is the maximum length for any git branch name.
const maxBranchName = 50

// generateBranchName creates a unique git branch name for a task.
// Uses a slugified title when possible (e.g. "task/fix-login-bug"),
// falls back to the task ID (e.g. "task/frost-amber-quartz"),
// and appends a short suffix if the branch already exists from a deleted task.
// The total branch name (including prefix) is capped at 50 characters.
func (te *TaskExecutor) generateBranchName(task *types.Task, taskID, projectID string) string {
	branchName := fmt.Sprintf("task/%s", task.ID)
	if !task.AutoMerge && task.Title != "" {
		slug := slugifyBranchName(task.Title)
		if slug != "" && len("task/"+slug) <= maxBranchName {
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
		// Append a unique suffix: taskID[:8], then taskID[:8]-N if that also collides
		suffix := "-" + taskID[:8]
		n := 1
		for {
			candidate := branchName + suffix
			if n > 1 {
				candidate = branchName + suffix + fmt.Sprintf("-%d", n)
			}
			// Truncate to fit within maxBranchName
			if len(candidate) > maxBranchName {
				candidate = candidate[:maxBranchName]
			}
			if !te.branchExists(projectID, candidate) {
				branchName = candidate
				break
			}
			n++
			if n > 100 {
				break // safety net — shouldn't happen
			}
		}
	}
	return branchName
}

// slugifyBranchName converts a task title into a git-friendly branch name slug.
// E.g. "Fix login bug #123" -> "fix-login-bug-123"
// The slug is capped so that "task/" + slug fits within maxBranchName (50 chars).
func slugifyBranchName(title string) string {
	s := strings.ToLower(title)
	var result []rune
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result = append(result, r)
		} else if len(result) > 0 && result[len(result)-1] != '-' {
			result = append(result, '-')
		}
	}
	out := string(result)
	out = strings.Trim(out, "-")
	slugMax := maxBranchName - len("task/")
	if len(out) > slugMax {
		out = out[:slugMax]
	}
	return out
}

func agentSandboxHash(sb *types.AgentSandbox) string {
	if sb == nil {
		return ""
	}
	b, _ := json.Marshal(sb)
	return string(b)
}

func (te *TaskExecutor) formatLSPDiagnostics(filePath string, diagnostics []LSPDiagnostic) string {
	var lines []string
	for _, d := range diagnostics {
		if d.Severity <= 1 {
			lines = append(lines, fmt.Sprintf("LSP Error in %s: %s at line %d, col %d", filePath, d.Message, d.Range.Start.Line+1, d.Range.Start.Character+1))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func (te *TaskExecutor) triggerLSPDiagnostics(ctx context.Context, projectID, taskID, filePath string) string {
	// Normalize sandbox path to relative path (e.g., /home/sandbox/temp/lsp_test.go -> lsp_test.go)
	relPath := normalizePath(projectID, filePath)
	if te.lspMgr == nil {
		log.Printf("[lsp-diag] task=%s lspMgr is nil", taskID)
		return ""
	}
	ext := filepath.Ext(relPath)
	if ext == "" {
		log.Printf("[lsp-diag] task=%s no file extension", taskID)
		return ""
	}
	lang := strings.TrimPrefix(ext, ".")
	registry := te.lspMgr.GetRegistry()
	cfg := registry.Lookup(relPath)
	if cfg == nil {
		log.Printf("[lsp-diag] task=%s no LSP config for relPath=%s ext=%s", taskID, relPath, ext)
		return ""
	}
	log.Printf("[lsp-diag] task=%s found LSP config lang=%s", taskID, lang)
	worktreePath, err := te.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		log.Printf("[lsp-diag] task=%s worktree path error: %v", taskID, err)
		return ""
	}
	proc, err := te.lspMgr.GetOrCreate(worktreePath, lang, cfg)
	if err != nil {
		log.Printf("[lsp-diag] task=%s GetOrCreate failed: %v", taskID, err)
		return ""
	}
	log.Printf("[lsp-diag] task=%s LSP connection ready", taskID)
	defer te.lspMgr.Release(worktreePath, lang)

	// Subscribe to LSP messages for this diagnostic check.
	subCh := te.lspMgr.Subscribe()
	defer te.lspMgr.Unsubscribe(subCh)

	absPath := filepath.Join(worktreePath, relPath)
	uri := "file://" + absPath

	// Step 1: LSP initialize handshake (required before any textDocument requests)
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"processId":    os.Getpid(),
			"clientInfo":   map[string]any{"name": "kanban-lsp-diag", "version": "1.0"},
			"rootUri":      "file://" + worktreePath,
			"workspaceFolders": []map[string]any{{"uri": "file://" + worktreePath, "name": "workspace"}},
			"capabilities": map[string]any{
				"textDocument": map[string]any{
					"publishDiagnostics": map[string]any{
						"relatedInformation": true,
						"tagSupport":         map[string]any{"valueSet": []int{1, 2}},
					},
				},
				"workspace": map[string]any{
					"workspaceFolders": true,
				},
			},
		},
	}
	// Merge server-specific init params overrides (e.g., tsserver.path for TypeScript)
	if cfg.InitParamsOverride != nil {
		params := initReq["params"].(map[string]any)
		for k, v := range cfg.InitParamsOverride {
			if existing, ok := params[k].(map[string]any); ok {
				if override, ok := v.(map[string]any); ok {
					for ok2, ov := range override {
						existing[ok2] = ov
					}
				} else {
					params[k] = v
				}
			} else {
				params[k] = v
			}
		}
	}
	initMsg, _ := json.Marshal(initReq)
	if err := te.lspMgr.SendToServer(proc, initMsg); err != nil {
		log.Printf("[lsp-diag] task=%s initialize failed: %v", taskID, err)
		return ""
	}

	// Wait for initialize response
	initTimer := time.After(10 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-initTimer:
			log.Printf("[lsp-diag] task=%s initialize timeout", taskID)
			return ""
		case raw, ok := <-subCh:
			if !ok {
				return ""
			}
			var resp struct {
				JSONRPC string `json:"jsonrpc"`
				ID      *int   `json:"id"`
				Result  any    `json:"result"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				continue
			}
			if resp.ID != nil && *resp.ID == 1 {
				if resp.Error != nil {
					log.Printf("[lsp-diag] task=%s initialize error: %s", taskID, resp.Error.Message)
					return ""
				}
				log.Printf("[lsp-diag] task=%s initialize successful", taskID)
				goto initialized
			}
		}
	}
initialized:
	// Step 2: Send initialized notification (fire-and-forget, no response expected)
	initNotif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	}
	initNotifMsg, _ := json.Marshal(initNotif)
	if err := te.lspMgr.LSPNotify(proc, initNotifMsg); err != nil {
		log.Printf("[lsp-diag] task=%s initialized notification failed: %v", taskID, err)
		return ""
	}

	// Step 3: Read file content and send didOpen (fire-and-forget notification)
	content, err := os.ReadFile(filepath.Join(worktreePath, relPath))
	if err != nil {
		log.Printf("[lsp-diag] task=%s ReadFile failed: %v", taskID, err)
		return ""
	}

	didOpen := map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": lang,
				"version":    1,
				"text":       string(content),
			},
		},
	}
	didOpenMsg, _ := json.Marshal(didOpen)
	if err := te.lspMgr.LSPNotify(proc, didOpenMsg); err != nil {
		log.Printf("[lsp-diag] task=%s didOpen failed: %v", taskID, err)
		return ""
	}

	// Step 4: Wait for publishDiagnostics
	timeout := time.After(10 * time.Second)
	for {
		select {
		case <-ctx.Done():
			log.Printf("[lsp-diag] task=%s context cancelled", taskID)
			return ""
		case <-timeout:
			log.Printf("[lsp-diag] task=%s timeout waiting for diagnostics", taskID)
			return ""
		case raw, ok := <-subCh:
			if !ok {
				log.Printf("[lsp-diag] task=%s subscriber channel closed", taskID)
				return ""
			}
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					Uri         string          `json:"uri"`
					Diagnostics []LSPDiagnostic `json:"diagnostics"`
				} `json:"params"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				continue
			}
			log.Printf("[lsp-diag] task=%s received LSP message: method=%s uri=%s diagnostics=%d", taskID, envelope.Method, envelope.Params.Uri, len(envelope.Params.Diagnostics))
			if envelope.Method != "textDocument/publishDiagnostics" || envelope.Params.Uri != uri {
				continue
			}
			errMsg := te.formatLSPDiagnostics(relPath, envelope.Params.Diagnostics)
			if errMsg != "" {
				log.Printf("[lsp-diag] task=%s diagnostic hint: %s", taskID, errMsg)
				return errMsg
			}
			log.Printf("[lsp-diag] task=%s no errors found (diagnostics may be warnings only)", taskID)
			return ""
		}
	}
}
