// Package exec is the execution engine: a task's LLM or agent loop, from accepting a user
// message until the run settles, including tool execution, summarization, sandbox setup,
// merge conflicts, checkpoints and the provider gates.
// The loop never touches a ResponseWriter, so a turn keeps running - and reporting over the
// task's WebSocket - whatever the browser does.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
	"kanban/backend/errs"
	"kanban/pkg/keylock"
	"kanban/pkg/logging"
)

// TaskExecutor handles task execution endpoints and orchestrates AI task execution.
//
// A concern is split out of this type by moving its methods onto a collaborator struct that
// NewTaskExecutor constructs and holds — never by splitting executionState: its lock discipline
// depends on the sandbox fields being read as one snapshot (sandbox, setSandbox), and a state
// owned by two groups gets its fields captured piecemeal — a mix of old and new sandbox
// callbacks reads a stopped container through a live client.
//
// A collaborator is constructed with exactly the dependencies its own functions use, plus:
//   - a lock as a release func — func() func(), or func(string) func() per task — never a
//     *sync.Mutex, for the reason LockWorktree gives;
//   - *executionState only when its concern owns the fields it reads, and otherwise neither it
//     nor a *TaskExecutor;
//   - no way to take taskMu or sandboxMu. A collaborator that needs state they guard gets a func
//     value whose body takes the lock here, on this side of the seam.
//
// Outside callers only ever see TaskExecutor, so a split keeps a one-line delegator for each
// name they use.
type TaskExecutor struct {
	store         *store.Store
	configStore   *config.ConfigStore
	appStreamMgr  *stream.AppStreamManager
	worktreeMgr   *git.WorktreeManager
	sandboxMgr    *sandbox.SandboxManager
	agentRegistry *agent.AgentRegistry
	// Whether a task's sandbox container holds a given binary. A test injects a recorder: which
	// name the pre-flight asks about is the behaviour under test.
	agentBinaries agentBinaries
	lspMgr        lspManager
	commitQueue   *CommitQueue
	gate          *ConcurrencyGate // per-LLM-request in-flight limit (incl. utility calls)
	taskGate      *ConcurrencyGate // per-provider running-task limit
	taskMu        sync.Mutex
	llmAdapter    *llmadapter.Adapter // provider request adaptation (llm extensions); nil = none
	tasks         map[string]*executionState
	// Per-task mutexes to serialize worktree access across executors and merges.
	worktreeMu taskWorktreeLocks // taskID -> the task's own worktree lock
	// Branch and merge behaviour of a finished turn. Its lock discipline is in mergeOrchestrator.
	merge *mergeOrchestrator
	// The task's context window: the threshold decision, the rolling summary, the manual paths.
	// Its lock discipline is in summarizeOrchestrator.
	summarize *summarizeOrchestrator
}

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

// CompactResult holds the result of an agent compact command.
type CompactResult struct {
	Summary string
	Error   error
}

// executionState is everything one running task holds between an executor taking it and its
// defer releasing it. Locking, because these fields are read from the executor loop, HTTP
// handlers and tool calls:
//
//   - sandboxMu guards the sandboxExec/sandboxExecStream/sandboxRead/sandboxWrite/sandboxEdit/
//     sandboxOperatingMode/agentSandboxHash/recoverBridge group. Read it with sandbox() and write
//     it with setSandbox(), never field by field. sandboxMu is a leaf: never held while taking
//     taskMu.
//   - activeSummarize has its own mutex, for the same one-field-one-lock reason.
//   - taskMu (on TaskExecutor) guards everything else here together with the tasks map:
//     runnerClosed, taskStatus, lastDiffHash, bridgeRecoveryAttempts. A method that reached
//     this state through the map already holds it.
//   - ctx, cancel and the channels are set once at construction, so they need no lock.
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
	createdIDs   []string     // task IDs created by create_task during this execution
	executorType string       // "llm" or "agent" — tracks which executor type is active
	taskStatus   types.Status // task status at start; cleared by StopTask so HasRunningTasks can skip it
	// sandboxEnabled is the task's project setting, fixed for the run. executeToolInner refuses
	// a sandboxed task's host fallbacks with it.
	sandboxEnabled bool
	// lastDiffHash tracks the last broadcast diff hash to avoid duplicate broadcasts
	// when fsnotify already caught the same change.
	lastDiffHash string
	// Sandbox I/O callbacks — nil means the task's project runs without a sandbox, so the tool
	// call works on the host instead. These six fields plus recoverBridge are guarded by
	// sandboxMu; see the type comment.
	sandboxMu         sync.Mutex
	sandboxExec       execFunc
	sandboxExecStream execStreamFunc
	sandboxRead       readFunc
	sandboxWrite      writeFunc
	sandboxEdit       editFunc
	// sandboxOperatingMode (ro vs rw) and agentSandboxHash record what the container was created
	// with; each turn compares them against the live config to decide whether to recreate it.
	sandboxOperatingMode types.OperatingMode
	agentSandboxHash     string
	// recoverBridge attempts to reconnect the sandbox bridge (client reconnect or full sandbox
	// recreation). Returns true if success. Only non-nil when sandbox is enabled.
	recoverBridge func() bool
	// bridgeRecoveryAttempts counts bridge-recovery tries against maxAttempts. Guarded by taskMu.
	bridgeRecoveryAttempts int
	// Closed by executor defer so ForwardUserMessage type-switch can wait for clean exit.
	done chan struct{}
	// Closed last by the executor defer
	finished chan struct{}
}

// sandboxState is a consistent snapshot of the sandbox fields of an executionState. Setup,
// an operating-mode change, an agent-config change and bridge recovery each replace the whole set
// at once, and a tool call that saw half the old callbacks and half the new ones would read a
// stopped container through a live client — which looks like a broken pipe and gets retried forever.
type sandboxState struct {
	exec            execFunc
	execStream      execStreamFunc
	read            readFunc
	write           writeFunc
	edit            editFunc
	operatingMode   types.OperatingMode
	agentConfigHash string
	recoverBridge   func() bool
}

// sandbox returns a copy of the sandbox fields. Hold the copy rather than re-reading per statement:
// a tool call then runs against one set of callbacks even if the bridge is swapped out mid-call.
func (st *executionState) sandbox() sandboxState {
	st.sandboxMu.Lock()
	defer st.sandboxMu.Unlock()
	return st.sandboxLocked()
}

// resetBridgeRecoveryAttempts clears the bridge-recovery failure latch. It takes the state pointer,
// not a task ID: the pointer stays valid for the task's lifetime, and a state that is not in the
// map still has a latch that must clear. taskMu is taken because it guards these fields.
func (te *TaskExecutor) resetBridgeRecoveryAttempts(state *executionState) {
	te.taskMu.Lock()
	state.bridgeRecoveryAttempts = 0
	te.taskMu.Unlock()
}

// setSandbox writes the sandbox fields through change, all under sandboxMu, so a caller cannot
// publish a new set of callbacks without also publishing the mode and hash that go with them.
func (st *executionState) setSandbox(change func(sb *sandboxState)) {
	st.sandboxMu.Lock()
	defer st.sandboxMu.Unlock()
	sb := st.sandboxLocked()
	change(&sb)
	st.sandboxExec = sb.exec
	st.sandboxExecStream = sb.execStream
	st.sandboxRead = sb.read
	st.sandboxWrite = sb.write
	st.sandboxEdit = sb.edit
	st.sandboxOperatingMode = sb.operatingMode
	st.agentSandboxHash = sb.agentConfigHash
	st.recoverBridge = sb.recoverBridge
}

// sandboxLocked builds the snapshot. Callers hold sandboxMu. Shared by sandbox() and setSandbox()
// so the field list exists once.
func (st *executionState) sandboxLocked() sandboxState {
	return sandboxState{
		exec:            st.sandboxExec,
		execStream:      st.sandboxExecStream,
		read:            st.sandboxRead,
		write:           st.sandboxWrite,
		edit:            st.sandboxEdit,
		operatingMode:   st.sandboxOperatingMode,
		agentConfigHash: st.agentSandboxHash,
		recoverBridge:   st.recoverBridge,
	}
}

// LSPDiagnosticPosition is one point of a diagnostic's range, in the LSP wire
// shape (line and character, both 0-based) the language server publishes.
type LSPDiagnosticPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LSPDiagnosticRange is the span a diagnostic covers, in the same wire shape.
type LSPDiagnosticRange struct {
	Start LSPDiagnosticPosition `json:"start"`
	End   LSPDiagnosticPosition `json:"end"`
}

// LSPRelatedInformation is one related location attached to a diagnostic.
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
	Code               any                     `json:"code"`
	RelatedInformation []LSPRelatedInformation `json:"relatedInformation"`
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

func NewTaskExecutor(store *store.Store, configStore *config.ConfigStore, appStreamMgr *stream.AppStreamManager, worktreeMgr *git.WorktreeManager, sandboxMgr *sandbox.SandboxManager, agentRegistry *agent.AgentRegistry, lspMgr *lsp.ExtensionLSPManager, commitQueue *CommitQueue) *TaskExecutor {
	// Normalize nil concrete to a nil interface so te.lspMgr == nil guards hold.
	var lspMgrI lspManager
	if lspMgr != nil {
		lspMgrI = lspMgr
	}
	// Same reason for the pre-flight's container question: a nil *SandboxManager stored in the
	// interface is not nil, and the pre-flight must read "no sandbox" as "not available".
	var binaries agentBinaries
	if sandboxMgr != nil {
		binaries = sandboxMgr
	}
	te := &TaskExecutor{
		store:         store,
		configStore:   configStore,
		appStreamMgr:  appStreamMgr,
		worktreeMgr:   worktreeMgr,
		sandboxMgr:    sandboxMgr,
		agentRegistry: agentRegistry,
		agentBinaries: binaries,
		lspMgr:        lspMgrI,
		commitQueue:   commitQueue,
		gate:          NewConcurrencyGate(),
		taskGate:      NewConcurrencyGate(),
		tasks:         make(map[string]*executionState),
	}
	te.worktreeMu.Configure(taskLockSlowWait, func(taskID string, waited time.Duration) {
		logging.For(logging.Tools).With(logging.TaskID, taskID).Warnf(
			"waited %s for the task's worktree lock - a tool call holds it", waited.Round(time.Second))
	})
	te.merge = te.newMergeOrchestrator()
	te.summarize = te.newSummarizeOrchestrator()
	te.appStreamMgr.SetLazyStartHandler(te.handleLazyStart)
	return te
}

// taskWorktreeLocks is the domain that serializes everything touching one task's worktree.
type taskWorktreeLocks struct{ keylock.Table }

// taskLockSlowWait is deliberately far above the git-family threshold: this lock is held across an
// entire tool call — `read_file`, `edit_file` and `bash`, the last of which runs up to 300 s — and
// `mergeInWorktree`/`syncTargetIntoTask` wait on it by design. Waiting behind a running tool is the
// intended behaviour, so the only thing worth saying is when the wait reaches minutes.
const taskLockSlowWait = 60 * time.Second

// LockWorktree acquires the per-task worktree lock and returns the function that releases it. The lock
// itself never leaves the package: a returned *sync.Mutex can be stored, copied and handed on, and a
// caller holding a copy of it serializes against nobody who holds the original.
func (te *TaskExecutor) LockWorktree(taskID string) func() {
	return te.worktreeMu.Lock(taskID)
}

// LockWorktreeWatched is LockWorktree with the wait reported, for a caller a person is waiting on.
func (te *TaskExecutor) LockWorktreeWatched(taskID string, every time.Duration, notice keylock.WaitNotice) func() {
	return te.worktreeMu.LockWatched(taskID, every, notice)
}

// runToolProtected runs a tool call under the task's worktree lock (which
// serializes tool writes against subtask merges) and turns a panic into a failed
// result. The caller must always be able to emit and persist a tool_result — a tool
// call left without one renders as "Running" forever — and a stranded worktree lock
// would deadlock every later write for the task.
func (te *TaskExecutor) runToolProtected(taskID, toolName string, run func() (string, bool)) (result string, success bool) {
	defer te.LockWorktree(taskID)()

	defer func() {
		if r := recover(); r != nil {
			logging.For(logging.Tools).Warnf("panic while executing %s for task=%s: %v\n%s", toolName, taskID, r, debug.Stack())
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
		if children[i].Status != types.StatusDone {
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
	if t.Status == types.StatusBacklog {
		return true
	}
	if t.Status == types.StatusInProgress && t.ExecutionState != types.ExecStateRunning && t.ExecutionState != types.ExecStateFailed {
		return true
	}
	return false
}

// SelectSubtaskToStart picks the subtask to start: the sole deepest startable
// one, or the LLM's choice when several tie (first on LLM failure). Empty
// string if none is startable.
//
// A start path should prefer TaskHandler.subtasksToStart (backend/managers),
// which honors the "run siblings in parallel" setting by returning every
// startable subtask when that is on; use this directly only when a single pick
// is genuinely wanted.
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
		logging.For(logging.Exec).With(logging.TaskID, parent.ID).Infof("no LLM config, using fallback")
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
		logging.For(logging.Exec).With(logging.TaskID, parent.ID).Warnf("LLM call failed: %v, using fallback", err)
		return deepest[0].ID
	}

	selectedID := strings.TrimSpace(response)
	// Validate the returned ID matches one of the deepest subtasks
	for _, s := range deepest {
		if s.ID == selectedID {
			logging.For(logging.Exec).With(logging.TaskID, parent.ID).Infof("LLM selected subtask %s", selectedID)
			return selectedID
		}
	}

	logging.For(logging.Exec).With(logging.TaskID, parent.ID).Warnf("LLM returned invalid ID %q, using fallback", selectedID)
	return deepest[0].ID
}

// StartSubtask starts the executor for the specified subtask,
// inheriting the parent's LLM config and ensuring the parent's branch. It does
// not move the parent or its siblings — the caller places the chain.
// autostarted is true when the subtask runs on its parent's behalf, with no human present to
// answer ask_question; false when a user started this task and is waiting on it.
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
		if err := te.merge.EnsureParentBranch(projectID, parent); err != nil {
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

// AutoStartNextSibling continues the autostart chain after a task finishes: start the next
// startable sibling (deepest first, the LLM picks when several), or the parent once all its
// children are done — but only a parent already in_progress, never a Backlog one. A child in
// review/failed halts that branch while the scheduler bubbles up, so an ancestor's other
// branches still progress.
func (te *TaskExecutor) AutoStartNextSibling(projectID, finishedTaskID, parentTaskID string) error {
	parent, err := te.store.GetTaskByID(parentTaskID)
	if err != nil {
		return fmt.Errorf("get parent task: %w", err)
	}
	// Only continue under an in_progress or backlog parent that hasn't completed.
	if parent.Status != types.StatusInProgress && parent.Status != types.StatusBacklog {
		return nil
	}
	if parent.ExecutionState == types.ExecStateCompleted {
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
		if parent.Status == types.StatusBacklog {
			logging.For(logging.Startup).Infof("parent %s is in backlog, skipping sibling auto-start (%d pending siblings)", parentTaskID, len(pending))
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
			logging.For(logging.Startup).With(logging.TaskID, finishedTaskID).Infof("selected sibling %s no longer startable, skipping", selectedID)
			return nil
		}

		logging.For(logging.Startup).With(logging.TaskID, finishedTaskID).Infof("finished, starting next sibling %s (parent=%s)", selectedID, parentTaskID)
		return te.StartSubtask(projectID, selectedID, true)
	}

	// No pending siblings.
	if allDone {
		// All children done: autostart the parent only if the user started it.
		if parent.Status == types.StatusInProgress {
			logging.For(logging.Startup).With(logging.TaskID, finishedTaskID).Infof("all children done, starting parent %s", parent.ID)
			if err := te.StartTask(projectID, parent.ID, true); err != nil {
				logging.For(logging.Startup).With(logging.TaskID, finishedTaskID).Warnf("failed to start parent %s: %v", parent.ID, err)
			} else {
				started, _ := te.store.GetTaskByID(parent.ID)
				if started != nil {
					te.appStreamMgr.BroadcastTaskUpdated(projectID, started)
				}
			}
		} else {
			logging.For(logging.Startup).With(logging.TaskID, finishedTaskID).Infof("all children done, parent %s in backlog — waiting for user to start", parent.ID)
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
	if grandparent.Status == types.StatusBacklog {
		logging.For(logging.Startup).Infof("ancestor %s is in backlog, skipping auto-start", grandparent.ID)
		return nil
	}
	logging.For(logging.Startup).With(logging.TaskID, finishedTaskID).Infof("parent %s waiting on children, recursing up to %s", parent.ID, parent.ParentTaskID)
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

	branchName := te.merge.generateBranchName(task, taskID, projectID)

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

	sourceBranch, err := te.merge.branchChildFrom(projectID, task)
	if err != nil {
		return nil, err
	}

	_, baseRef, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, sourceBranch)
	if err != nil {
		// Roll back the branch name: this path persists it before the worktree exists,
		// so a stale name here would later read as "not a valid commit, skipping merge".
		_ = te.store.ClearTaskBranch(taskID, projectID)
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	task.Branch = branchName
	task.BaseRef = baseRef
	te.pinBaselineAndWatch(projectID, task)

	task.UpdatedAt = time.Now().UTC()
	if err := te.storeTask(task, projectID); err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}

	logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("created worktree with branch %s", branchName)
	return task, nil
}

// pinBaselineAndWatch records the base ref's commit hash and starts diff watching on it.
// The hash is taken now because a fast-forward merge moves the branch pointer, and a
// done-task diff needs the original commit. Watching the commit keeps card stats matching
// the panel. Both are no-ops without a base ref.
func (te *TaskExecutor) pinBaselineAndWatch(projectID string, task *types.Task) {
	if task.BaseCommit == "" && task.BaseRef != "" {
		if project, _ := te.store.GetProject(projectID); project != nil {
			c := git.Command("", "-C", project.Path, "rev-parse", task.BaseRef)
			if out, err := c.CombinedOutput(); err == nil {
				task.BaseCommit = strings.TrimSpace(string(out))
			}
		}
	}

	watchBaseRef := task.BaseRef
	if task.BaseCommit != "" {
		watchBaseRef = task.BaseCommit
	}
	te.appStreamMgr.WatchWorktree(task.ID, projectID, watchBaseRef)
}

// taskExecSetup holds the shared state prepared for task execution.
type taskExecSetup struct {
	task           *types.Task
	project        *types.Project
	projectID      string
	worktreePath   string
	serverURL      string
	modelName      string
	ctx            context.Context
	cancel         context.CancelFunc
	userMsgChan    chan stream.UserMessage
	summarizeCh    chan *summarizeRequest // channel for manual summarization requests
	initialContent string                 // user message content for lazy-start (empty for normal start)
	regenerate     bool
	sandboxEnabled bool
	sandboxOpts    sandbox.SandboxOpts
	done           chan struct{} // closed by executor defer; see executionState.done
	finished       chan struct{} // closed after the executor's last DB write
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

// ErrTaskAlreadyRunning means another executor holds this task right now. It classifies as
// errs.ErrConflict, which is what the transport maps to 409; returning a fresh error with the same text
// answered 500 for a condition the user caused.
var ErrTaskAlreadyRunning = errs.Conflict("task already running")

// ErrAgentNeedsNetwork is a configuration the person using MIKE can change — enable network for the
// sandboxed project — so it declares a conflict and its sentence reaches the browser. Returned raw, the
// answer was 500: a server fault for something the user has to fix.
var ErrAgentNeedsNetwork = errs.Conflict("agent requires network to be enabled")

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
		te.summarize.dbSummarizeCancel.mu.Lock()
		_, inFlight := te.summarize.dbSummarizeCancel.tasks[taskID]
		te.summarize.dbSummarizeCancel.mu.Unlock()
		if !inFlight {
			task.Summarizing = false
		}
	}

	// Reset stale running state (executor crashed/server restarted)
	if task.ExecutionState == types.ExecStateRunning {
		te.taskMu.Lock()
		_, activelyRunning := te.tasks[taskID]
		te.taskMu.Unlock()
		if activelyRunning {
			return nil, ErrTaskAlreadyRunning
		}
		logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("stale running state detected, resetting")
		task.ExecutionState = types.ExecStateIdle
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
		branchName = te.merge.generateBranchName(task, taskID, projectID)
	}
	// A child branches from its parent, which also gives the parent a branch if it has none.
	sourceBranch, err := te.merge.branchChildFrom(projectID, task)
	if err != nil {
		return nil, err
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
	te.pinBaselineAndWatch(projectID, task)

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
		task.ExecutionState = types.ExecStateRunning
		task.Status = types.StatusInProgress
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
		task.ExecutionState = types.ExecStateIdle
		task.Status = types.StatusBacklog
		task.UpdatedAt = time.Now().UTC()
		_ = te.storeTask(task, projectID)
		return nil, ErrAgentNeedsNetwork
	}

	te.taskMu.Lock()
	execType := "llm"
	if te.configStore.IsAgentProvider(serverURL) {
		execType = "agent"
	}
	doneCh := make(chan struct{})
	finishedCh := make(chan struct{})
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan, summarizeCh: summarizeCh, compactCh: make(chan chan *CompactResult, 16), projectName: project.Name, sandboxEnabled: project.SandboxEnabled, executorType: execType, taskStatus: task.Status, done: doneCh, finished: finishedCh}
	te.taskMu.Unlock()

	return &taskExecSetup{
		task:           task,
		project:        project,
		projectID:      projectID,
		worktreePath:   worktreePath,
		serverURL:      serverURL,
		modelName:      modelName,
		ctx:            ctx,
		cancel:         cancel,
		userMsgChan:    userMsgChan,
		summarizeCh:    summarizeCh,
		sandboxEnabled: project.SandboxEnabled,
		sandboxOpts: sandbox.SandboxOpts{
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
	agentName, ok := types.ParseAgentURL(task.LlamaServerURL)
	if !ok {
		return nil, fmt.Errorf("invalid agent URL: %s", task.LlamaServerURL)
	}

	// Lookup agent extension via registry
	ext := te.agentRegistry.LookupByName(agentName)
	if ext == nil {
		return nil, errs.Conflict(fmt.Sprintf("agent %q is not installed", agentName))
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
		ctx:            setup.ctx,
		cancel:         setup.cancel,
		userMsgChan:    setup.userMsgChan,
		summarizeCh:    setup.summarizeCh,
		compactCh:      make(chan chan *CompactResult, 16),
		agentRunner:    runner,
		projectName:    setup.project.Name,
		sandboxEnabled: setup.sandboxEnabled,
		executorType:   "agent",
		taskStatus:     task.Status,
		done:           setup.done,
		finished:       setup.finished,
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
		logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("last message is from assistant, skipping LLM call -> review (awaiting user)")
		execSetup.cancel()
		te.taskMu.Lock()
		delete(te.tasks, taskID)
		te.taskMu.Unlock()
		if fresh, err := te.store.GetTask(projectID, taskID); err == nil {
			fresh.Status = types.StatusReview
			fresh.ExecutionState = types.ExecStateIdle
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
			logging.For(logging.Exec).With(logging.TaskID, task.ID).Infof("project=%s stopped (settings change)", projectID)
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
		logging.For(logging.Recovery).Warnf("list in-flight tasks failed: %v", err)
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
		logging.For(logging.Recovery).With(logging.TaskID, task.ID).Infof("stale %s/%s -> %s/%s", task.Status, oldState, task.Status, task.ExecutionState)
		recovered++
	}
	if recovered > 0 {
		logging.For(logging.Recovery).Infof("%d stale in-flight task(s) recovered", recovered)
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
		logging.For(logging.Recovery).Warnf("list pending tasks failed: %v", err)
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
			logging.For(logging.Recovery).With(logging.TaskID, task.ID).Infof("pending while subtasks are unfinished — left as is")
			continue
		}
		last, lerr := te.store.GetLastMessageInThread(task.CurrentThreadID)
		if lerr != nil {
			logging.For(logging.Recovery).With(logging.TaskID, task.ID).Warnf("last message lookup failed: %v", lerr)
			continue
		}
		if last == nil || last.Role == "user" {
			logging.For(logging.Recovery).With(logging.TaskID, task.ID).Infof("pending with an unanswered user message — left as is")
			continue
		}
		fresh, gerr := te.store.GetTask(task.ProjectID, task.ID)
		if gerr != nil {
			continue
		}
		fresh.Status = types.StatusReview
		fresh.ExecutionState = types.ExecStateIdle
		fresh.Output = "Nothing to run — the agent has the last word and is waiting for your reply"
		fresh.UpdatedAt = time.Now().UTC()
		if perr := te.storeTask(fresh, task.ProjectID); perr != nil {
			logging.For(logging.Recovery).With(logging.TaskID, task.ID).Warnf("pending normalization failed: %v", perr)
			continue
		}
		logging.For(logging.Recovery).With(logging.TaskID, task.ID).Infof("pending with %s holding the last word -> review/idle", last.Role)
		normalized++
	}
	if normalized > 0 {
		logging.For(logging.Recovery).Infof("%d task(s) moved to review: pending with nothing pending", normalized)
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
		if _, ok := te.tasks[task.ID]; ok && task.Status == types.StatusInProgress {
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
			logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("executor loop is dead, restarting via lazy-start")
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
			logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("switching from %s to %s", state.executorType, requestedType)
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
			logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("channel full, dropping message")
		}
		te.taskMu.Unlock()
		return
	}
	te.taskMu.Unlock()
	// Block lazy-start while summarization is in progress to prevent
	// concurrent LLM calls. Message is already in DB from HandleSend.
	if task, _ := te.store.GetTaskByID(taskID); task != nil && task.Summarizing {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("blocking lazy-start: summarization in progress")
		te.summarize.blockedMessages.mu.Lock()
		te.summarize.blockedMessages.tasks[taskID] = struct{}{}
		te.summarize.blockedMessages.mu.Unlock()
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
		logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("channel full, dropping notification")
	}
}

// StopSandbox removes the sandbox container for a task (called on task deletion).
func (te *TaskExecutor) StopSandbox(taskID string) {
	if te.sandboxMgr != nil {
		if err := te.sandboxMgr.Stop(taskID); err != nil {
			logging.For(logging.Sandbox).With(logging.TaskID, taskID).Warnf("stop error: %v", err)
		}
	}
}

// RetryTask retries a failed task.
func (te *TaskExecutor) RetryTask(projectID, taskID string) error {
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return err
	}
	task.ExecutionState = types.ExecStateIdle
	te.storeTask(task, projectID)
	return te.StartTask(projectID, taskID, false)
}

// handleLazyStart is called when a user message arrives for a task with no active executor.
func (te *TaskExecutor) handleLazyStart(projectID, taskID, content, modelName, serverURL, reasoningEffort string, regenerate bool) {
	logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("project=%s (override: model=%s, url=%s, effort=%s, regenerate=%v)", projectID, modelName, serverURL, reasoningEffort, regenerate)

	te.taskMu.Lock()
	if _, exists := te.tasks[taskID]; exists {
		te.taskMu.Unlock()
		return
	}
	te.taskMu.Unlock()

	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		logging.For(logging.Startup).With(logging.TaskID, taskID).Warnf("get error: %v", err)
		return
	}
	// Apply model override from the user message (may arrive before updateTask completes)
	if reasoningEffort != "" {
		task.ReasoningEffort = reasoningEffort
		te.storeTask(task, projectID)
	}

	execSetup, err := te.prepareTaskExecution(projectID, taskID, serverURL, modelName, true)
	if err != nil {
		logging.For(logging.Startup).With(logging.TaskID, taskID).Warnf("setup failed: %v", err)
		task.ExecutionState = types.ExecStateFailed
		task.Status = types.StatusReview
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
			TargetBranch:    te.merge.ResolveMergeTarget(projectID, task),
			TaskTitle:       task.Title,
			TaskDescription: task.Prompt,
			ConflictedFiles: conflictedFiles,
		})
		if pErr != nil {
			logging.For(logging.Startup).With(logging.TaskID, taskID).Warnf("failed to render merge conflict prompt: %v", pErr)
		}
		conflictContext += "\nAfter resolving the merge, continue with the user's request."
		content = conflictContext + "\n\n--- User message ---\n\n" + content
		logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("merge conflict detected, injecting conflict context")
	}

	execSetup.initialContent = content // pass user message to executor
	execSetup.regenerate = regenerate
	logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("worktree=%s", execSetup.worktreePath)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("starting executor")

	// Signal the executor (re)started so clients arm the "Awaiting LLM" indicator
	if !te.configStore.IsAgentProvider(execSetup.serverURL) {
		te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})
	}

	go te.executeTask(execSetup)
}

// -- Helper functions --

// composeSystemPrompt builds a task's system prompt, embedding the most recent
// conversation summary (empty for a task that was never summarized)
func composeSystemPrompt(task *types.Task, worktreePath, projectName string, sandboxEnabled bool, summary string) string {
	cwd := worktreePath
	skillsMount := ""
	if sandboxEnabled {
		cwd = sandbox.MountPath(projectName)
		skillsMount = sandbox.SkillsMount
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

// getCompactCh returns the compact channel for a running task, or nil if not found.
func (te *TaskExecutor) getCompactCh(taskID string) chan chan *CompactResult {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	if state, ok := te.tasks[taskID]; ok {
		return state.compactCh
	}
	return nil
}

// runningState returns the executionState of a running task, or nil. Its body takes taskMu here
// so a collaborator can reach a running task without seeing the lock; what it may read through
// the result is the collaborator's own contract.
func (te *TaskExecutor) runningState(taskID string) *executionState {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	return te.tasks[taskID]
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
