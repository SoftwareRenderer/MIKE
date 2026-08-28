package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"kanban/backend/git"
	"kanban/backend/llama"
	"kanban/backend/prompts"
	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/stream"
	"kanban/backend/toolresult"
	"kanban/backend/types"

	sandbox "kanban/pkg/bridge"

	"github.com/google/uuid"
)

// imageExts is the set of file extensions considered image files (allowed in staging).
var imageExts = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".bmp": {},
	".svg": {}, ".ico": {}, ".tiff": {}, ".tif": {}, ".avif": {}, ".apng": {},
}

// isImageFile returns true if the file path has a known image extension.
func isImageFile(path string) bool {
	lower := strings.ToLower(filepath.Ext(path))
	_, ok := imageExts[lower]
	return ok
}

// unstageNewBinaryFiles unstages newly staged binary non-image files.
func unstageNewBinaryFiles(wm *git.WorktreeManager, taskID string) {
	out, err := wm.GitRunInWorktreeOutput(taskID, "diff", "--staged", "--numstat", "--diff-filter=A")
	if err != nil || len(strings.TrimSpace(out)) == 0 {
		return
	}
	paths := binaryPathsFromStaged(out)
	unstagePaths(wm, taskID, paths)
}

// Retry bounds for LLM streaming.
const (
	rateLimitRetryAttempts = 3
)

// backoffFor returns the duration to wait before the next LLM retry attempt
func backoffFor(attempt int, err error) time.Duration {
	var le *llama.LlamaError
	if errors.As(err, &le) && le.IsRateLimit() && le.RetryAfter > 0 {
		return le.RetryAfter + time.Second + jitter()
	}
	return time.Duration(1<<attempt) * 2 * time.Second // 2s, 4s, 8s, ...
}

// sendRetryHint persists the "Connection interrupted" retry notification and
// broadcasts it with role "system" so the frontend renders a system card.
func (te *TaskExecutor) sendRetryHint(projectID, taskID string, task *types.Task, streamErr error, attempt int) {
	retryMsg := fmt.Sprintf("Connection interrupted (%v), retrying... (attempt %d/%d)", streamErr, attempt, rateLimitRetryAttempts)
	log.Printf("[retry] setup.task=%s %s", taskID, retryMsg)
	retryMsgID := uuid.New().String()
	te.store.CreateTaskMessage(&types.TaskMessage{
		ID: retryMsgID, TaskID: taskID, ThreadID: task.CurrentThreadID, Role: "system",
		Content: retryMsg, CreatedAt: time.Now().UTC(),
	})
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeHint, Data: map[string]any{
		"id":         retryMsgID,
		"role":       "system",
		"content":    retryMsg,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}})
}

// jitter returns a random duration between 250ms and 500ms (inclusive)
func jitter() time.Duration {
	return 250*time.Millisecond + time.Duration(rand.Int64N(251))*time.Millisecond
}

// streamFlusher batches streaming events to reduce WebSocket write overhead.
type streamFlusher struct {
	mu             sync.Mutex
	tokenBuf       strings.Builder
	reasoningBuf   strings.Builder
	toolCallDeltas map[string]toolCallDelta // tool_id -> delta info
	projectID      string
	taskID         string
	sandboxEnabled bool
	send           func(projectID, taskID string, event types.StreamEvent)
	loopDetector   *loopDetector // for phrase repetition detection on tokens
}

// toolCallDelta holds buffered delta info for a single tool call.
type toolCallDelta struct {
	toolName      string
	displayName   string
	path          string
	sandbox       bool
	startArgLen   int    // arg length when first seen (0 = new, send full raw_args)
	currentArgLen int    // latest arg length
	args          string // full args string at currentArgLen
}

const streamThrottleInterval = 100 * time.Millisecond

func newStreamFlusher(projectID, taskID string, sandboxEnabled bool, send func(projectID, taskID string, event types.StreamEvent), detector *loopDetector) *streamFlusher {
	return &streamFlusher{
		toolCallDeltas: make(map[string]toolCallDelta),
		projectID:      projectID,
		taskID:         taskID,
		sandboxEnabled: sandboxEnabled,
		send:           send,
		loopDetector:   detector,
	}
}

// appendToken buffers a token for batched sending.
func (f *streamFlusher) appendToken(text string) {
	f.loopDetector.checkStreamingText(text)
	f.mu.Lock()
	f.tokenBuf.WriteString(text)
	f.mu.Unlock()
}

// appendReasoning buffers reasoning text for batched sending.
func (f *streamFlusher) appendReasoning(text string) {
	f.mu.Lock()
	f.reasoningBuf.WriteString(text)
	f.mu.Unlock()
}

// appendToolCall buffers a tool call delta for batched sending.
func (f *streamFlusher) appendToolCall(toolID, toolName, displayName, path, args string, argLen, prevLen int, isNew, sandbox bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.toolCallDeltas[toolID]
	if !ok {
		// First time seeing this tool_id — record start position.
		f.toolCallDeltas[toolID] = toolCallDelta{
			toolName:      toolName,
			displayName:   displayName,
			path:          path,
			sandbox:       sandbox,
			startArgLen:   prevLen,
			currentArgLen: argLen,
			args:          args,
		}
	} else if argLen > existing.currentArgLen {
		// Args grew — update to latest state.
		existing.currentArgLen = argLen
		existing.args = args
		existing.path = path
		f.toolCallDeltas[toolID] = existing
	}
}

// flush sends all buffered events. Must be called with f.mu held by caller
// OR from the ticker goroutine (which locks internally).
func (f *streamFlusher) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Flush buffered tokens as a single batched event.
	if f.tokenBuf.Len() > 0 {
		text := f.tokenBuf.String()
		f.tokenBuf.Reset()
		f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": text}})
	}

	// Flush buffered reasoning as a single batched event.
	if f.reasoningBuf.Len() > 0 {
		text := f.reasoningBuf.String()
		f.reasoningBuf.Reset()
		f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": text}})
	}

	// Flush buffered tool call deltas.
	if len(f.toolCallDeltas) > 0 {
		for toolID, d := range f.toolCallDeltas {
			data := map[string]any{
				"tool_id":           toolID,
				"tool_name":         d.toolName,
				"tool_display_name": d.displayName,
				"args":              nil,
				"path":              d.path,
				"sandbox":           d.sandbox,
			}
			if d.startArgLen == 0 {
				// New tool call — send full raw_args.
				data["raw_args"] = d.args
			} else if d.currentArgLen <= d.startArgLen || d.currentArgLen > len(d.args) {
				// Defensive: invalid/empty delta bounds — send full raw_args instead of
				// panicking on an out-of-range slice (e.g. slice bounds out of range [217:194]).
				data["raw_args"] = d.args
			} else {
				// Update — send delta from start position.
				data["raw_args_delta"] = d.args[d.startArgLen:d.currentArgLen]
			}
			// Include edit line counts in the tool_call event so they're available immediately.
			if d.toolName == "edit_file" {
				added, removed := countEditLines(d.args)
				data["lines_added"] = added
				data["lines_removed"] = removed
			}
			f.send(f.projectID, f.taskID, types.StreamEvent{Type: types.WSTypeToolCall, Data: data})
		}
		f.toolCallDeltas = make(map[string]toolCallDelta)
	}
}

// reset clears all buffered content. Called before retry.
func (f *streamFlusher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenBuf.Reset()
	f.reasoningBuf.Reset()
	f.toolCallDeltas = make(map[string]toolCallDelta)
}

// loopContext holds mutable state for the AI conversation loop.
type loopContext struct {
	messages         []types.ChatMessage // full conversation (all messages preserved)
	assistantContent strings.Builder
	reasoningContent strings.Builder
	pendingToolCalls []types.ChatToolCall
	prevArgLens      map[string]int // tool_id -> arg length, for detecting streaming updates
	finishReason     string         // LLM finish_reason from last stream ("stop", "tool_calls", etc.)
	detector         *loopDetector
	tracker          failureTracker
	mc               *modelConfig
	client           *llama.LlamaClient
	providerName     string
	contextWindow    *int
	systemPrompt     string
	sandboxEnabled   bool
	callStartTime    time.Time // start of current LLM call (for per-call Duration)
	usage            *types.ChatUsage
	sessionCost      float64 // accumulated API cost (USD) of completed calls for this task run
	checkpointIdx    int     // messages index of last checkpoint
}

// toolResult holds one executed tool call's outcome. Incomplete calls (empty
// ID/name) are skipped during execution, so toolResults is NOT index-aligned
// with lc.pendingToolCalls.
type toolResult struct {
	toolCallID string
	output     string
	messageID  string         // persisted tool_call message ID
	args       map[string]any // parsed tool args
	rawArgs    string         // raw tool args JSON
	success    bool
	duration   int64
}

// askQuestionResults maps pendingToolCalls indexes of complete ask_question
// calls to their toolResults index (incomplete calls have no result).
func askQuestionResults(pending []types.ChatToolCall, results []toolResult) map[int]int {
	m := make(map[int]int)
	ri := 0
	for i, tc := range pending {
		if tc.ID == "" || tc.Function.Name == "" {
			continue // incomplete call — skipped during execution, no result
		}
		if tc.Function.Name == "ask_question" {
			m[i] = ri
		}
		ri++
	}
	return m
}

func (te *TaskExecutor) executeTask(setup *taskExecSetup) {
	// Declare lc at function scope so the defer can reference it.
	var lc *loopContext
	defer func() {
		close(setup.done) // signal executor exit for ForwardUserMessage type-switch
		// Persist accumulated session cost for this task run
		if lc != nil {
			setup.task.SessionCost = lc.sessionCost
			_ = te.store.UpdateTaskSessionCost(setup.task.ID, lc.sessionCost)
		}
		// Release the running-task slot if still held (no-op when released at
		// an idle transition or never acquired — agent providers skip it).
		te.releaseTaskSlot(setup)

		if r := recover(); r != nil {
			log.Printf("[panic] setup.task=%s recovered from panic: %v", setup.task.ID, r)
			te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
			// Transition the task out of its in-flight state (in_progress →
			// paused) so a panic can't leave a stale "running" row.
			te.handleCancelledTask(setup.task.ID, setup.projectID)
		}
		// Close os.Root to release file descriptor (non-sandbox tasks)
		te.taskMu.Lock()
		if state, exists := te.tasks[setup.task.ID]; exists && state.root != nil {
			_ = state.root.Close()
		}
		te.taskMu.Unlock()
		// Only remove from tasks map if we're still the active executor
		// (prevents old executor from deleting a replacement during type switch).
		// For Pi executors, keep the setup.task alive for on-demand compaction.
		te.taskMu.Lock()
		if state, exists := te.tasks[setup.task.ID]; exists && state.ctx == setup.ctx {
			if state.executorType != "agent" {
				delete(te.tasks, setup.task.ID)
			} else {
				// Clear status so HasRunningTasks doesn't count completed agent tasks
				state.taskStatus = ""
			}
		}
		te.taskMu.Unlock()
		// Clear assigned ports when setup.task execution ends
		if len(setup.task.AssignedPorts) > 0 {
			setup.task.AssignedPorts = nil
			te.persistTask(setup)
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
		}
		// Don't remove the stream here — the broadcaster lives as long as clients are connected.
		// It's removed only when the setup.task is deleted (via RemoveStream in cleanupTask).
		// Sandbox container is also kept alive until setup.task deletion.
	}()

	// Acquire the per-provider running-task slot BEFORE sandbox setup so a
	// queued task doesn't hold a sandbox container while waiting. Agent
	// providers manage their own concurrency and are excluded.
	if !te.configStore.IsAgentProvider(setup.serverURL) {
		if !te.acquireTaskSlot(setup, setup.serverURL) {
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		}
	}

	// Create sandbox if enabled
	// Get state reference to store sandbox callbacks
	te.taskMu.Lock()
	state := te.tasks[setup.task.ID]
	te.taskMu.Unlock()

	// Resolve image and agent config dir early — needed for both initial creation and mid-execution recreation.
	imageToUse := ""
	if setup.sandboxEnabled {
		var agentClient *sandbox.Client
		var err error
		imageToUse, agentClient, _, err = te.setupSandbox(setup)
		if err != nil {
			te.failTask(setup.task, setup.projectID, err.Error(), "sandbox_setup_failed")
			return
		}
		state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(agentClient)
		state.sandboxOperatingMode = setup.task.OperatingMode
		state.agentSandboxHash = agentSandboxHash(te.agentRegistry.GetAnyAgentSandbox())

		// Wire up bridge recovery — reconnects client or (re)creates sandbox.
		state.recoverBridge = func() bool {
			return te.recoverBridgeForTask(te.sandboxMgr, setup, state, imageToUse, te.agentRegistry.GetAnyAgentSandbox(), prompts.SkillsDir(), func() {
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{
					Type: types.WSTypeStatus,
					Data: map[string]string{"state": "running", "activity": "Sandbox bridge recovered"},
				})
			})
		}
	} else {
		// Non-sandbox: use os.Root only for write isolation.
		// Read uses plain os.ReadFile (no isolation).
		root, err := os.OpenRoot(setup.worktreePath)
		if err != nil {
			te.failTask(setup.task, setup.projectID, fmt.Sprintf("Failed to create worktree root: %v", err), "root_create_failed")
			return
		}
		state.root = root
		_, state.sandboxWrite = makeRootCallbacks(root, setup.worktreePath)
	}

	// Check if this setup.task uses an agent provider
	if te.configStore.IsAgentProvider(setup.serverURL) {
		log.Printf("[exec] setup.task=%s detected agent provider (url=%s), calling executeTaskWithAgent", setup.task.ID, setup.serverURL)
		te.executeTaskWithAgent(setup.ctx, setup.task, setup.projectID, setup.worktreePath, setup.serverURL, setup.modelName, setup.userMsgChan, setup.sandboxEnabled, setup.initialContent)
		log.Printf("[exec] setup.task=%s executeTaskWithAgent returned", setup.task.ID)
		return
	}

	mc := &modelConfig{serverURL: setup.serverURL, modelName: setup.modelName}
	providerName := te.configStore.GetProviderIDByURL(setup.serverURL)
	reasoningEffort := setup.task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(setup.modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(setup.serverURL)
	client := te.newLlamaClient(setup.serverURL, setup.modelName, reasoningEffort, apiKey)
	// Select tools based on operating mode
	var tools []types.ChatTool
	if setup.task.IsPlanMode() {
		tools = llama.GetPlanModeToolDefinitions()
	} else if setup.task.IsExploreMode() {
		tools = llama.GetExploreModeToolDefinitions()
	} else {
		tools = llama.GetToolDefinitions()
	}
	// Append LSP tools if any LSP extension has tool calls enabled
	if te.lspMgr != nil && te.lspMgr.GetRegistry().HasAnyLSPToolCallsExtension() {
		tools = append(tools, llama.GetLSPToolDefinitions()...)
	}
	taskMsgs, _ := te.store.GetTaskMessages(setup.task.ID, setup.task.CurrentThreadID)
	messages := buildMessagesFromDB(setup.task, taskMsgs, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)

	// Remove duplicate user messages that may occur during regeneration due to
	// race conditions between DELETE and POST handlers.
	if setup.regenerate {
		messages = removeDuplicateUserMessage(messages)
	}

	// Context tracking: load context window from provider config, discover if unknown
	var contextWindow *int
	if cw := te.configStore.GetModelContextWindow(setup.modelName, setup.serverURL); cw != nil {
		contextWindow = cw
	} else if discovered := te.configStore.DiscoverContextWindow(setup.modelName, setup.serverURL); discovered != nil {
		contextWindow = discovered
		te.configStore.SaveModelContextWindow(setup.modelName, setup.serverURL, *discovered)
		log.Printf("[context] discovered context window for %s: %d", setup.modelName, *discovered)
	}

	maxFailures := setup.task.MaxConsecutiveFailedTurns
	if maxFailures == 0 {
		maxFailures = 3
	}

	// Consolidate all mutable loop state into a single struct.
	lc = &loopContext{
		messages:         messages,
		pendingToolCalls: nil,
		prevArgLens:      make(map[string]int),
		detector:         newLoopDetector(),
		mc:               mc,
		client:           client,
		providerName:     providerName,
		contextWindow:    contextWindow,
		systemPrompt:     messages[0].Content,
		sandboxEnabled:   setup.sandboxEnabled,
	}
	// Sync once more to ensure we have the latest DB state (e.g. if messages were added/deleted)
	lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)

	// Carry over the accumulated session cost from prior runs/executors so the badge
	// reflects the full session cost across provider/model switches and executor restarts.
	lc.sessionCost = setup.task.SessionCost

	for {
		select {
		case <-setup.ctx.Done():
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		default:
		}

		// Check for model/provider changes before each LLM call.
		if updatedTask, err := te.store.GetTaskByID(setup.task.ID); err == nil {
			newURL := updatedTask.LlamaServerURL
			newModel := updatedTask.ModelName
			curURL, curModel := lc.mc.get()
			if newURL != "" && newURL != curURL || newModel != "" && newModel != curModel {
				// Skip model switch if target is agent — requires different executor
				if te.configStore.IsAgentProvider(newURL) {
					log.Printf("[model-switch] setup.task=%s skipping switch to agent (requires executor restart)", setup.task.ID)
				} else {
					if newURL == "" {
						newURL = curURL
					}
					if newModel == "" {
						newModel = curModel
					}
					switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, newURL, newModel, false, true)
					lc.client = switched.client
					lc.providerName = switched.providerName
					if switched.contextWindow != nil {
						lc.contextWindow = switched.contextWindow
					}
					lc.detector.reset() // model changed, clear stale text window
					lc.detector.clearHints()
					log.Printf("[model-switch] setup.task=%s url=%s model=%s context_window=%v", setup.task.ID, newURL, newModel, lc.contextWindow)
					// Provider (not just model) changed - move the running-task
					// slot to the new provider (may queue while it's saturated).
					if strings.TrimRight(newURL, "/") != strings.TrimRight(curURL, "/") {
						if !te.syncTaskSlot(setup, newURL) {
							te.handleCancelledTask(setup.task.ID, setup.projectID)
							return
						}
					}
				}
			}

			// Check for operating mode change — recreate sandbox with correct mount mode
			if setup.sandboxEnabled && te.sandboxMgr != nil && te.sandboxMgr.ContainerIsRunning(setup.task.ID) {
				te.taskMu.Lock()
				currentOperatingMode := state.sandboxOperatingMode
				te.taskMu.Unlock()
				newOperatingMode := updatedTask.OperatingMode
				if newOperatingMode != currentOperatingMode {
					log.Printf("[mode-switch] setup.task=%s operating mode changed (%s → %s), recreating sandbox", setup.task.ID, currentOperatingMode, newOperatingMode)
					// Stop old container
					if err := te.sandboxMgr.Stop(setup.task.ID); err != nil {
						log.Printf("[mode-switch] setup.task=%s failed to stop old sandbox: %v", setup.task.ID, err)
					}
					// Create new container with updated mode
					var assignedPorts []types.AssignedPort
					assignedPorts, createErr := te.sandboxMgr.Create(setup.task.ID, setup.worktreePath, setup.project, kanban_sandbox.SandboxOpts{
						BaseImage:        imageToUse,
						NetworkIsolation: setup.sandboxOpts.NetworkIsolation,
						MemoryLimit:      setup.sandboxOpts.MemoryLimit,
						ForwardPorts:     setup.sandboxOpts.ForwardPorts,
						AgentSandbox:     te.agentRegistry.GetAnyAgentSandbox(),
						SkillsDir:        prompts.SkillsDir(),
						SandboxMounts:    setup.sandboxOpts.SandboxMounts,
						SandboxEnvs:      setup.sandboxOpts.SandboxEnvs,
						Capabilities:     setup.sandboxOpts.Capabilities,
						PlanMode:         updatedTask.IsPlanMode(),
					})
					if createErr != nil {
						log.Printf("[mode-switch] setup.task=%s failed to recreate sandbox: %v", setup.task.ID, createErr)
						setup.task.ExecutionState = "failed"
						setup.task.Status = "review"
						setup.task.Output = fmt.Sprintf("Failed to recreate sandbox for mode change: %v", createErr)
						te.persistTask(setup)
						te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
						te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Sandbox recreation failed: %v", createErr), "sandbox_recreate_failed")
						return
					}
					// Get new agent client and update callbacks
					newAgentClient, clientErr := te.sandboxMgr.GetClient(setup.task.ID)
					if clientErr != nil {
						log.Printf("[mode-switch] setup.task=%s failed to connect to new sandbox bridge: %v", setup.task.ID, clientErr)
						setup.task.ExecutionState = "failed"
						setup.task.Status = "review"
						setup.task.Output = fmt.Sprintf("Failed to connect to sandbox bridge after mode change: %v", clientErr)
						te.persistTask(setup)
						te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
						te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Agent connection failed after mode change: %v", clientErr), "agent_connection_failed")
						return
					}
					te.taskMu.Lock()
					state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(newAgentClient)
					state.sandboxOperatingMode = newOperatingMode
					te.taskMu.Unlock()
					// Update setup.task reference with latest mode
					setup.task.OperatingMode = updatedTask.OperatingMode
					// Update tools if mode affects them
					if updatedTask.IsExploreMode() {
						tools = llama.GetExploreModeToolDefinitions()
					} else if updatedTask.IsPlanMode() {
						tools = llama.GetPlanModeToolDefinitions()
					} else {
						tools = llama.GetToolDefinitions()
					}
					// Append LSP tools if any LSP extension has tool calls enabled
					if te.lspMgr != nil && te.lspMgr.GetRegistry().HasAnyLSPToolCallsExtension() {
						tools = append(tools, llama.GetLSPToolDefinitions()...)
					}
					// Persist + broadcast assigned ports if any changed (carried on the task object)
					if len(assignedPorts) > 0 {
						setup.task.AssignedPorts = assignedPorts
						te.persistTask(setup)
						te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					}
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Sandbox recreated for mode change (%s)", newOperatingMode)}})
				}
			}
		}

		// Check for agent sandbox config change — recreate sandbox with updated mounts/envs
		if setup.sandboxEnabled && te.sandboxMgr != nil && te.sandboxMgr.ContainerIsRunning(setup.task.ID) {
			currentHash := ""
			te.taskMu.Lock()
			currentHash = state.agentSandboxHash
			te.taskMu.Unlock()
			newHash := agentSandboxHash(te.agentRegistry.GetAnyAgentSandbox())
			if newHash != "" && newHash != currentHash {
				log.Printf("[sandbox-config] setup.task=%s agent sandbox config changed, recreating sandbox", setup.task.ID)
				if err := te.sandboxMgr.Stop(setup.task.ID); err != nil {
					log.Printf("[sandbox-config] setup.task=%s failed to stop old sandbox: %v", setup.task.ID, err)
				}
				var assignedPorts []types.AssignedPort
				assignedPorts, createErr := te.sandboxMgr.Create(setup.task.ID, setup.worktreePath, setup.project, kanban_sandbox.SandboxOpts{
					BaseImage:        imageToUse,
					NetworkIsolation: setup.sandboxOpts.NetworkIsolation,
					MemoryLimit:      setup.sandboxOpts.MemoryLimit,
					ForwardPorts:     setup.sandboxOpts.ForwardPorts,
					AgentSandbox:     te.agentRegistry.GetAnyAgentSandbox(),
					SkillsDir:        prompts.SkillsDir(),
					SandboxMounts:    setup.sandboxOpts.SandboxMounts,
					SandboxEnvs:      setup.sandboxOpts.SandboxEnvs,
					Capabilities:     setup.sandboxOpts.Capabilities,
					PlanMode:         setup.task.IsPlanMode(),
				})
				if createErr != nil {
					log.Printf("[sandbox-config] setup.task=%s failed to recreate sandbox: %v", setup.task.ID, createErr)
					setup.task.ExecutionState = "failed"
					setup.task.Status = "review"
					setup.task.Output = fmt.Sprintf("Failed to recreate sandbox for config change: %v", createErr)
					te.persistTask(setup)
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Sandbox recreation failed: %v", createErr), "sandbox_recreate_failed")
					return
				}
				newAgentClient, clientErr := te.sandboxMgr.GetClient(setup.task.ID)
				if clientErr != nil {
					log.Printf("[sandbox-config] setup.task=%s failed to connect to new sandbox bridge: %v", setup.task.ID, clientErr)
					setup.task.ExecutionState = "failed"
					setup.task.Status = "review"
					setup.task.Output = fmt.Sprintf("Failed to connect to sandbox bridge after config change: %v", clientErr)
					te.persistTask(setup)
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Agent connection failed after config change: %v", clientErr), "agent_config_failed")
					return
				}
				te.taskMu.Lock()
				state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite, state.sandboxEdit = makeSandboxCallbacks(newAgentClient)
				state.agentSandboxHash = newHash
				te.taskMu.Unlock()
				// Persist + broadcast assigned ports if any changed (carried on the task object)
				if len(assignedPorts) > 0 {
					setup.task.AssignedPorts = assignedPorts
					te.persistTask(setup)
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
				}
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Sandbox recreated for extension config change"}})
			}
		}

		// Check for user messages that arrived during tool execution.
		select {
		case um := <-setup.userMsgChan:
			if um.ContextChanged {
				log.Printf("[context-changed] setup.task=%s conversation context changed, resetting loop detector", setup.task.ID)
				lc.detector.reset()
				lc.detector.clearHints()
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
			} else {
				log.Printf("[user-msg] setup.task=%s received message before LLM call (setup.regenerate=%v)", setup.task.ID, um.Regenerate)
				lc.detector.reset() // user input breaks any loop pattern
				lc.detector.clearHints()
				if um.Regenerate {
					lc.messages = truncateForRegenerate(lc.messages)
					lc.messages = append(lc.messages, userMessageToChatMessage(um))
				} else {
					// Re-sync with DB to pick up the user message (already created by HandleSend)
					// and any deleted messages. Do NOT append — syncMessagesFromDB already includes it.
					lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
				}
				if um.ModelName != "" && !te.configStore.IsAgentProvider(um.ServerURL) {
					prevURL, _ := lc.mc.get()
					switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, um.ServerURL, um.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
					if um.ServerURL != "" && strings.TrimRight(um.ServerURL, "/") != strings.TrimRight(prevURL, "/") {
						if !te.syncTaskSlot(setup, um.ServerURL) {
							te.handleCancelledTask(setup.task.ID, setup.projectID)
							return
						}
					}
				}
			}
		default:
		}

		// Clear turn-specific state before each LLM call.
		lc.prevArgLens = make(map[string]int)
		lc.finishReason = ""
		lc.pendingToolCalls = nil
		lc.assistantContent.Reset()
		lc.reasoningContent.Reset()

		// Acquire concurrency gate slot before LLM call. Keyed by the CURRENT
		// provider — lc.mc follows mid-run model/provider switches, while
		// setup.serverURL is only the start-time URL.
		gateURL, activeModel := lc.mc.get()
		concurrency := te.configStore.GetProviderConcurrency(gateURL)
		maxRPS := te.configStore.GetProviderMaxRequestsPerSecond(gateURL)

		// Queue status polling — runs while waiting for a gate slot, reporting
		// the call's own position (its queue entry is handed to the poller via
		// the onEnqueued callback as soon as it is enqueued).
		onEnqueued, queueDone := te.startGateQueuePoller(te.gate, gateURL, setup.projectID, setup.task.ID, setup.ctx)
		if _, err := te.gate.Acquire(setup.ctx, gateURL, concurrency, maxRPS, onEnqueued); err != nil {
			close(queueDone) // cleanup polling goroutine
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		}
		close(queueDone)

		// Per-call context — can be cancelled independently to abort the LLM stream.
		callCtx, callCancel := context.WithCancel(setup.ctx)

		streamDone := make(chan struct{})
		var streamErr error
		var lastProgressSend time.Time // throttle progress updates to ~1/2s
		var lastPredictedN int         // track predicted tokens for real-time stats events

		// Assemble LLM context (compresses history via summary if one exists).
		llmMessages := buildLLMContext(lc.messages)

		// Set status before the LLM call so the frontend picks up streaming tokens.
		setup.task.Status = "in_progress"
		setup.task.ExecutionState = "running"
		te.persistTask(setup)
		te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)

		// streamFlusher batches streaming events to reduce WebSocket write overhead.
		flusher := newStreamFlusher(setup.projectID, setup.task.ID, lc.sandboxEnabled,
			te.appStreamMgr.SendTaskEvent, lc.detector)
		flushDone := make(chan struct{})
		go func() {
			tick := time.NewTicker(streamThrottleInterval)
			defer tick.Stop()
			defer close(flushDone)
			for {
				select {
				case <-callCtx.Done():
					return
				case <-tick.C:
					flusher.flush()
				}
			}
		}()

		// Shared callbacks for ChatStream — used by both the initial call and retries.
		tokenCb := func(choice types.ChatChoice) {
			// Capture finish_reason for loop detection (last non-empty value wins).
			if choice.FinishReason != "" {
				lc.finishReason = choice.FinishReason
			}
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				lc.reasoningContent.WriteString(*choice.Delta.ReasoningContent)
				flusher.appendReasoning(*choice.Delta.ReasoningContent)
			}
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				lc.assistantContent.WriteString(*choice.Delta.Content)
				flusher.appendToken(*choice.Delta.Content)
				// Real-time phrase repetition detection (not throttled — detection runs per-token)
				if loopType := lc.detector.checkStreamingText(*choice.Delta.Content); loopType == LoopPhrase {
					log.Printf("[loop] setup.task=%s phrase repetition detected during streaming", setup.task.ID)
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Repetitive response detected"}})
				}
			}
			if len(choice.Delta.ToolCalls) > 0 {
				priorLen := len(lc.pendingToolCalls)
				lc.pendingToolCalls = llama.MergeToolCalls(lc.pendingToolCalls, choice.Delta.ToolCalls)
				for i := 0; i < len(lc.pendingToolCalls); i++ {
					tc := lc.pendingToolCalls[i]
					if tc.ID == "" || tc.Function.Name == "" {
						continue
					}
					argLen := len(tc.Function.Arguments)
					prevLen := lc.prevArgLens[tc.ID]
					isNew := i >= priorLen
					isUpdated := !isNew && argLen != prevLen
					if !isNew && !isUpdated {
						continue
					}
					lc.prevArgLens[tc.ID] = argLen
					path := extractPathFromArgs(tc.Function.Arguments)
					actualPrevLen := prevLen
					if isNew {
						actualPrevLen = 0
					}
					flusher.appendToolCall(tc.ID, tc.Function.Name, llama.GetToolDisplayName(tc.Function.Name), path, tc.Function.Arguments, argLen, actualPrevLen, isNew, (lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)) || isLSPTool(tc.Function.Name))
				}
			}
			// Throttled progress updates for write_file (1 per 2 seconds)
			if time.Since(lastProgressSend) > 2*time.Second {
				for _, tc := range lc.pendingToolCalls {
					if tc.Function.Name == "write_file" && tc.Function.Arguments != "" {
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": extractPathFromArgs(tc.Function.Arguments)}})
						lastProgressSend = time.Now()
					}
				}
			}
		}
		// usageCb publishes live token stats. It reads the snapshot, never
		// lc.usage: that field is only set once ChatStream returns, so mid-stream
		// it holds nil (first turn) or the previous turn's numbers (later turns).
		usageCb := func(u *types.ChatUsage) {
			if u == nil {
				return
			}
			if u.CompletionTokens != lastPredictedN {
				lastPredictedN = u.CompletionTokens
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
					"model_name":       activeModel,
					"prompt_tokens":    u.PromptTokens,
					"predicted_tokens": u.CompletionTokens,
					"predicted_ms":     u.PredictedMs,
				}})
			}
		}
		progressCb := func(progress types.PromptProgressData) {
			// Forward prompt processing progress to the frontend.
			// Throttle to ~2 updates/sec to avoid flooding the WebSocket.
			if time.Since(lastProgressSend) > 500*time.Millisecond {
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypePromptProgress, Data: map[string]any{
					"total":     progress.Total,
					"cache":     progress.Cache,
					"processed": progress.Processed,
					"time_ms":   progress.TimeMs,
				}})
				lastProgressSend = time.Now()
			}
		}

		lc.callStartTime = time.Now()
		go func() {
			lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, tokenCb, progressCb, usageCb)

			// Retry on transient errors (skip if cancelled by user message)
			if streamErr != nil && callCtx.Err() == nil {
				for i := range rateLimitRetryAttempts {
					select {
					case <-callCtx.Done():
						flusher.flush()
						close(streamDone)
						return
					case <-time.After(backoffFor(i, streamErr)):
					}
					// Notify user of retry attempt
					te.sendRetryHint(setup.projectID, setup.task.ID, setup.task, streamErr, i+1)
					// Flush any remaining buffered events before retry.
					flusher.flush()
					// Reset builders so retry doesn't concatenate partial content.
					lc.assistantContent.Reset()
					lc.reasoningContent.Reset()
					lc.pendingToolCalls = nil
					lc.finishReason = ""
					flusher.reset()
					lc.callStartTime = time.Now()
					lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, tokenCb, progressCb, usageCb)
					if streamErr == nil {
						break
					}
				}
			}

			// Emit accumulated session cost (LLM providers only; agents report their own).
			if streamErr == nil && lc.usage != nil {
				curURL, _ := lc.mc.get()
				if cost := te.configStore.EstimateCallCost(curURL, activeModel, lc.usage); cost > 0 {
					lc.sessionCost += cost
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
						"model_name":       activeModel,
						"prompt_tokens":    lc.usage.PromptTokens,
						"predicted_tokens": lc.usage.CompletionTokens,
						"predicted_ms":     lc.usage.PredictedMs,
						"cost":             lc.sessionCost,
					}})
				}
			}

			// Flush any remaining buffered events before signalling completion.
			flusher.flush()
			close(streamDone)
		}()

		// Wait for: stream complete, new user message, or setup.task stop.
		select {
		case <-setup.ctx.Done():
			callCancel()
			te.gate.Release(gateURL) // Release gate immediately so other tasks can proceed
			<-streamDone             // Then wait for goroutine to finish
			te.handleCancelledTask(setup.task.ID, setup.projectID)
			return
		case <-streamDone:
			// Stream completed normally (or all retries exhausted)
		case um := <-setup.userMsgChan:
			if um.ContextChanged {
				log.Printf("[context-changed] setup.task=%s conversation context changed during streaming, cancelling", setup.task.ID)
				lc.detector.reset()
				lc.detector.clearHints()
				callCancel()
				select {
				case <-streamDone:
				case <-time.After(2 * time.Second):
					log.Printf("[cancel] setup.task=%s streamDone timeout, forcing continue", setup.task.ID)
				}
				te.gate.Release(gateURL)
				lc.assistantContent.Reset()
				lc.reasoningContent.Reset()
				lc.pendingToolCalls = nil
				lc.finishReason = ""
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
				continue
			}
			// New user message — cancel current LLM call.
			log.Printf("[cancel] setup.task=%s user message received during streaming, cancelling (setup.regenerate=%v)", setup.task.ID, um.Regenerate)
			lc.detector.reset() // user input breaks any loop pattern
			lc.detector.clearHints()
			callCancel()
			// Wait for goroutine to finish (with timeout in case HTTP stream is slow to abort).
			select {
			case <-streamDone:
			case <-time.After(2 * time.Second):
				log.Printf("[cancel] setup.task=%s streamDone timeout, forcing continue", setup.task.ID)
			}
			te.gate.Release(gateURL)

			// Drain all queued messages from channel (non-blocking).
			var queuedMsgs []stream.UserMessage
			queuedMsgs = append(queuedMsgs, um)
			for range 16 {
				select {
				case q := <-setup.userMsgChan:
					queuedMsgs = append(queuedMsgs, q)
				default:
					goto drainDone
				}
			}
		drainDone:

			// Clear partial assistant state.
			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()
			lc.pendingToolCalls = nil
			lc.finishReason = ""

			// If any queued message is a setup.regenerate, truncate stale assistant messages.
			// Otherwise, re-sync with DB to pick up any deleted messages.
			hadRegenerate := false
			for _, qm := range queuedMsgs {
				if qm.Regenerate {
					lc.messages = truncateForRegenerate(lc.messages)
					hadRegenerate = true
					break
				}
			}
			if !hadRegenerate {
				// Re-sync with DB to pick up user messages (already created by HandleSend)
				// and any deleted messages. Do NOT append — syncMessagesFromDB already includes them.
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
			}

			// Apply model overrides from queued messages.
			for _, qm := range queuedMsgs {
				if qm.Regenerate {
					lc.messages = append(lc.messages, userMessageToChatMessage(qm))
				}
				if qm.ModelName != "" {
					switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, qm.ServerURL, qm.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
			}
			// If the queued messages moved the task to another provider, move
			// the running-task slot with it (no-op when the URL is unchanged).
			if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
				te.handleCancelledTask(setup.task.ID, setup.projectID)
				return
			}

			// Notify frontend that response was cancelled.
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "cancelled", "activity": "Response cancelled, processing new message"}})

			continue
		}
		callCancel()

		if streamErr != nil {
			// Release gate slot (was acquired before the LLM call above).
			te.gate.Release(gateURL)
			log.Printf("[connect] setup.task=%s LLM connection failed after retries: %v", setup.task.ID, streamErr)
			te.failTask(setup.task, setup.projectID,
				fmt.Sprintf("LLM connection failed: %v", streamErr),
				"llm_connection_failed")
			return
		}

		// Release gate slot after LLM call (before tool execution).
		te.gate.Release(gateURL)

		assistantText := lc.assistantContent.String()

		var toolResults []toolResult
		var turnFailures []turnResult

		if len(lc.pendingToolCalls) > 0 {
			// Persist assistant message BEFORE tool calls so DB ordering matches LLM output order
			msgCountBefore := len(lc.messages)
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages[:msgCountBefore], assistantText, lc.reasoningContent.String())
			logStatsSpeed(setup.task.ID, activeModel, promptTokens-cacheTokens, predictedTokens, promptMs, predictedMs)
			assistantCreatedAt := time.Now().UTC()
			assistantMsgID := uuid.New().String()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: assistantMsgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: lc.reasoningContent.String(),
				ModelName: activeModel, ProviderName: lc.providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: time.Since(lc.callStartTime).Milliseconds(), CreatedAt: assistantCreatedAt,
			})

			// Track if any tool call was a git command — needed because fsnotify
			// can't detect diff state changes from git commit/checkout/merge
			// (branch pointer moves without working tree file changes).
			hadGitCmd := false
			hadRequestReview := false // request_review detected in this turn

			for _, tc := range lc.pendingToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					log.Printf("[tool] skipping incomplete tool call (id=%q, name=%q)", tc.ID, tc.Function.Name)
					continue
				}
				args := parseToolArgs(tc.Function.Arguments)
				// Send final path update before execution (full args now available)
				if tc.Function.Name == "write_file" {
					path := extractPathFromArgs(tc.Function.Arguments)
					if path == "" {
						if p, ok := args["path"].(string); ok && p != "" {
							path = p
						}
					}
					if path != "" {
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": path}})
					}
				}
				// Persist tool_call message BEFORE execution so it survives page refresh.
				// ToolResult is empty until execution completes.
				tcJSON, _ := json.Marshal(map[string]any{
					"tool_id":           tc.ID,
					"tool_name":         tc.Function.Name,
					"tool_display_name": llama.GetToolDisplayName(tc.Function.Name),
					"args":              args,
					"raw_args":          tc.Function.Arguments,
					"sandbox":           (lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)) || isLSPTool(tc.Function.Name),
				})
				toolCallMsgID := uuid.New().String()
				if err := te.store.CreateTaskMessage(&types.TaskMessage{
					ID:        toolCallMsgID,
					TaskID:    setup.task.ID,
					ThreadID:  setup.task.CurrentThreadID,
					Role:      "tool_call",
					Content:   "",
					ToolCalls: string(tcJSON),
					CreatedAt: time.Now().UTC(),
				}); err != nil {
					log.Printf("[tool] FAILED to persist tool_call message for setup.task=%s tool=%s: %v", setup.task.ID, tc.Function.Name, err)
				}

				// tool_call event already sent during streaming, just execute and send result
				log.Printf("[tool] executing %s for setup.task=%s", tc.Function.Name, setup.task.ID)
				toolStart := time.Now()
				te.taskMu.Lock()
				state := te.tasks[setup.task.ID]
				te.taskMu.Unlock()
				// Serialize tool writes against subtask merges into this worktree.
				te.GetWorktreeMu(setup.task.ID).Lock()
				streamed := false
				result, success := te.executeTool(setup.ctx, tc.Function.Name, args, setup.worktreePath, setup.task, state, func(toolID string, text string) {
					streamed = true
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolOutput, Data: map[string]any{"tool_id": tc.ID, "text": text}})
				})

				// Trigger LSP diagnostics for files modified by the tool.
				// For write/edit tools, use the path from tool args directly.
				// For bash, extract file paths from the command and only check files
				// that match an installed LSP extension.
				var lspHint string
				switch tc.Function.Name {
				case "write_file", "edit_file":
					if path, ok := args["path"].(string); ok && path != "" {
						lspHint = te.triggerLSPDiagnostics(setup.ctx, setup.projectID, setup.task.ID, path)
					}
				case "bash":
					if cmd, ok := args["command"].(string); ok && cmd != "" {
						files := extractFilesFromBashCommand(cmd)
						for _, f := range files {
							if hint := te.triggerLSPDiagnostics(setup.ctx, setup.projectID, setup.task.ID, f); hint != "" {
								lspHint = hint
								break
							}
						}
					}
				}
				// Inject LSP diagnostic hint into LLM context if found.
				if lspHint != "" {
					task, _ := te.store.GetTaskByID(setup.task.ID)
					if task != nil {
						msgID := uuid.New().String()
						_ = te.store.CreateTaskMessage(&types.TaskMessage{
							ID: msgID, TaskID: setup.task.ID, ThreadID: task.CurrentThreadID, Role: "user", Content: lspHint, CreatedAt: time.Now().UTC(),
						})
						lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: lspHint})
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{
							Type: types.WSTypeUserMessage,
							Data: map[string]any{
								"id":      msgID,
								"role":    "user",
								"content": lspHint,
							},
						})
					}
				}
				te.GetWorktreeMu(setup.task.ID).Unlock()
				toolDuration := time.Since(toolStart).Milliseconds()
				isSandboxed := (lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)) || isLSPTool(tc.Function.Name)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: buildToolResultEvent(tc.ID, tc.Function.Name, result, success, toolDuration, args, tc.Function.Arguments, extractPathFromArgs(tc.Function.Arguments), isSandboxed, streamed, toolCallMsgID)})
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result, messageID: toolCallMsgID, args: args, rawArgs: tc.Function.Arguments, success: success, duration: toolDuration})

				// Update tool_call message with result after execution completes.
				resultJSON, _ := json.Marshal(map[string]any{
					"output":   result,
					"success":  success,
					"duration": toolDuration,
				})
				_ = te.store.UpdateTaskMessageToolResult(toolCallMsgID, string(resultJSON))

				turnFailures = append(turnFailures, turnResult{
					signature: tc.Function.Name + ":" + tc.Function.Arguments,
					success:   success,
				})

				// Detect git commands in bash tool calls
				if tc.Function.Name == "bash" {
					if cmd, ok := args["command"].(string); ok && strings.Contains(cmd, "git ") {
						hadGitCmd = true
					}
				}

				if tc.Function.Name == "request_review" {
					hadRequestReview = true
				}
			}
			// Post-tool git check: broadcast diff if a git command changed the state
			if hadGitCmd {
				te.broadcastDiffIfGitCmd(setup.projectID, setup.task.ID, setup.task)
			}

			// ask_question: auto-started tasks auto-answer from parent context and
			// continue; otherwise stop and wait for the user.
			askResults := askQuestionResults(lc.pendingToolCalls, toolResults)
			autoAnswered := false
			if len(askResults) > 0 {
				// Collect question indexes in call order (map iteration is unordered).
				questionIdxs := make([]int, 0, len(askResults))
				for i := range askResults {
					questionIdxs = append(questionIdxs, i)
				}
				slices.Sort(questionIdxs)
				if setup.task.Autostarted {
					questions := make([]string, 0, len(questionIdxs))
					for _, i := range questionIdxs {
						questions = append(questions, toolResults[askResults[i]].output)
					}
					answers, aerr := te.automatedAnswers(setup.ctx, setup.task, questions)
					if aerr != nil || len(answers) != len(questionIdxs) {
						if aerr != nil {
							log.Printf("[ask_question] setup.task=%s auto-answer failed: %v, falling back to user", setup.task.ID, aerr)
						} else {
							log.Printf("[ask_question] setup.task=%s auto-answer count mismatch (%d answers, %d questions), falling back to user", setup.task.ID, len(answers), len(questionIdxs))
						}
					} else {
						for k, idx := range questionIdxs {
							ri := askResults[idx]
							tr := toolResults[ri]
							tr.output = answers[k]
							toolResults[ri] = tr
							// Persist the answer as this tool call's result.
							resultJSON, _ := json.Marshal(map[string]any{"output": tr.output, "success": tr.success, "duration": tr.duration})
							_ = te.store.UpdateTaskMessageToolResult(tr.messageID, string(resultJSON))
							// Re-broadcast so the UI shows the answer in place.
							te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: buildToolResultEvent(tr.toolCallID, "ask_question", tr.output, tr.success, tr.duration, tr.args, tr.rawArgs, "", false, false, tr.messageID)})
						}
						autoAnswered = true
						log.Printf("[ask_question] setup.task=%s auto-answered %d question(s) from parent context", setup.task.ID, len(answers))
					}
				}

				// Not auto-answered and no request_review: stop and wait for the user.
				if !hadRequestReview && !autoAnswered {
					tr := toolResults[askResults[questionIdxs[0]]]
					setup.task.ExecutionState = "idle"
					setup.task.Status = "review"
					te.persistTask(setup)
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
						"state":    "idle",
						"question": tr.output,
						"summary":  "Agent has a question",
					}})
					log.Printf("[ask_question] setup.task=%s stopping executor, awaiting user answer", setup.task.ID)
					// Remove from setup.task registry so next user message triggers handleLazyStart
					te.taskMu.Lock()
					delete(te.tasks, setup.task.ID)
					te.taskMu.Unlock()
					return
				}
			}

			// request_review: handle task completion (commit/merge)
			// This runs AFTER auto-answer so that ask_question in the same turn
			// is answered before the task completes.
			if hadRequestReview {
				if setup.task.AutoCommit {
					if setup.task.HasMergeConflict {
						_, setup.task = te.commitConflictResolution(setup.projectID, setup.task.ID, setup.worktreePath, setup.task)
						if setup.task.HasMergeConflict {
							// Still conflicted — inject resolution prompt and continue
							conflictedFiles := GetConflictedFiles(setup.worktreePath)
							prompt, pErr := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
								TaskBranch:      setup.task.Branch,
								TargetBranch:    te.ResolveMergeTarget(setup.projectID, setup.task),
								TaskTitle:       setup.task.Title,
								TaskDescription: setup.task.Prompt,
								ConflictedFiles: conflictedFiles,
							})
							if pErr != nil {
								log.Printf("[merge] setup.task=%s failed to render merge conflict prompt: %v", setup.task.ID, pErr)
							}
							lc.detector.reset()
							lc.detector.clearHints()
							lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: prompt})
							te.store.CreateTaskMessage(&types.TaskMessage{
								ID: uuid.New().String(), TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID,
								Role: "system", Content: "Merge conflict detected — resolving with tools",
								CreatedAt: time.Now().UTC(),
							})
							te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Resolving merge conflicts"}})
							lc.pendingToolCalls = nil
							lc.assistantContent.Reset()
							lc.reasoningContent.Reset()
							lc.finishReason = ""
							continue
						}
						if !setup.task.AutoMerge {
							setup.task.ExecutionState = "idle"
							setup.task.Status = "review"
						} else {
							setup.task.ExecutionState = "completed"
							setup.task.Status = "done"
						}
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(setup.task.ID)
						}
						te.persistTask(setup)
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
							"state":                setup.task.ExecutionState,
							"assistant_message_id": assistantMsgID,
							"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
							"cache_tokens": cacheTokens,
							"prompt_ms":    promptMs, "predicted_ms": predictedMs,
							"model_name": activeModel, "duration": time.Since(lc.callStartTime).Milliseconds(),
						}})
						te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
						if setup.task.ParentTaskID != "" {
							_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID)
						}
						return
					}

					// No merge conflict — normal auto-commit path
					commitMsg := te.GenerateCommitMessage(setup.ctx, setup.task)
					mergeErr := te.CommitAndMerge(setup.projectID, setup.task.ID, setup.worktreePath, commitMsg, setup.task.AutoMerge)
					setup.task, _ = te.store.GetTaskByID(setup.task.ID)
					if setup.task.HasMergeConflict {
						conflictedFiles := GetResolvedConflictedFiles(setup.worktreePath)
						prompt, pErr := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
							TaskBranch:      setup.task.Branch,
							TargetBranch:    te.ResolveMergeTarget(setup.projectID, setup.task),
							TaskTitle:       setup.task.Title,
							TaskDescription: setup.task.Prompt,
							ConflictedFiles: conflictedFiles,
						})
						if pErr != nil {
							log.Printf("[merge] setup.task=%s failed to render merge conflict prompt: %v", setup.task.ID, pErr)
						}
						lc.detector.reset()
						lc.detector.clearHints()
						lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: prompt})
						te.store.CreateTaskMessage(&types.TaskMessage{
							ID: uuid.New().String(), TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID,
							Role: "system", Content: "Merge conflict detected — resolving with tools",
							CreatedAt: time.Now().UTC(),
						})
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Resolving merge conflicts"}})
						lc.pendingToolCalls = nil
						lc.assistantContent.Reset()
						lc.reasoningContent.Reset()
						lc.finishReason = ""
						continue
					}
					if mergeErr != nil {
						log.Printf("[merge] task=%s merge failed, moving to review: %v", setup.task.ID, mergeErr)
						setup.task.ExecutionState = "idle"
						setup.task.Status = "review"
						setup.task.Output = "Merge failed: " + mergeErr.Error()
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(setup.task.ID)
						}
					} else if !setup.task.AutoMerge {
						setup.task.ExecutionState = "idle"
						setup.task.Status = "review"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(setup.task.ID)
						}
					} else {
						setup.task.ExecutionState = "completed"
						setup.task.Status = "done"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(setup.task.ID)
						}
					}
				} else if setup.task.HasMergeConflict {
					_, setup.task = te.commitConflictResolution(setup.projectID, setup.task.ID, setup.worktreePath, setup.task)
					if setup.task.HasMergeConflict {
						log.Printf("[merge] setup.task=%s merge still conflicted after request_review", setup.task.ID)
						setup.task.ExecutionState = "failed"
						setup.task.Status = "review"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(setup.task.ID)
						}
					} else if !setup.task.AutoMerge {
						setup.task.ExecutionState = "idle"
						setup.task.Status = "review"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(setup.task.ID)
						}
					} else {
						setup.task.ExecutionState = "completed"
						setup.task.Status = "done"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(setup.task.ID)
						}
					}
				} else {
					setup.task.ExecutionState = "idle"
					setup.task.Status = "review"
				}
				te.persistTask(setup)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
					"state":                setup.task.ExecutionState,
					"assistant_message_id": assistantMsgID,
					"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
					"cache_tokens": cacheTokens,
					"prompt_ms":    promptMs, "predicted_ms": predictedMs,
					"model_name": activeModel, "duration": time.Since(lc.callStartTime).Milliseconds(),
				}})
				te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
				if hadGitCmd {
					te.broadcastDiffIfGitCmd(setup.projectID, setup.task.ID, setup.task)
				}
				if setup.task.ParentTaskID != "" {
					_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID)
				}
				return
			}

			// Check consecutive identical tool call failures (turn-level).
			if count := lc.tracker.recordTurn(turnFailures); count >= maxFailures {
				setup.task.ExecutionState = "failed"
				setup.task.Status = "review"
				setup.task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
				te.persistTask(setup)
				te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
				te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures), "max_failures")
				return
			}

			// Build assistant message with reasoning, content, and tool calls.
			// Including tool_calls in the message history is critical for models like gemma
			// to understand what tool was called and match it to the result.
			// Keep reasoning in ReasoningContent so toAPIMessage sends it as reasoning_content.
			var assistantToolCalls []types.ToolCallMsg
			for _, tc := range lc.pendingToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					assistantToolCalls = append(assistantToolCalls, types.ToolCallMsg{
						ID:   tc.ID,
						Type: "function",
					})
					assistantToolCalls[len(assistantToolCalls)-1].Function.Name = tc.Function.Name
					assistantToolCalls[len(assistantToolCalls)-1].Function.Arguments = tc.Function.Arguments
				}
			}
			assistantMsg := types.ChatMessage{
				Role:      "assistant",
				Content:   assistantText,
				ToolCalls: assistantToolCalls,
			}
			if lc.reasoningContent.Len() > 0 {
				assistantMsg.ReasoningContent = lc.reasoningContent.String()
			}
			lc.messages = append(lc.messages, assistantMsg)
			for _, tr := range toolResults {
				lc.messages = append(lc.messages, types.ChatMessage{Role: "tool", Content: tr.output, ToolCallID: tr.toolCallID})
			}

			// Loop detection: record this turn and check for repetition
			loopType, _, toolArgs := lc.detector.recordTurn(lc.pendingToolCalls, assistantText, lc.finishReason)
			if loopType != LoopNone {
				target := ""
				if loopType == LoopTool {
					target = extractPathFromArgs(toolArgs)
				}
				if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, loopType, target, &lc.messages, setup.task.IsExploreMode()) {
					return
				}
			} else {
				// Valid turn: remove previously injected hints from context
				lc.messages = lc.detector.removeHints(lc.messages)
			}

			lc.pendingToolCalls = nil
			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()
			lc.finishReason = ""

			// Auto-commit file changes as a checkpoint.
			newMessages := lc.messages[lc.checkpointIdx:]
			te.createCheckpoint(setup.ctx, setup.projectID, setup.task.ID, setup.worktreePath, setup.task.CurrentThreadID, assistantMsgID, newMessages, lc.sandboxEnabled)
			lc.checkpointIdx = len(lc.messages)

			// Auto-summarize if context is approaching the limit
			te.maybeAutoSummarize(setup, lc, activeModel, true)

			// Continue to call LLM again with tool results — don't fall through to blocking select
			continue
		} else if assistantText != "" {
			// Text-only response resets the failure tracker
			lc.tracker.reset()

			// Keep reasoning in ReasoningContent so toAPIMessage sends it as reasoning_content.
			assistantMsg := types.ChatMessage{Role: "assistant", Content: assistantText}
			if lc.reasoningContent.Len() > 0 {
				assistantMsg.ReasoningContent = lc.reasoningContent.String()
			}
			lc.messages = append(lc.messages, assistantMsg)
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages[:len(lc.messages)-1], assistantText, lc.reasoningContent.String())
			logStatsSpeed(setup.task.ID, activeModel, promptTokens-cacheTokens, predictedTokens, promptMs, predictedMs)
			duration := time.Since(lc.callStartTime).Milliseconds()
			assistantMsgID := uuid.New().String()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: assistantMsgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: lc.reasoningContent.String(),
				ModelName: activeModel, ProviderName: lc.providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: duration, CreatedAt: time.Now().UTC(),
			})
			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()

			// Loop detection for text-only responses
			loopType, _, _ := lc.detector.recordTurn(nil, assistantText, lc.finishReason)
			lc.finishReason = ""
			// Also check for phrase repetition detected during streaming.
			// Skip if the response contains a question (natural question, not a loop).
			if loopType == LoopNone && lc.detector.phraseDetected() && !strings.Contains(assistantText, "?") {
				loopType = LoopPhrase
			}
			if loopType != LoopNone {
				if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, loopType, "", &lc.messages, setup.task.IsExploreMode()) {
					return
				}
				if !setup.task.IsExploreMode() && !setup.task.IsPlanMode() {
					// In act mode: loop detected, inject hint, continue
					setup.task.ExecutionState = "running"
					te.persistTask(setup)
					continue
				}
				// In exploration/plan mode: text-only responses are valid (brainstorming / planning).
				// Fall through to end the turn.
			}
			// Valid text-only response: remove previously injected hints from context
			lc.messages = lc.detector.removeHints(lc.messages)

			// Mark the response complete and the task idle FIRST so the assistant
			// message renders as finished (streaming cursor stops) before any
			// auto-summarization runs. This prevents the message from looking like it
			// is still generating while the summary is produced. Release the
			// running-task slot too - an idle task no longer blocks other tasks.
			te.releaseTaskSlot(setup)
			setup.task.Status = "review"
			setup.task.ExecutionState = "idle"
			te.persistTask(setup)
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
				"state": "idle", "summary": "Agent finished responding",
				"assistant_message_id": assistantMsgID,
				"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
				"cache_tokens": cacheTokens,
				"prompt_ms":    promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": duration,
			}})

			// Auto-summarize if context is approaching the window limit. Runs right
			// after the message completes, without waiting for the task transition.
			// The task goes idle here (context is NOT fed back to the LLM), so no
			// "Proceed" is injected — a queued real user message supersedes it.
			te.maybeAutoSummarize(setup, lc, activeModel, false)

			// Check for queued user messages.
			select {
			case um := <-setup.userMsgChan:
				if um.ContextChanged {
					log.Printf("[context-changed] setup.task=%s conversation context changed after response, re-syncing", setup.task.ID)
					lc.detector.reset()
					lc.detector.clearHints()
					lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
					// A fresh LLM call follows - re-acquire the running-task slot.
					if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
						te.handleCancelledTask(setup.task.ID, setup.projectID)
						return
					}
					continue
				}
				log.Printf("[user-msg] setup.task=%s received message after response, continuing (setup.regenerate=%v)", setup.task.ID, um.Regenerate)
				lc.detector.reset() // user input breaks any loop pattern
				lc.detector.clearHints()
				if um.Regenerate {
					lc.messages = truncateForRegenerate(lc.messages)
					lc.messages = append(lc.messages, userMessageToChatMessage(um))
				} else {
					// Re-sync with DB to pick up the user message (already created by HandleSend)
					// and any deleted messages. Do NOT append — syncMessagesFromDB already includes it.
					lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
				}
				if um.ModelName != "" {
					switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, um.ServerURL, um.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
				// The task went idle after the response - re-acquire the
				// running-task slot (a provider switch rides along via lc.mc).
				if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
					te.handleCancelledTask(setup.task.ID, setup.projectID)
					return
				}
				setup.task.ExecutionState = "running"
				te.persistTask(setup)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
				continue
			default:
			}

			// If this is a subtask, auto-start the next sibling
			if setup.task.ParentTaskID != "" {
				_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID)
			}
		} else if lc.reasoningContent.Len() > 0 {
			// Reasoning-only response: no visible content, no tool calls.
			// Persist the reasoning, inject a hint, and retry.
			reasoningText := lc.reasoningContent.String()

			// Persist the reasoning-only assistant message
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages, "", reasoningText)
			logStatsSpeed(setup.task.ID, activeModel, promptTokens-cacheTokens, predictedTokens, promptMs, predictedMs)
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "assistant",
				Content: "", ReasoningContent: reasoningText,
				ModelName: activeModel, ProviderName: lc.providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: time.Since(lc.callStartTime).Milliseconds(), CreatedAt: time.Now().UTC(),
			})

			// Append reasoning to conversation history so the LLM sees its own thinking.
			// Put in ReasoningContent so toAPIMessage sends it as reasoning_content.
			lc.messages = append(lc.messages, types.ChatMessage{Role: "assistant", Content: "", ReasoningContent: reasoningText})

			// Advance loop detector and check max hints (injects hint into messages + DB + WebSocket)
			lc.detector.recordTurn(nil, "", lc.finishReason)
			if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, LoopReasoningOnly, "", &lc.messages, setup.task.IsExploreMode()) {
				return
			}

			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()
			lc.finishReason = ""
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Reasoning-only response, requesting action"}})

			// Auto-summarize if context is approaching the window limit
			te.maybeAutoSummarize(setup, lc, activeModel, true)

			continue
		}

		// Block for user message before next LLM call
	idleWait:
		for {
			select {
			case <-setup.ctx.Done():
				te.handleCancelledTask(setup.task.ID, setup.projectID)
				return
			case req := <-setup.summarizeCh:
				// Manual summarization request from HandleSummarize.
				// Check if it was cancelled (user clicked ✕ or new request arrived).
				if req.ctx.Err() != nil {
					if req.ctx.Err() == context.Canceled {
						log.Printf("[summarize] setup.task=%s summarization cancelled", setup.task.ID)
					}
					req.resultCh <- req.ctx.Err()
					continue idleWait
				}
				log.Printf("[summarize] setup.task=%s manual summarization requested", setup.task.ID)
				// Fallback (non-utility) summarization requests go to the task's
				// CURRENT provider - lc.mc follows mid-run switches.
				sumURL, _ := lc.mc.get()
				lc.messages = te.applySummarization(req.ctx, setup.task, setup.projectID, setup.task.ID, lc.messages, sumURL, activeModel, req.resultCh)
				lc.detector.reset() // context changed, clear stale text window
				lc.detector.clearHints()
				// Count messages before DB sync (includes in-memory summary).
				preSyncCount := len(lc.messages)
				// Re-sync from DB to pick up any messages queued during summarization.
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
				// Clear summarizing flag
				setup.task.Summarizing = false
				_ = te.store.SetTaskSummarizing(setup.task.ID, false)
				te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
				// If DB has more messages than we had in-memory, new input arrived.
				hasQueuedInput := len(lc.messages) > preSyncCount
				if !hasQueuedInput {
					// No new user input — stay idle, don't call LLM with just the summary.
					// Defensive release: the task is expected not to hold a slot
					// while idle (it was released when it went idle).
					te.releaseTaskSlot(setup)
					setup.task.Status = "review"
					setup.task.ExecutionState = "idle"
					te.persistTask(setup)
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					// Re-block for a user message instead of looping into a fresh LLM call.
					continue idleWait
				}
				// User message present — continue to LLM call. The task was idle,
				// so re-acquire the running-task slot first (may queue).
				if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
					te.handleCancelledTask(setup.task.ID, setup.projectID)
					return
				}
				setup.task.Status = "in_progress"
				setup.task.ExecutionState = "running"
				te.persistTask(setup)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing queued message"}})
				break idleWait
			case um := <-setup.userMsgChan:
				if um.ContextChanged {
					log.Printf("[context-changed] setup.task=%s conversation context changed while idle, re-syncing", setup.task.ID)
					lc.detector.reset()
					lc.detector.clearHints()
					lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
					continue idleWait
				}
				log.Printf("[user-msg] setup.task=%s received message while idle, continuing (setup.regenerate=%v)", setup.task.ID, um.Regenerate)
				lc.detector.reset()
				lc.detector.clearHints()
				if um.Regenerate {
					lc.messages = truncateForRegenerate(lc.messages)
					lc.messages = append(lc.messages, userMessageToChatMessage(um))
				} else {
					// Re-sync with DB to pick up the user message
					lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
				}
				if um.ModelName != "" && !te.configStore.IsAgentProvider(um.ServerURL) {
					switched := te.applyModelSwitch(lc.mc, setup.task, setup.projectID, um.ServerURL, um.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
				// The task was idle - re-acquire the running-task slot (a
				// provider switch rides along via lc.mc) before the LLM call.
				if curURL, _ := lc.mc.get(); curURL != "" && !te.syncTaskSlot(setup, curURL) {
					te.handleCancelledTask(setup.task.ID, setup.projectID)
					return
				}
				setup.task.ExecutionState = "running"
				te.persistTask(setup)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
				break idleWait
			}
		}
	}
}

// sendAndPersistError sends an error via WebSocket and persists it.
func (te *TaskExecutor) sendAndPersistError(taskID, message, code string) {
	task, _ := te.store.GetTaskByID(taskID)
	errMsg := &types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, Role: "error",
		Content: fmt.Sprintf("[%s] %s", code, message), CreatedAt: time.Now(),
	}
	if task != nil {
		errMsg.ThreadID = task.CurrentThreadID
		te.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeError, Data: map[string]string{"message": message, "code": code}})
	}
	if err := te.store.CreateTaskMessage(errMsg); err != nil {
		log.Printf("[error-persist] task=%s failed to persist error: %v", taskID, err)
	}
}

// syncMessagesFromDB re-syncs the in-memory conversation with the database.
// When messages are deleted from the DB (e.g., user deletes chat messages),
// the in-memory lc.messages slice is stale. This function reloads messages
// from the DB and rebuilds the conversation, ensuring deleted messages are
// excluded from the next LLM call. It also refreshes the task to pick up
// any thread changes from fork cleanup.
// Returns the synced messages and the (possibly updated) task.
func (te *TaskExecutor) syncMessagesFromDB(task *types.Task, worktreePath, projectName string, sandboxEnabled bool) ([]types.ChatMessage, *types.Task) {
	// Reload task from DB to pick up thread changes (e.g., fork cleanup switched active thread).
	if updatedTask, err := te.store.GetTaskByID(task.ID); err == nil && updatedTask != nil {
		task = updatedTask
	}
	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	return buildMessagesFromDB(task, taskMsgs, worktreePath, projectName, sandboxEnabled), task
}

// truncateForRegenerate removes the last user message and everything after it
// from the in-memory conversation. When a regenerate arrives, the DB has already
// trimmed messages from the last user message onward (DeleteTaskMessagesAfter),
// but lc.messages still retains the stale entries. This function syncs the
// in-memory state by finding the last user message and discarding it plus
// everything after it. The new (re-sent) user message is then appended.
func truncateForRegenerate(messages []types.ChatMessage) []types.ChatMessage {
	for i := len(messages) - 1; i > 0; i-- {
		if messages[i].Role == "user" {
			return messages[:i]
		}
	}
	return messages
}

// removeDuplicateUserMessage removes the first of two consecutive user messages
// with identical content. During regeneration, a race between the DELETE and POST
// handlers can leave the old user message in the DB alongside the new one.
// Compares both Content and ContentParts (for messages with image attachments).
func removeDuplicateUserMessage(messages []types.ChatMessage) []types.ChatMessage {
	for i := 1; i < len(messages); i++ {
		if messages[i].Role == "user" && messages[i-1].Role == "user" && userMessagesEqual(messages[i-1], messages[i]) {
			// Keep the newer one (at index i), remove the older one (at index i-1)
			messages = append(messages[:i-1], messages[i:]...)
			break
		}
	}
	return messages
}

// userMessagesEqual compares two user messages for content identity.
// Checks both plain Content and ContentParts (multi-modal messages store text in ContentParts[0].Text).
func userMessagesEqual(a, b types.ChatMessage) bool {
	if len(a.ContentParts) > 0 && len(b.ContentParts) > 0 {
		// Both have multi-modal parts — compare text parts and attachment count
		if len(a.ContentParts) != len(b.ContentParts) {
			return false
		}
		for j := range a.ContentParts {
			if a.ContentParts[j].Text != b.ContentParts[j].Text {
				return false
			}
			// Compare image presence (not raw data — attachments may differ between stale/new)
			aHasImg := a.ContentParts[j].ImageSource != nil
			bHasImg := b.ContentParts[j].ImageSource != nil
			if aHasImg != bHasImg {
				return false
			}
		}
		return true
	}
	// Plain text comparison
	return a.Content == b.Content
}

func (te *TaskExecutor) gitRun(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	_, err := c.CombinedOutput()
	return err
}

// failTask sets the task to failed/review state, persists it, broadcasts the
// update, and sends a WebSocket error event.
func (te *TaskExecutor) failTask(task *types.Task, projectID, output, code string) {
	task.ExecutionState = "failed"
	task.Status = "review"
	task.Output = output
	te.storeTask(task, projectID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	te.sendAndPersistError(task.ID, output, code)
}

func computeStats(usage *types.ChatUsage, messages []types.ChatMessage, assistantText, reasoningContent string) (promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int) {
	if usage != nil {
		return usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, int(usage.PromptMs), int(usage.PredictedMs)
	}
	// usage is nil only when ChatStream returns no SSE data at all (empty/error stream).
	// Callers should treat zero stats as "unknown" — no estimation needed.
	return 0, 0, 0, 0, 0
}

// logStatsSpeed logs prompt and generation speeds for a turn.
func logStatsSpeed(taskID, model string, processedTokens, predictedTokens, promptMs, predictedMs int) {
	var promptTps, genTps float64
	if promptMs > 0 {
		promptTps = float64(processedTokens) * 1000.0 / float64(promptMs)
	}
	if predictedMs > 0 {
		genTps = float64(predictedTokens) * 1000.0 / float64(predictedMs)
	}
	log.Printf("[stats] task=%s model=%s prompt=%.0f t/s, gen=%.0f t/s (prompt_tokens=%d, gen_tokens=%d, prompt_ms=%d, gen_ms=%d)",
		taskID, model, promptTps, genTps, processedTokens, predictedTokens, promptMs, predictedMs)
}

// extractFilesFromBashCommand parses a bash command and extracts file paths that
// are likely being written to or modified. Only returns paths with known code file
// extensions (e.g., .go, .ts, .py, .rs) — the triggerLSPDiagnostics function
// further filters by installed LSP extensions.
//
// Handles common patterns:
//   - Shell redirections: > file, >> file
//   - echo/cat/printf ... > file
//   - tee file
//   - sed -i 's/...' file
//   - mv old new (returns new)
//   - cp src dest (returns dest)
//   - touch file
func extractFilesFromBashCommand(cmd string) []string {
	var files []string
	seen := make(map[string]bool)

	addFile := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || path == "." || path == "-" {
			return
		}
		// Remove quotes
		path = strings.Trim(path, "\"'")
		if seen[path] {
			return
		}
		// Only include files with extensions (skip directories, /dev/null, etc.)
		if filepath.Ext(path) == "" {
			return
		}
		// Skip non-code files
		if isImageFile(path) || strings.HasPrefix(path, "/dev/") {
			return
		}
		seen[path] = true
		files = append(files, path)
	}

	// Split into lines for multi-line commands, then split each line by &&/||
	for _, line := range strings.Split(cmd, "\n") {
		for _, part := range splitChainedCommands(line) {
			part = strings.TrimSpace(part)
			if part == "" || strings.HasPrefix(part, "#") {
				continue
			}

			// 1. Shell redirections: > file, >> file
			// Match patterns like: echo "foo" > file.ts or cat > file.ts <<EOF
			for _, re := range []string{">>", ">"} {
				parts := strings.Split(part, re)
				if len(parts) < 2 {
					continue
				}
				// Check each part after the first (before the command) for file paths.
				// The first redirect target is the one we want (e.g., > file 2>&1).
				// parts[1] is right after the first >, parts[2+] are from 2> etc.
				for i := 1; i < len(parts); i++ {
					right := strings.TrimSpace(parts[i])
					if right == "" {
						continue
					}
					// Skip stderr redirect patterns like 2>&1, 2>/dev/null
					if strings.HasPrefix(right, "2") {
						continue
					}
					// Skip if it's a heredoc (starts with <<)
					if strings.HasPrefix(right, "<<") {
						continue
					}
					// Skip pipes
					if strings.HasPrefix(right, "|") {
						continue
					}
					// Extract just the filename (may have trailing commands)
					fields := strings.Fields(right)
					if len(fields) == 0 {
						continue
					}
					addFile(fields[0])
					break // only the first valid redirect matters
				}
			}

			// 2. sed -i 's/...' file (and similar in-place edit commands)
			if strings.Contains(part, "sed ") && strings.Contains(part, "-i") {
				fields := strings.Fields(part)
				for i, f := range fields {
					if f == "sed" && i+1 < len(fields) {
						j := i + 1
						if j < len(fields) && fields[j] == "-i" {
							j++
						}
						if j < len(fields) && !strings.HasPrefix(fields[j], "-") {
							j++
						}
						for k := j; k < len(fields); k++ {
							if !strings.HasPrefix(fields[k], "-") && fields[k] != "|" && fields[k] != "&&" && fields[k] != "||" {
								addFile(fields[k])
							}
						}
					}
				}
			}

			// 3. tee file
			if idx := strings.Index(part, "tee "); idx != -1 {
				rest := strings.TrimSpace(part[idx+4:])
				fields := strings.Fields(rest)
				for _, f := range fields {
					if !strings.HasPrefix(f, "-") && f != "|" && f != "&&" && f != "||" {
						addFile(f)
					}
				}
			}

			// 4. mv old new (returns destination)
			if strings.HasPrefix(part, "mv ") {
				fields := strings.Fields(part)
				for i, f := range fields {
					if f == "mv" && i+2 < len(fields) {
						j := i + 1
						for j < len(fields) && strings.HasPrefix(fields[j], "-") {
							j++
						}
						if j+1 < len(fields) {
							addFile(fields[j+1])
						}
						break
					}
				}
			}

			// 5. cp src dest (returns destination)
			if strings.HasPrefix(part, "cp ") {
				fields := strings.Fields(part)
				for i, f := range fields {
					if f == "cp" && i+2 < len(fields) {
						j := i + 1
						for j < len(fields) && strings.HasPrefix(fields[j], "-") {
							j++
						}
						for k := len(fields) - 1; k > j; k-- {
							if !strings.HasPrefix(fields[k], "-") && fields[k] != "|" && fields[k] != "&&" && fields[k] != "||" {
								addFile(fields[k])
								break
							}
						}
						break
					}
				}
			}

			// 6. touch file
			if strings.HasPrefix(part, "touch ") {
				fields := strings.Fields(part)
				for i, f := range fields {
					if f == "touch" {
						for j := i + 1; j < len(fields); j++ {
							if !strings.HasPrefix(fields[j], "-") && fields[j] != "|" && fields[j] != "&&" && fields[j] != "||" {
								addFile(fields[j])
							}
						}
						break
					}
				}
			}

			// 7. Commands that write to a file via argument: gofmt -w file
			if strings.Contains(part, " -w ") {
				fields := strings.Fields(part)
				for i, f := range fields {
					if f == "-w" && i+1 < len(fields) {
						addFile(fields[i+1])
					}
				}
			}
		}
	}

	return files
}

// splitChainedCommands splits a bash command line by && and || operators,
// respecting quoted strings. This allows processing each sub-command separately.
func splitChainedCommands(line string) []string {
	var parts []string
	var current strings.Builder
	inSingleQuote, inDoubleQuote := false, false
	i := 0

	for i < len(line) {
		ch := line[i]

		// Track quote state
		switch ch {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '\\':
			if inSingleQuote {
				current.WriteByte(ch)
			} else {
				// Escape next char
				current.WriteByte(ch)
				if i+1 < len(line) {
					i++
					current.WriteByte(line[i])
				}
				i++
				continue
			}
		}

		// Check for && or || outside quotes
		if !inSingleQuote && !inDoubleQuote {
			if i+1 < len(line) && ((line[i:i+2] == "&&") || (line[i:i+2] == "||")) {
				parts = append(parts, strings.TrimSpace(current.String()))
				current.Reset()
				i += 2
				continue
			}
		}

		current.WriteByte(ch)
		i++
	}

	// Add the last part
	if last := strings.TrimSpace(current.String()); last != "" {
		parts = append(parts, last)
	}

	return parts
}

// buildToolResultEvent constructs the data payload for a WSTypeToolResult event.
// For bash and read_file tools, the output is truncated to ~1KB/10 lines with
// metadata about the full output. For all other tools, the full output is sent.
func buildToolResultEvent(toolID, toolName, result string, success bool, duration int64,
	args map[string]any, rawArgs, path string, sandbox, streamed bool, messageID string) map[string]any {
	data := map[string]any{
		"tool_id":  toolID,
		"success":  success,
		"duration": duration,
		"args":     args,
		"raw_args": rawArgs,
		"path":     path,
		"sandbox":  sandbox,
	}

	if toolName == "bash" || toolName == "read_file" {
		preview, originalLines, originalBytes, truncated := toolresult.Truncate(result)
		data["output"] = preview
		data["result_truncated"] = truncated
		data["original_lines"] = originalLines
		data["original_bytes"] = originalBytes
		data["streamed"] = streamed
		data["message_id"] = messageID
	} else {
		data["output"] = result
	}

	return data
}
