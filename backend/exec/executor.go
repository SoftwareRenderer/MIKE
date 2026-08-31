package exec

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"kanban/backend/agent"
	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/llama"
	"kanban/backend/llmadapter"
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
	lspMgr        lspManager
	commitQueue   *CommitQueue
	gate          *ConcurrencyGate // per-LLM-request in-flight limit (incl. utility calls)
	taskGate      *ConcurrencyGate // per-provider running-task limit
	taskMu        sync.Mutex
	llmAdapter    *llmadapter.Adapter // provider request adaptation (llm extensions); nil = none
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

// SetLLMAdapter wires the provider request adapter (from enabled `llm`
// extensions). New LlamaClients created by this executor then adapt their
// requests to provider-specific formats. Nil = no adaptation.
func (te *TaskExecutor) SetLLMAdapter(a *llmadapter.Adapter) {
	te.llmAdapter = a
}

// newLlamaClient creates a LlamaClient with the provider request adapter
// attached.
func (te *TaskExecutor) newLlamaClient(serverURL, modelName, reasoningEffort, apiKey string) *llama.LlamaClient {
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	client.SetAdapter(te.llmAdapter)
	return client
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
	ctx             context.Context
	cancel          context.CancelFunc
	userMsgChan     chan stream.UserMessage
	summarizeCh     chan *summarizeRequest // send a request, receives result via resultCh
	activeSummarize struct {
		mu      sync.Mutex
		request *summarizeRequest // currently active summarize request (nil = none)
	}
	compactCh    chan chan *CompactResult // send a channel, receives result from agent compaction
	agentRunner  *agent.AgentRunner       // Agent runner — kept alive after completion for on-demand compaction
	runnerClosed bool
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
	sandboxEdit       editFunc
	// Sandbox operating mode (ro vs rw) at container creation time.
	sandboxOperatingMode string
	// Agent sandbox config hash at container creation time.
	agentSandboxHash string
	// os.Root handle for non-sandbox write isolation.
	root *os.Root
	// recoverBridge attempts to reconnect the sandbox bridge (client reconnect or
	// full sandbox recreation). Returns true if success. Only non-nil when sandbox is enabled.
	recoverBridge          func() bool
	bridgeRecoveryAttempts int
	// Closed by executor defer so ForwardUserMessage type-switch can wait for clean exit.
	done chan struct{}
	// Closed last by the executor defer
	finished chan struct{}
}

// modelConfig holds mutable model/provider settings for an executor.
type LSPDiagnosticPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type LSPDiagnosticRange struct {
	Start LSPDiagnosticPosition `json:"start"`
	End   LSPDiagnosticPosition `json:"end"`
}

type LSPRelatedInformation struct {
	Location struct {
		URI   string             `json:"uri"`
		Range LSPDiagnosticRange `json:"range"`
	} `json:"location"`
	Message string `json:"message"`
}

type LSPDiagnostic struct {
	Range              LSPDiagnosticRange      `json:"range"`
	Message            string                  `json:"message"`
	Severity           int                     `json:"severity"`
	Source             string                  `json:"source"`
	Code               interface{}             `json:"code"`
	RelatedInformation []LSPRelatedInformation `json:"relatedInformation"`
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
	// Normalize nil concrete to a nil interface so te.lspMgr == nil guards hold.
	var lspMgrI lspManager
	if lspMgr != nil {
		lspMgrI = lspMgr
	}
	te := &TaskExecutor{
		store:         store,
		configStore:   configStore,
		appStreamMgr:  appStreamMgr,
		worktreeMgr:   worktreeMgr,
		sandboxMgr:    sandboxMgr,
		agentRegistry: agentRegistry,
		lspMgr:        lspMgrI,
		commitQueue:   commitQueue,
		gate:          NewConcurrencyGate(),
		taskGate:      NewConcurrencyGate(),
		tasks:         make(map[string]*executionState),
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

// runToolProtected runs a tool call under the task's worktree lock (which
// serializes tool writes against subtask merges) and turns a panic into a failed
// result. The caller must always be able to emit and persist a tool_result — a tool
// call left without one renders as "Running" forever — and a stranded worktree lock
// would deadlock every later write for the task.
func (te *TaskExecutor) runToolProtected(taskID, toolName string, run func() (string, bool)) (result string, success bool) {
	mu := te.GetWorktreeMu(taskID)
	mu.Lock()
	defer mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[tool] panic while executing %s for task=%s: %v\n%s", toolName, taskID, r, debug.Stack())
			result = fmt.Sprintf("Error: internal error while running %s: %v", toolName, r)
			success = false
		}
	}()

	return run()
}

// WorktreeMgr returns the WorktreeManager. Used by server wiring.
func (te *TaskExecutor) WorktreeMgr() *git.WorktreeManager {
	return te.worktreeMgr
}

// RunWithGate executes fn while holding a concurrency gate slot for the given provider.
// Blocks until a slot is available. If the gate can't be acquired (e.g. timeout),
// the function is not run and the gate error is returned.
func (te *TaskExecutor) RunWithGate(ctx context.Context, serverURL string, fn func() error) error {
	concurrency := te.configStore.GetProviderConcurrency(serverURL)
	maxRPS := te.configStore.GetProviderMaxRequestsPerSecond(serverURL)
	if _, err := te.gate.Acquire(ctx, serverURL, concurrency, maxRPS, nil); err != nil {
		return err
	}
	defer te.gate.Release(serverURL)
	return fn()
}

// ChatCompletionOpts holds optional parameters for ChatCompletion.
type ChatCompletionOpts struct {
	MaxTokens       *int   // MaxTokens caps the response. nil = server default (no cap).
	ReasoningEffort string // effort override; "" = "off" (no provider lookup)
}

// utilityTarget resolves the configured Utility provider/model pair, falling
// back to the task's own values when unset. ok = Utility pair used.
func (te *TaskExecutor) utilityTarget(task *types.Task) (serverURL, model string, ok bool) {
	providerName, utilModel := te.configStore.GetUtility()
	if providerName != "" {
		if p := te.configStore.ResolveProvider(providerName); p != nil {
			return p.URL, utilModel, true
		}
	}
	return task.LlamaServerURL, task.ModelName, false
}

// ResolveUtilityTarget returns the URL/model for utility-type LLM calls,
// routing through the Utility provider/model with task fallback.
func (te *TaskExecutor) ResolveUtilityTarget(task *types.Task) (serverURL, model string) {
	serverURL, model, _ = te.utilityTarget(task)
	return serverURL, model
}

// automatedAnswers answers an auto-started subtask's questions in one LLM call
// using the parent's prompt context. Returns one answer per question.
func (te *TaskExecutor) automatedAnswers(ctx context.Context, subtask *types.Task, questions []string) ([]string, error) {
	if subtask.ParentTaskID == "" {
		return nil, errors.New("task has no parent; cannot auto-answer")
	}
	parent, err := te.store.GetTaskByID(subtask.ParentTaskID)
	if err != nil {
		return nil, fmt.Errorf("get parent task: %w", err)
	}
	if len(questions) == 0 {
		return nil, errors.New("no questions to answer")
	}

	systemPrompt, userPrompt, err := prompts.RenderAnswerQuestionPrompt(prompts.AnswerQuestionData{
		ParentTitle:  parent.Title,
		ParentPrompt: parent.Prompt,
		SubtaskTitle: subtask.Title,
		Questions:    questions,
	})
	if err != nil {
		return nil, fmt.Errorf("render answer prompt: %w", err)
	}

	// Resolve the endpoint: prefer the parent's config, then the subtask's, then the Utility pair.
	serverURL, model := parent.LlamaServerURL, parent.ModelName
	if serverURL == "" || model == "" {
		serverURL, model = subtask.LlamaServerURL, subtask.ModelName
	}
	if serverURL == "" || model == "" {
		serverURL, model = te.ResolveUtilityTarget(subtask)
	}

	messages := []types.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	_, answer, err := te.ChatCompletion(ctx, serverURL, model, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("automated answer completion: %w", err)
	}
	return splitNumberedAnswers(answer, len(questions))
}

// numberedMarker matches "N)" markers (single-line format, after whitespace)
// and "N." markers at line starts. Group 1 = paren number, group 2 = dot number.
var numberedMarker = regexp.MustCompile(`(?:^|\s)(\d+)\)|(?:^|\n)(\d+)\.`)

// leadingMarker matches a numbered marker at the very start of a response.
var leadingMarker = regexp.MustCompile(`^(\d+)\s*[\)\.]`)

// markerFollowedByDigit reports whether the char after a marker is a digit,
// i.e. the "marker" is really a decimal/version ("1.21", "2.5").
func markerFollowedByDigit(text string, end int) bool {
	return end < len(text) && text[end] >= '0' && text[end] <= '9'
}

// splitNumberedAnswers parses "1) a 2) b..." into n answers aligned by index.
// Only markers whose number matches the expected position (1..n) are accepted,
// so decimals/versions in answers don't split them and out-of-order numbering
// errors instead of silently misaligning.
func splitNumberedAnswers(response string, n int) ([]string, error) {
	if n == 0 {
		return nil, errors.New("no questions to answer")
	}
	text := strings.TrimSpace(response)
	if text == "" {
		return nil, errors.New("empty automated answer")
	}
	// Single question: only a leading marker delimits the answer; body numbers
	// ("Use Go 1.21") are content.
	if n == 1 {
		if m := leadingMarker.FindStringSubmatchIndex(text); m != nil && !markerFollowedByDigit(text, m[1]) {
			return []string{strings.TrimSpace(text[m[1]:])}, nil
		}
		return []string{text}, nil
	}
	locs := numberedMarker.FindAllStringSubmatchIndex(text, -1)
	// bounds[i] = content start after marker i, and marker i's match start
	// (the boundary where the previous segment ends).
	bounds := make([][2]int, 0, n)
	expected := 1
	for _, loc := range locs {
		if markerFollowedByDigit(text, loc[1]) {
			continue // decimal/version, not a marker
		}
		numStr := ""
		if loc[2] >= 0 {
			numStr = text[loc[2]:loc[3]]
		} else {
			numStr = text[loc[4]:loc[5]]
		}
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		if num == expected {
			bounds = append(bounds, [2]int{loc[1], loc[0]})
			expected++
			if expected > n {
				break
			}
		} else if num > expected {
			// Out-of-order or missing answer — refuse to guess.
			return nil, fmt.Errorf("automated answer has out-of-order numbering (marker %d, expected %d)", num, expected)
		}
		// num < expected: duplicate/spurious marker — ignore.
	}
	if len(bounds) == 0 {
		return nil, fmt.Errorf("unparseable automated answer: no numbered markers (expected %d answers)", n)
	}
	if len(bounds) < n {
		return nil, fmt.Errorf("automated answer has %d segment(s), expected %d", len(bounds), n)
	}
	segments := make([]string, 0, n)
	for i, b := range bounds {
		end := len(text)
		if i+1 < len(bounds) {
			end = bounds[i+1][1]
		}
		if seg := strings.TrimSpace(text[b[0]:end]); seg != "" {
			segments = append(segments, seg)
		}
	}
	if len(segments) < n {
		return nil, fmt.Errorf("automated answer has %d segment(s), expected %d", len(segments), n)
	}
	return segments[:n], nil
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
		}
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := te.newLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
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
	client := te.newLlamaClient(serverURL, modelName, "off", apiKey)
	var usage *types.ChatUsage
	err := te.RunWithGate(ctx, serverURL, func() error {
		var err error
		// onUsage nil: the only caller streams tokens and ignores usage.
		usage, err = client.ChatStream(ctx, messages, nil, cb, onProgress, nil)
		return err
	})
	return usage, err
}

// -- Subtask auto-start methods --

// ChildrenAllDone reports whether every child in the slice is in the "done"
// status. An empty slice (no children) counts as done.
func ChildrenAllDone(children []types.Task) bool {
	for i := range children {
		if children[i].Status != "done" {
			return false
		}
	}
	return true
}

// AllChildrenDone reports whether all of the task's direct children are done
// (no children counts as done).
func (te *TaskExecutor) AllChildrenDone(taskID string) (bool, error) {
	children, err := te.store.GetChildren(taskID)
	if err != nil {
		return false, err
	}
	return ChildrenAllDone(children), nil
}

// IsStartableNow reports whether a task can run its own execution now:
// startable board state and all children done.
func (te *TaskExecutor) IsStartableNow(t types.Task) bool {
	if !isIncompleteForStart(t) {
		return false
	}
	done, err := te.AllChildrenDone(t.ID)
	return err == nil && done
}

// FindDeepestStartableSubtasks returns the startable subtasks at the deepest
// level of the tree (startable board state and all children done). Subtrees
// rooted at a non-startable task (review, failed) are skipped.
func (te *TaskExecutor) FindDeepestStartableSubtasks(tasks []types.Task) []types.Task {
	type levelTask struct {
		task  types.Task
		depth int
	}
	var startable []levelTask
	var walk func(tasks []types.Task, depth int)
	walk = func(tasks []types.Task, depth int) {
		for _, t := range tasks {
			if !isIncompleteForStart(t) {
				// Not startable (review, failed, done) — skip its subtree.
				continue
			}
			children, err := te.store.GetChildren(t.ID)
			if err != nil || len(children) == 0 {
				// Leaf (or unreadable children) — startable as-is.
				startable = append(startable, levelTask{t, depth})
				continue
			}
			if ChildrenAllDone(children) {
				startable = append(startable, levelTask{t, depth})
				continue
			}
			// Still has pending children — walk down to them.
			walk(children, depth+1)
		}
	}
	walk(tasks, 1)
	if len(startable) == 0 {
		return nil
	}
	maxDepth := 0
	for _, s := range startable {
		if s.depth > maxDepth {
			maxDepth = s.depth
		}
	}
	var deepest []types.Task
	for _, s := range startable {
		if s.depth == maxDepth {
			deepest = append(deepest, s.task)
		}
	}
	return deepest
}

// isIncompleteForStart reports whether a task is in a startable board state:
// backlog, or in_progress and not running/failed. Board state only —
// IsStartableNow additionally requires all children done.
func isIncompleteForStart(t types.Task) bool {
	if t.Status == "backlog" {
		return true
	}
	if t.Status == "in_progress" && t.ExecutionState != "running" && t.ExecutionState != "failed" {
		return true
	}
	return false
}

// SelectSubtaskToStart picks the subtask to start: the sole deepest startable
// one, or the LLM's choice when several tie (first on LLM failure). Empty
// string if none is startable.
func (te *TaskExecutor) SelectSubtaskToStart(parent *types.Task, subtasks []types.Task) string {
	deepest := te.FindDeepestStartableSubtasks(subtasks)
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

// StartSubtask starts the executor for the specified subtask,
// inheriting the parent's LLM config and ensuring the parent's branch. It does
// not move the parent or its siblings — the caller places the chain.
// autostarted records whether this start came from autonomous sibling continuation.
func (te *TaskExecutor) StartSubtask(projectID, subtaskID string, autostarted bool) error {
	subtask, err := te.store.GetTaskByID(subtaskID)
	if err != nil {
		return fmt.Errorf("get subtask: %w", err)
	}
	if subtask.ParentTaskID == "" {
		return fmt.Errorf("task has no parent")
	}

	parent, err := te.store.GetTaskByID(subtask.ParentTaskID)
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
		if err := te.storeTask(subtask, projectID); err != nil {
			return fmt.Errorf("update subtask model config: %w", err)
		}
	}

	// Ensure the parent has a branch (needed for subtask merge target).
	// This does NOT change the parent's status — it stays in its current column.
	if parent.Branch == "" {
		if err := te.EnsureParentBranch(projectID, parent); err != nil {
			return fmt.Errorf("ensure parent branch: %w", err)
		}
		parent, _ = te.store.GetTaskByID(subtask.ParentTaskID)
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
						_ = te.storeTask(parent, projectID)
						te.appStreamMgr.BroadcastTaskUpdated(projectID, parent)
					}
				}
			}
		}
	}

	te.appStreamMgr.BroadcastTaskUpdated(projectID, subtask)

	// Start the subtask executor
	return te.StartTask(projectID, subtaskID, autostarted)
}

// EnsureParentBranch creates the parent task's branch when the parent has not started
// yet (parent.Branch == ""). The branch is the merge target once a subtask finishes
// with auto_merge.
//
// A full worktree is created, not a bare branch, so that when the parent eventually
// starts, prepareTaskExecution finds the worktree and reuses it.
//
// The parent's status is NOT changed — it stays in its column (e.g. backlog) while
// subtasks run; only Branch is set. A parent that already has a branch is left alone.
func (te *TaskExecutor) EnsureParentBranch(projectID string, parent *types.Task) error {
	project, err := te.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}

	if parent.Branch != "" {
		// verify it actually exists in git.
		if te.branchExists(projectID, parent.Branch) {
			return nil // branch exists, nothing to do
		}
		// Branch missing in git — create it from HEAD so the stored name becomes valid.
		cmd := osexec.Command("git", "-C", project.Path, "branch", parent.Branch, "HEAD")
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[ensure-parent-branch] task=%s branch %q missing in git, create failed: %s", parent.ID, parent.Branch, string(out))
			return fmt.Errorf("create missing parent branch %q: %w", parent.Branch, err)
		}
		log.Printf("[ensure-parent-branch] task=%s recovered missing branch %q from HEAD", parent.ID, parent.Branch)
		return nil
	}

	branchName := te.generateBranchName(parent, parent.ID, projectID)

	// Atomic set prevents double worktree creation from concurrent callers.
	set, err := te.store.SetTaskBranchIfEmpty(parent.ID, projectID, branchName)
	if err != nil {
		return fmt.Errorf("set parent branch: %w", err)
	}
	if !set {
		latest, err := te.store.GetTaskByID(parent.ID)
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
	latest, _ := te.store.GetTaskByID(parent.ID)
	if latest.Branch != "" && latest.Branch != branchName {
		branchName = latest.Branch
	}
	parent.Branch = branchName
	parent.UpdatedAt = time.Now().UTC()
	if err := te.storeTask(parent, projectID); err != nil {
		// Clean up the worktree if DB update fails
		_ = te.worktreeMgr.RemoveWorktree(projectID, parent.ID, branchName)
		return fmt.Errorf("update parent task with branch: %w", err)
	}

	log.Printf("[ensure-parent-branch] task=%s created worktree with branch %s for parent", parent.ID, branchName)
	return nil
}

// AutoStartNextSibling continues the autostart chain after a task finishes:
// start the next startable sibling (deepest first, LLM picks when several), or
// the parent itself once all its children are done — but only if the parent was
// already started (in_progress); a Backlog parent is never autostarted. A child
// in review/failed halts that branch, while the scheduler bubbles up so the
// ancestor's other branches can still progress.
func (te *TaskExecutor) AutoStartNextSibling(projectID, finishedTaskID, parentTaskID string) error {
	parent, err := te.store.GetTaskByID(parentTaskID)
	if err != nil {
		return fmt.Errorf("get parent task: %w", err)
	}
	// Only continue under an in_progress or backlog parent that hasn't completed.
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

	// Pending siblings (startable board state; the finished task excluded).
	var pending []types.Task
	for _, c := range children {
		if c.ID == finishedTaskID {
			continue
		}
		// Skip a sibling that already has a live executor (running, or queued
		// as "Pending" in_progress+idle in parallel mode) — restarting it would
		// cancel its slot and drop its queue position. No-op in sequential mode,
		// where siblings are never pre-started.
		if te.hasLiveExecutor(c.ID) {
			continue
		}
		if isIncompleteForStart(c) {
			pending = append(pending, c)
		}
	}

	allDone := ChildrenAllDone(children)

	if len(pending) > 0 {
		// A Backlog parent was never started by the user — no sibling autostart.
		if parent.Status == "backlog" {
			log.Printf("[auto-start] parent %s is in backlog, skipping sibling auto-start (%d pending siblings)", parentTaskID, len(pending))
			return nil
		}

		selectedID := te.SelectSubtaskToStart(parent, pending)
		if selectedID == "" {
			// Nothing startable — the branch is blocked (e.g. child in review).
			// Bubble up so the ancestor's other branches can progress.
			return te.autoStartAncestorSiblings(projectID, parent, finishedTaskID)
		}

		// Reload to avoid stale state — prepareTaskExecution has its own guard.
		selected, err := te.store.GetTaskByID(selectedID)
		if err != nil || !te.IsStartableNow(*selected) {
			log.Printf("[auto-start] task=%s selected sibling %s no longer startable, skipping", finishedTaskID, selectedID)
			return nil
		}

		log.Printf("[auto-start] task=%s finished, starting next sibling %s (parent=%s)", finishedTaskID, selectedID, parentTaskID)
		return te.StartSubtask(projectID, selectedID, true)
	}

	// No pending siblings.
	if allDone {
		// All children done: autostart the parent only if the user started it.
		if parent.Status == "in_progress" {
			log.Printf("[auto-start] task=%s all children done, starting parent %s", finishedTaskID, parent.ID)
			if err := te.StartTask(projectID, parent.ID, true); err != nil {
				log.Printf("[auto-start] task=%s failed to start parent %s: %v", finishedTaskID, parent.ID, err)
			} else {
				started, _ := te.store.GetTaskByID(parent.ID)
				if started != nil {
					te.appStreamMgr.BroadcastTaskUpdated(projectID, started)
				}
			}
		} else {
			log.Printf("[auto-start] task=%s all children done, parent %s in backlog — waiting for user to start", finishedTaskID, parent.ID)
		}
		return nil
	}

	// Children still unfinished — bubble up to the ancestor's other branches.
	return te.autoStartAncestorSiblings(projectID, parent, finishedTaskID)
}

// autoStartAncestorSiblings bubbles up one level so the waiting parent's
// sibling branches can progress. The parent is passed as finishedTaskID to
// exclude it from its own sibling list. Bubbling stops at a Backlog ancestor
// (never started by the user).
func (te *TaskExecutor) autoStartAncestorSiblings(projectID string, parent *types.Task, finishedTaskID string) error {
	if parent.ParentTaskID == "" {
		return nil // root parent, no more levels
	}
	grandparent, err := te.store.GetTaskByID(parent.ParentTaskID)
	if err != nil {
		return nil
	}
	if grandparent.Status == "backlog" {
		log.Printf("[auto-start] ancestor %s is in backlog, skipping auto-start", grandparent.ID)
		return nil
	}
	log.Printf("[auto-start] task=%s parent %s waiting on children, recursing up to %s", finishedTaskID, parent.ID, parent.ParentTaskID)
	return te.AutoStartNextSibling(projectID, parent.ID, parent.ParentTaskID)
}

// InitTask creates the worktree and branch for a task without starting LLM execution.
// This enables the terminal and file explorer before the task is started.
// It is idempotent — calling it on an already-initialized task is a no-op.
func (te *TaskExecutor) InitTask(projectID, taskID string) (*types.Task, error) {
	task, err := te.store.GetTaskByID(taskID)
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
		task, err = te.store.GetTaskByID(taskID)
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
		parent, err := te.store.GetTaskByID(task.ParentTaskID)
		if err == nil && parent.Branch == "" {
			if err := te.EnsureParentBranch(projectID, parent); err != nil {
				return nil, fmt.Errorf("ensure parent branch: %w", err)
			}
		}
	}

	// If this is a child task, branch from the parent's actual worktree branch.
	sourceBranch := ""
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByID(task.ParentTaskID)
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
		// Roll back orphaned branch name so later calls don't see a stale
		// branch with no corresponding git ref (would cause "not a valid
		// commit, skipping merge" errors).
		_ = te.store.ClearTaskBranch(taskID, projectID)
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
	if err := te.storeTask(task, projectID); err != nil {
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
	serverURL             string
	modelName             string
	ctx                   context.Context
	cancel                context.CancelFunc
	userMsgChan           chan stream.UserMessage
	summarizeCh           chan *summarizeRequest // channel for manual summarization requests
	initialContent        string                 // user message content for lazy-start (empty for normal start)
	regenerate            bool
	sandboxEnabled        bool
	sandboxDockerfilePath string
	sandboxOpts           sandboxmgr.SandboxOpts
	done                  chan struct{} // closed by executor defer; see executionState.done
	finished              chan struct{} // closed after the executor's last DB write
	// taskSlotURL is the provider URL whose running-task slot this execution
	// currently holds ("" = not held). LLM providers only; agent providers
	// manage their own concurrency.
	taskSlotURL string
}

// projectName returns the project name used for prompt assembly. Tolerates a nil
// project (unit-test setups); only the sandboxed container cwd uses it.
func (s *taskExecSetup) projectName() string {
	if s.project == nil {
		return ""
	}
	return s.project.Name
}

// prepareTaskExecution sets up the shared infrastructure for task execution.
// Both StartTask and handleLazyStart use this to avoid duplicating worktree
// creation, branch watching, status updates, and sandbox configuration.
// Returns nil if the task is already actively running (caller should check execSetup == nil).
func (te *TaskExecutor) prepareTaskExecution(projectID, taskID string, overrideServerURL, overrideModelName string, updateStatus bool) (*taskExecSetup, error) {
	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return nil, err
	}

	if task.Summarizing {
		te.dbSummarizeCancel.mu.Lock()
		_, inFlight := te.dbSummarizeCancel.tasks[taskID]
		te.dbSummarizeCancel.mu.Unlock()
		if !inFlight {
			task.Summarizing = false
		}
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
		parent, err := te.store.GetTaskByID(task.ParentTaskID)
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
		parent, err := te.store.GetTaskByID(task.ParentTaskID)
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
	// Cancel any earlier executor for this task and let it finish writing before
	// claiming the row, so its stale copy can't land after our "running".
	waitForExecutorExit(taskID, te.cancelExistingExecutor(taskID), executorExitGrace)

	if updateStatus {
		task.ExecutionState = "running"
		task.Status = "in_progress"
		if err := te.storeTaskClaiming(task, projectID); err != nil {
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
		_ = te.storeTask(task, projectID)
		return nil, fmt.Errorf("agent requires network to be enabled")
	}

	te.taskMu.Lock()
	execType := "llm"
	if te.configStore.IsAgentProvider(serverURL) {
		execType = "agent"
	}
	doneCh := make(chan struct{})
	finishedCh := make(chan struct{})
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan, summarizeCh: summarizeCh, compactCh: make(chan chan *CompactResult, 16), projectName: project.Name, executorType: execType, taskStatus: task.Status, done: doneCh, finished: finishedCh}
	te.taskMu.Unlock()

	return &taskExecSetup{
		task:                  task,
		project:               project,
		projectID:             projectID,
		worktreePath:          worktreePath,
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
		done:     doneCh,
		finished: finishedCh,
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

	task, err := te.store.GetTaskByID(taskID)
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
		finished:     setup.finished,
	}
	te.tasks[taskID] = state
	te.taskMu.Unlock()

	return runner, nil
}

// StartTask begins AI execution for a task. autostarted records whether this
// execution was started autonomously from a parent (sibling continuation) rather
// than by a direct user action.
func (te *TaskExecutor) StartTask(projectID, taskID string, autostarted bool) error {
	execSetup, err := te.prepareTaskExecution(projectID, taskID, "", "", true)
	if err != nil {
		return err
	}
	task := execSetup.task

	// Persist how this execution was started (backend-only provenance flag).
	task.Autostarted = autostarted
	if err := te.storeTask(task, projectID); err != nil {
		execSetup.cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		return fmt.Errorf("persist autostarted flag: %w", err)
	}

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

	// The conversation already ends with the assistant, so the agent is done and waits for
	// the user — that is review. Leaving it in_progress would show "Pending" with nothing
	// pending: this start has no work to do.
	if len(existingMsgs) > 0 && existingMsgs[len(existingMsgs)-1].Role == "assistant" {
		log.Printf("[start] task=%s last message is from assistant, skipping LLM call -> review (awaiting user)", taskID)
		execSetup.cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		if fresh, err := te.store.GetTask(projectID, taskID); err == nil {
			fresh.Status = "review"
			fresh.ExecutionState = "idle"
			fresh.Output = "Nothing to run — the agent has the last word and is waiting for your reply"
			fresh.UpdatedAt = time.Now().UTC()
			_ = te.storeTask(fresh, projectID)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, fresh)
		}
		return nil
	}

	// Agents send their own status:running (Pi via agent_start).
	if !te.configStore.IsAgentProvider(execSetup.serverURL) {
		te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})
	}

	go te.executeTask(execSetup)
	return nil
}

// StopTask cancels the task's live executor and transitions the task row out of its in-flight state
func (te *TaskExecutor) StopTask(taskID string) error {
	te.taskMu.Lock()
	state, ok := te.tasks[taskID]
	if ok {
		// Close agent runner if it's still alive (kept after completion for compaction).
		if state.agentRunner != nil && !state.runnerClosed {
			state.agentRunner.Close()
			state.runnerClosed = true
		}
		state.taskStatus = ""
		state.cancel()
		delete(te.tasks, taskID)
	}
	te.taskMu.Unlock()

	// Wait for the cancelled executor to finish writing before transitioning.
	// Transitioning first let its stale copy put "running" back with no executor
	// behind it, leaving the task stuck until a second Stop. No-op with no live
	// executor (state is nil).
	waitForExecutorExit(taskID, state, executorExitGrace)

	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		return err
	}
	if applyStopTransition(task) {
		te.persistStopTransition(task)
	}
	te.sendStopStatusEvent(task.ProjectID, taskID, task.ExecutionState)
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
		if state, ok := te.tasks[task.ID]; ok {
			state.cancel()
			delete(te.tasks, task.ID)
			stopped++
			log.Printf("[stop-project-tasks] task=%s project=%s stopped (settings change)", task.ID, projectID)
			te.taskMu.Unlock()
			// Same ordering as StopTask.
			waitForExecutorExit(task.ID, state, executorExitGrace)
			// Reset only in-flight tasks (in_progress → paused); preserve the column.
			if applyStopTransition(&task) {
				te.persistStopTransition(&task)
			}
			te.sendStopStatusEvent(projectID, task.ID, task.ExecutionState)
		} else {
			te.taskMu.Unlock()
		}
	}
	return stopped
}

// RecoverStaleTasks transitions every in-flight task row to its post-stop state at server startup
func (te *TaskExecutor) RecoverStaleTasks() int {
	inFlight, err := te.store.GetInFlightTasks()
	if err != nil {
		log.Printf("[recover] list in-flight tasks failed: %v", err)
		return 0
	}
	recovered := 0
	for _, task := range inFlight {
		oldState := task.ExecutionState
		if !applyStopTransition(&task) {
			continue
		}
		te.taskMu.Lock()
		// A task may have been restarted before this recovery ran (autostart
		// from a sibling chain); never clobber a live executor's state.
		_, activelyRunning := te.tasks[task.ID]
		te.taskMu.Unlock()
		if activelyRunning {
			continue
		}
		// Atomic compare-and-set: a task autostarted (sibling chain) between the
		// scan and this write keeps its live in-flight state.
		if !te.persistStopTransition(&task) {
			continue
		}
		log.Printf("[recover] task=%s stale %s/%s -> %s/%s", task.ID, task.Status, oldState, task.Status, task.ExecutionState)
		recovered++
	}
	if recovered > 0 {
		log.Printf("[recover] %d stale in-flight task(s) recovered", recovered)
	}
	// Now the rows that were never in flight but still show as pending.
	te.normalizePendingTasks()
	return recovered
}

// normalizePendingTasks moves tasks that rest in In Progress with execution_state idle
// back to review, where a finished turn waiting for the user belongs. It runs at startup,
// when nothing can be mid-queue, and skips what is legitimately pending: a live executor,
// a parent with unfinished subtasks, and a thread whose newest row is a user message —
// work that never ran is a dropped message, not a finished turn, so it is only reported.
func (te *TaskExecutor) normalizePendingTasks() int {
	pending, err := te.store.GetPendingIdleTasks()
	if err != nil {
		log.Printf("[recover] list pending tasks failed: %v", err)
		return 0
	}
	normalized := 0
	for _, task := range pending {
		te.taskMu.Lock()
		_, live := te.tasks[task.ID]
		te.taskMu.Unlock()
		if live {
			continue
		}
		if children, cerr := te.store.GetChildren(task.ID); cerr == nil && len(children) > 0 && !ChildrenAllDone(children) {
			log.Printf("[recover] task=%s pending while subtasks are unfinished — left as is", task.ID)
			continue
		}
		last, lerr := te.store.GetLastMessageInThread(task.CurrentThreadID)
		if lerr != nil {
			log.Printf("[recover] task=%s last message lookup failed: %v", task.ID, lerr)
			continue
		}
		if last == nil || last.Role == "user" {
			log.Printf("[recover] task=%s pending with an unanswered user message — left as is", task.ID)
			continue
		}
		fresh, gerr := te.store.GetTask(task.ProjectID, task.ID)
		if gerr != nil {
			continue
		}
		fresh.Status = "review"
		fresh.ExecutionState = "idle"
		fresh.Output = "Nothing to run — the agent has the last word and is waiting for your reply"
		fresh.UpdatedAt = time.Now().UTC()
		if perr := te.storeTask(fresh, task.ProjectID); perr != nil {
			log.Printf("[recover] task=%s pending normalization failed: %v", task.ID, perr)
			continue
		}
		log.Printf("[recover] task=%s pending with %s holding the last word -> review/idle", task.ID, last.Role)
		normalized++
	}
	if normalized > 0 {
		log.Printf("[recover] %d task(s) moved to review: pending with nothing pending", normalized)
	}
	return normalized
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

// hasLiveExecutor reports whether the task currently has an active executor —
// either running or blocked waiting for a concurrency slot (queued as
// "Pending" in_progress+idle). Used by the autostart chain to avoid restarting
// a sibling that was already started in parallel mode.
func (te *TaskExecutor) hasLiveExecutor(taskID string) bool {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	_, ok := te.tasks[taskID]
	return ok
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
	if task, _ := te.store.GetTaskByID(taskID); task != nil && task.Summarizing {
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
	te.storeTask(task, projectID)
	return te.StartTask(projectID, taskID, false)
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

	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		log.Printf("[lazy-start] task=%s get error: %v", taskID, err)
		return
	}
	// Apply model override from the user message (may arrive before updateTask completes)
	if reasoningEffort != "" {
		task.ReasoningEffort = reasoningEffort
		te.storeTask(task, projectID)
	}

	execSetup, err := te.prepareTaskExecution(projectID, taskID, serverURL, modelName, true)
	if err != nil {
		log.Printf("[lazy-start] task=%s setup failed: %v", taskID, err)
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = err.Error()
		_ = te.storeTask(task, projectID)
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

	// Signal the executor (re)started so clients arm the "Awaiting LLM" indicator
	if !te.configStore.IsAgentProvider(execSetup.serverURL) {
		te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})
	}

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
		task, _ := te.store.GetTaskByID(taskID)
		if err := te.MergeBranch(projectID, taskID, task); err != nil {
			log.Printf("[auto-commit] task=%s merge failed: %v", taskID, err)
			return err
		}
	} else {
		log.Printf("[auto-commit] task=%s merge skipped (auto_merge=false)", taskID)
	}
	return nil
}

// syncTargetIntoTask merges the target branch into the task's branch (in the task's
// worktree) when the target has new commits since the task diverged, so conflicts
// surface while the LLM still has the task's changes in context.
//
// Returns nil on a clean merge, when there is nothing to merge, or when merge_conflict
// state was entered. MergeBranch must check task.ExecutionState afterwards and skip the
// final merge on "merge_conflict".
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
		te.logSystemMessage(taskID, task.CurrentThreadID, "info",
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
	te.logSystemMessage(taskID, task.CurrentThreadID, "warning",
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
		parent, err := te.store.GetTaskByID(task.ParentTaskID)
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
		recovered := te.recoverMissingBranch(projectID, taskID, taskBranch)
		if recovered != "" {
			taskBranch = recovered
		}
		// If still missing, skip the merge.
		if taskBranch == "" || !te.branchExists(projectID, taskBranch) {
			log.Printf("[merge] task=%s branch %q is not a valid commit, skipping merge: %v", taskID, taskBranch, err)
			return nil
		}
	}

	// Determine target branch and the git directory to run merge in.
	// For child tasks, the target is ALWAYS the parent's branch (never origin).
	// For root tasks, the target is the origin branch.
	var targetBranch string
	var mergeInWorktree string // non-empty: run merge in this worktree path
	if task.ParentTaskID != "" {
		parent, err := te.store.GetTaskByID(task.ParentTaskID)
		if err != nil {
			log.Printf("[merge] task=%s parent_task_id=%s not found: %v, aborting merge", taskID, task.ParentTaskID, err)
			te.logSystemMessage(taskID, task.CurrentThreadID, "error",
				fmt.Sprintf("Error: parent task %s not found — merge aborted.", task.ParentTaskID))
			return fmt.Errorf("parent task %s not found", task.ParentTaskID)
		}
		// Child tasks MUST merge into their parent's branch, never origin.
		// If the parent has no branch yet, create it now.
		if parent.Branch == "" {
			log.Printf("[merge] task=%s parent %s has no branch, creating it now", taskID, parent.ID)
			if err := te.EnsureParentBranch(projectID, parent); err != nil {
				log.Printf("[merge] task=%s failed to create parent branch: %v, aborting merge", taskID, err)
				te.logSystemMessage(taskID, task.CurrentThreadID, "error",
					fmt.Sprintf("Error: could not create branch for parent task %q — merge aborted: %v", parent.Title, err))
				return fmt.Errorf("create parent branch: %w", err)
			}
			parent, _ = te.store.GetTaskByID(task.ParentTaskID)
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
				// A concurrent merge may have created it between our check and
				// now — re-verify before failing.
				recheck := osexec.Command("git", "-C", project.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+targetBranch)
				if recheck.Run() == nil {
					log.Printf("[merge] task=%s branch %q created by a concurrent merge, proceeding", taskID, targetBranch)
				} else {
					log.Printf("[merge] task=%s failed to create branch %q from %q: %s", taskID, targetBranch, startPoint, string(out))
					te.logSystemMessage(taskID, task.CurrentThreadID, "error",
						fmt.Sprintf("Error: could not create branch %q from %q — merge skipped.", targetBranch, startPoint))
					return fmt.Errorf("create branch %q: %w", targetBranch, err)
				}
			} else {
				log.Printf("[merge] task=%s created branch %q from %q", taskID, targetBranch, startPoint)
			}
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
	task, _ = te.store.GetTaskByID(taskID)
	if task.HasMergeConflict {
		log.Printf("[merge] task=%s sync has merge conflict, skipping final merge", taskID)
		return nil
	}

	if mergeInWorktree != "" {
		err := te.mergeInWorktree(projectID, mergeInWorktree, targetBranch, taskBranch, taskID, task.ParentTaskID)
		if err == nil && task.ParentTaskID != "" {
			te.broadcastParentDiffStats(projectID, task.ParentTaskID)
		}
		return err
	}
	// Root tasks only reach mergeInProject (child tasks always merge into the
	// parent's worktree above). The worktree routing for the target branch
	// happens inside it, under the project merge lock.
	err = te.mergeInProject(projectID, targetBranch, taskBranch, taskID)
	if err == nil && task.ParentTaskID != "" {
		te.broadcastParentDiffStats(projectID, task.ParentTaskID)
	}
	return err
}

// mergeInProject performs a merge in the project's main directory.
func (te *TaskExecutor) mergeInProject(projectID, targetBranch, taskBranch, taskID string) error {
	project, _ := te.store.GetProject(projectID)
	// Serialize against other merges on this project's main checkout: it has a
	// single HEAD/index, so interleaved merges clobber each other's checkout,
	// stashes, and restore. Held for the whole sequence below (capture →
	// merge → restore → stash pop). Lock order: before the project stash mutex.
	te.worktreeMgr.GetProjectMergeMu(projectID).Lock()
	defer te.worktreeMgr.GetProjectMergeMu(projectID).Unlock()
	// Prune stale worktree registrations so a deleted worktree doesn't block checkout.
	te.worktreeMgr.GitRunInProject(projectID, "worktree", "prune")
	// If the target branch is checked out in a worktree, merge there instead.
	// This check must run under the merge lock: an in-flight main-checkout
	// merge temporarily checks the target out in the main checkout, which an
	// unlocked scan would mis-route to (skipping both the lock and the
	// checkout restore).
	if wtPath := te.findWorktreeForBranch(projectID, targetBranch); wtPath != "" {
		return te.mergeInWorktree(projectID, wtPath, targetBranch, taskBranch, taskID, "")
	}
	// The merge temporarily switches the project's main checkout to the target
	// branch. Remember what it is on now so the merge can put it back — the
	// user's working checkout is theirs, not the merge's.
	return te.mergeInto(projectID, project.Path, targetBranch, taskBranch, taskID, currentCheckoutRef(project.Path))
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
	// Worktrees must stay on their own branch — no checkout restore.
	return te.mergeInto(projectID, worktreePath, targetBranch, taskBranch, taskID, "")
}

// currentCheckoutRef returns the ref (branch name, or SHA when detached) that
// dir's HEAD is on. Returns "" when it cannot be determined — in that case the
// merge leaves the checkout where it ends up, as before.
func currentCheckoutRef(dir string) string {
	cmd := osexec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" || ref == "HEAD" {
		// Detached HEAD — remember the exact commit.
		cmd = osexec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = dir
		out, err = cmd.Output()
		if err != nil {
			return ""
		}
		ref = strings.TrimSpace(string(out))
	}
	return ref
}

// mergeInto performs a merge of taskBranch into targetBranch inside the given directory.
// Both mergeInProject and mergeInWorktree delegate here — the only difference between them
// is the directory in which git commands are run. When restoreRef is non-empty and differs
// from targetBranch, dir's checkout is switched back to restoreRef after the merge,
// preserving the caller's checkout state.
func (te *TaskExecutor) mergeInto(projectID, dir, targetBranch, taskBranch, taskID, restoreRef string) error {
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
	// Restore the caller's checkout after the merge. Registered after the
	// stash-pop defer above, so (LIFO) it runs first and the stashed changes
	// pop back onto the user's own branch rather than the merge target.
	if restoreRef != "" && restoreRef != targetBranch {
		defer func() {
			if _, err := te.worktreeMgr.GitRunInDirOutput(dir, "checkout", "-f", restoreRef); err != nil {
				log.Printf("[merge] task=%s failed to restore checkout to %s: %v", taskID, restoreRef, err)
			}
		}()
	}

	if err := te.worktreeMgr.GitRunInDir(dir, "checkout", "-f", targetBranch); err != nil {
		return fmt.Errorf("checkout target branch %s: %w", targetBranch, err)
	}

	mergeOut, mergeErr := te.worktreeMgr.GitRunInDirOutput(dir, "merge", taskBranch, "--no-edit")
	if mergeErr != nil {
		// Merge conflict — detect, set flag for LLM resolution, then abort
		task, _ := te.store.GetTaskByID(taskID)
		if resolveErr := te.tryResolveMergeConflicts(dir, taskID, targetBranch, taskBranch, task); resolveErr != nil {
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
		task, _ := te.store.GetTaskByID(taskID)
		te.logMergeComplete(taskID, task.CurrentThreadID, taskBranch, targetBranch)
	}
	return nil
}

// -- Helper functions --

var wordRe = regexp.MustCompile(`\S+`)

// composeSystemPrompt builds a task's system prompt, embedding the most recent
// conversation summary (empty for a task that was never summarized)
func composeSystemPrompt(task *types.Task, worktreePath, projectName string, sandboxEnabled bool, summary string) string {
	cwd := worktreePath
	skillsMount := ""
	if sandboxEnabled {
		cwd = sandboxmgr.MountPath(projectName)
		skillsMount = sandboxmgr.SkillsMount
	}
	return llama.BuildSystemPrompt(worktreePath, cwd, summary, skillsMount, task.OperatingMode)
}

// buildMessagesFromDB loads persisted messages and builds the system prompt.
// The most recent summary is injected into the system prompt.
func buildMessagesFromDB(task *types.Task, taskMsgs []types.TaskMessage, worktreePath, projectName string, sandboxEnabled bool) []types.ChatMessage {
	// Extract most recent summary for the system prompt.
	var summary string
	for i := len(taskMsgs) - 1; i >= 0; i-- {
		if taskMsgs[i].Role == "summarization" {
			summary = taskMsgs[i].Content
			break
		}
	}

	messages := []types.ChatMessage{{Role: "system", Content: composeSystemPrompt(task, worktreePath, projectName, sandboxEnabled, summary)}}

	// One rebuild path for every consumer of persisted rows (see groupTaskMessages): the
	// system prompt on front, the conversation — tool calls with their assistant row, tool
	// results right after, advisories under their own role — behind it.
	messages = append(messages, groupTaskMessages(taskMsgs)...)
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

// recoverMissingBranch looks for a stale/orphaned branch with the same slug
// pattern when the expected branch is missing. Returns the recovered branch
// name (and updates the task in DB), or empty string if no match found.
func (te *TaskExecutor) recoverMissingBranch(projectID, taskID, missingBranch string) string {
	slugPrefix := types.TaskBranchSlugPrefix(missingBranch)
	if slugPrefix == "" {
		return ""
	}

	// List all branches matching the pattern.
	branchesOut, err := te.worktreeMgr.GitRunInProjectOutput(projectID, "branch", "--list", slugPrefix+"*")
	if err != nil {
		return ""
	}

	var matches []string
	for line := range strings.SplitSeq(branchesOut, "\n") {
		b := strings.TrimSpace(line)
		// Strip " + " prefix from git branch --list output (marks checked-out branch).
		b = strings.TrimPrefix(b, " + ")
		b = strings.TrimPrefix(b, "+ ")
		if b != "" && b != missingBranch {
			matches = append(matches, b)
		}
	}
	if len(matches) == 0 {
		return ""
	}

	// If multiple matches, pick the most recent one (by commit date).
	var recovered string
	if len(matches) == 1 {
		recovered = matches[0]
	} else {
		// Pick the branch with the most recent commit.
		var latestTime time.Time
		for _, b := range matches {
			tsOut, err := te.worktreeMgr.GitRunInProjectOutput(projectID, "log", "-1", "--format=%ct", b)
			if err != nil {
				continue
			}
			if ts, err := strconv.ParseInt(strings.TrimSpace(tsOut), 10, 64); err == nil {
				t := time.Unix(ts, 0)
				if t.After(latestTime) {
					latestTime = t
					recovered = b
				}
			}
		}
		if recovered == "" {
			recovered = matches[0] // fallback to first
		}
	}

	if err := te.store.SetTaskBranch(taskID, projectID, recovered); err != nil {
		log.Printf("[merge] task=%s failed to update branch from %q to %q: %v", taskID, missingBranch, recovered, err)
		return ""
	}
	log.Printf("[merge] task=%s recovered missing branch %q → %q", taskID, missingBranch, recovered)
	return recovered
}

// randomSuffix generates a random alphanumeric string of length n.
func randomSuffix(n int) string {
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// randomSuffixLen is the random tail of a task branch name — see
// types.TaskBranchSuffixLen, which the branch-name parser matches against it.
const randomSuffixLen = types.TaskBranchSuffixLen

// generateBranchName creates a unique git branch name for a task.
// Uses a slugified title when possible (e.g. "task/fix-login-bug"),
// falls back to the task ID (e.g. "task/frost-amber-quartz"),
// and appends a short suffix if the branch already exists from a deleted task.
// The total branch name (including prefix) is capped at types.MaxBranchName.
func (te *TaskExecutor) generateBranchName(task *types.Task, taskID, projectID string) string {
	baseName := types.TaskBranchPrefix + task.ID
	if !task.AutoMerge && task.Title != "" {
		slug := types.SlugifyBranchName(task.Title)
		if slug != "" {
			baseName = types.TaskBranchPrefix + slug
		}
	}
	// Always append random suffix to avoid collisions with stale branches.
	// Truncate base to leave room for "-" + randomSuffixLen chars.
	suffixSpace := 1 + randomSuffixLen // "-" + 6 chars
	if len(baseName)+suffixSpace > types.MaxBranchName {
		baseName = baseName[:types.MaxBranchName-suffixSpace]
	}
	branchName := baseName + "-" + randomSuffix(randomSuffixLen)

	// Safety net: if this random name somehow collides, append taskID chars.
	wtPath, wtErr := te.worktreeMgr.WorktreePath(taskID)
	_, wtStatErr := os.Stat(wtPath)
	wtExists := wtErr == nil && wtStatErr == nil
	if !wtExists && te.branchExists(projectID, branchName) {
		extra := "-" + taskID[:8]
		if len(branchName)+len(extra) <= types.MaxBranchName {
			branchName += extra
		}
	}
	return branchName
}

func agentSandboxHash(sb *types.AgentSandbox) string {
	if sb == nil {
		return ""
	}
	b, _ := json.Marshal(sb)
	return string(b)
}

// formatLSPDiagnostics formats context for LLM follow-up action
func (te *TaskExecutor) formatLSPDiagnostics(filePath string, diagnostics []LSPDiagnostic) string {
	var issues []string
	for i, d := range diagnostics {
		// Include errors (1) and warnings (2); skip info (3) and hints (4)
		if d.Severity > 2 {
			continue
		}
		label := "error"
		if d.Severity == 2 {
			label = "warning"
		}
		// Build source+code tag, e.g. "[gopls S1002]" or "[eslint]"
		var tag string
		parts := []string{}
		if d.Source != "" {
			parts = append(parts, d.Source)
		}
		if d.Code != nil {
			switch c := d.Code.(type) {
			case string:
				parts = append(parts, c)
			case float64:
				parts = append(parts, fmt.Sprintf("%d", int(c)))
			}
		}
		if len(parts) > 0 {
			tag = " [" + strings.Join(parts, " ") + "]"
		}
		msg := d.Message
		// Append related info if present, e.g. " (see: type defined at foo.go:10)"
		if len(d.RelatedInformation) > 0 {
			for _, r := range d.RelatedInformation {
				if r.Message != "" {
					relLine := r.Location.Range.Start.Line + 1
					relFile := filepath.Base(r.Location.URI)
					msg += fmt.Sprintf(" (see %s:%d: %s)", relFile, relLine, r.Message)
				}
			}
		}
		issues = append(issues, fmt.Sprintf("%d. line %d, col %d %s: %s (%s)", i+1, d.Range.Start.Line+1, d.Range.Start.Character+1, tag, msg, label))
	}
	if len(issues) == 0 {
		return ""
	}
	header := fmt.Sprintf("Fix issues in %s:", filePath)
	return header + "\n" + strings.Join(issues, "\n")
}

// lspHandshake performs the LSP initialize/initialized exchange for a workspace
// session and reports whether the server is ready to take document requests.
//
// It is called once per session, not once per file: `initialize` is a per-process
// request, and gopls answers a second one with an error — after which the server
// that is answering has never been told which workspace it is looking at.
func (te *TaskExecutor) lspHandshake(ctx context.Context, proc *lsp.LSPProcess, subCh chan json.RawMessage, worktreePath, lang string, cfg *lsp.ServerConfig, taskID string) bool {
	// Step 1: LSP initialize (required before any textDocument requests)
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"processId":        os.Getpid(),
			"clientInfo":       map[string]any{"name": "kanban-lsp-diag", "version": "1.0"},
			"rootUri":          "file://" + worktreePath,
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
		return false
	}

	// Wait for initialize response
	initTimer := time.After(10 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-initTimer:
			log.Printf("[lsp-diag] task=%s initialize timeout", taskID)
			return false
		case raw, ok := <-subCh:
			if !ok {
				return false
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
					return false
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
		return false
	}
	return true
}

func (te *TaskExecutor) triggerLSPDiagnostics(ctx context.Context, projectID, taskID, filePath string) string {
	// Bound this file's whole pass. Derived from the caller's ctx, so stopping the task
	// ends it; given a deadline, so a silent language server cannot park this goroutine.
	ctx, cancel := context.WithTimeout(ctx, lspDiagnosticsBudget)
	defer cancel()

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
	proc, err := te.lspMgr.GetOrCreate(ctx, worktreePath, lang, cfg)
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

	// Step 1: LSP initialize handshake (required before any textDocument requests).
	// Skipped when this session is already initialized: `initialize` is per-process,
	// and a server that is asked twice answers the second request with an error and
	// never learns about this workspace again.
	if !te.lspMgr.SessionInitialized(worktreePath, lang) {
		if !te.lspHandshake(ctx, proc, subCh, worktreePath, lang, cfg, taskID) {
			return ""
		}
		te.lspMgr.MarkSessionInitialized(worktreePath, lang)
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
			// A server that cannot load the project answers with confident guesses
			// about it. Drop those before they reach the model; keep everything else.
			env := te.lspMgr.Environment(worktreePath, relPath)
			findings := filterUnresolvable(env, envelope.Params.Diagnostics)
			if len(findings) < len(envelope.Params.Diagnostics) {
				log.Printf("[lsp-diag] task=%s suppressed %d unresolvable finding(s): %s", taskID,
					len(envelope.Params.Diagnostics)-len(findings), env.Note)
			}
			errMsg := te.formatLSPDiagnostics(relPath, findings)
			if errMsg != "" {
				log.Printf("[lsp-diag] task=%s diagnostic hint: %s", taskID, errMsg)
				return errMsg
			}
			log.Printf("[lsp-diag] task=%s no errors found (diagnostics may be warnings only)", taskID)
			return ""
		}
	}
}
