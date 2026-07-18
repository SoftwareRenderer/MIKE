package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kanban/backend/git"
	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/llama"
	"kanban/backend/prompts"
	"kanban/backend/stream"
	"kanban/backend/types"

	sandbox "kanban/pkg/bridge"

	"github.com/google/uuid"
)

// PiSentinelURL is the sentinel value for llama_server_url when a task uses Pi.
const PiSentinelURL = "pi://agent"

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

// unstageNewBinaryFilesInDir is like unstageNewBinaryFiles but uses a directory path.
func unstageNewBinaryFilesInDir(dir string) {
	out, err := runGitCommand(dir, "diff", "--staged", "--numstat", "--diff-filter=A")
	if err != nil || len(strings.TrimSpace(out)) == 0 {
		return
	}
	paths := binaryPathsFromStaged(out)
	unstagePathsInDir(dir, paths)
}

// streamFlusher batches token, reasoning, and tool_call WebSocket events
// and sends them at most once every throttleInterval (100ms).
// This reduces WebSocket write overhead during fast LLM streaming.
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
	checkpointIdx    int // messages index of last checkpoint
}

func (te *TaskExecutor) executeTask(setup *taskExecSetup) {
	defer func() {
		close(setup.done) // signal executor exit for ForwardUserMessage type-switch
		if r := recover(); r != nil {
			log.Printf("[panic] setup.task=%s recovered from panic: %v", setup.task.ID, r)
			te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
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
			if state.executorType != "pi" {
				delete(te.tasks, setup.task.ID)
			}
		}
		te.taskMu.Unlock()
		// Clear assigned ports when setup.task execution ends
		if len(setup.task.AssignedPorts) > 0 {
			setup.task.AssignedPorts = nil
			te.store.UpdateTask(setup.task)
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
		}
		// Don't remove the stream here — the broadcaster lives as long as clients are connected.
		// It's removed only when the setup.task is deleted (via RemoveStream in cleanupTask).
		// Sandbox container is also kept alive until setup.task deletion.
	}()

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
		state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite = makeSandboxCallbacks(agentClient)
		state.sandboxClient = agentClient
		state.sandboxOperatingMode = setup.task.OperatingMode
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

	// Check if this setup.task uses Pi agent
	if setup.serverURL == PiSentinelURL {
		log.Printf("[exec] setup.task=%s detected Pi agent (url=%s), calling executeTaskWithPi", setup.task.ID, setup.serverURL)
		te.executeTaskWithPi(setup.ctx, setup.task, setup.projectID, setup.project.Name, setup.worktreePath, setup.userMsgChan, setup.summarizeCh, setup.sandboxEnabled, setup.initialContent)
		log.Printf("[exec] setup.task=%s executeTaskWithPi returned", setup.task.ID)
		return
	}

	mc := &modelConfig{serverURL: setup.serverURL, modelName: setup.modelName}
	providerName := te.configStore.GetProviderNameByURL(setup.serverURL)
	reasoningEffort := setup.task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(setup.modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(setup.serverURL)
	client := llama.NewLlamaClient(setup.serverURL, setup.modelName, reasoningEffort, apiKey)
	// Select tools based on operating mode
	var tools []types.ChatTool
	if setup.task.IsPlanMode() {
		tools = llama.GetPlanModeToolDefinitions()
	} else if setup.task.IsIdeationMode() {
		tools = llama.GetIdeationModeToolDefinitions()
	} else {
		tools = llama.GetToolDefinitions()
	}
	taskMsgs, _ := te.store.GetTaskMessages(setup.task.ID, setup.task.CurrentThreadID)
	messages := buildMessagesFromDB(setup.task, taskMsgs, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)

	// During regeneration, HandleSend creates the re-sent user message in the DB.
	// If the DELETE hasn't completed yet (race between DELETE and POST handlers),
	// the old user message may still be in the DB alongside the new one, creating
	// duplicate consecutive user messages. Remove the stale copy.
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
	lc := &loopContext{
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

	for {
		select {
		case <-setup.ctx.Done():
			te.handleStopped(setup.task, setup.projectID, setup.task.ID, "Stopped by user", "stopped", true)
			return
		default:
		}

		// Check for model/provider changes before each LLM call.
		if updatedTask, err := te.store.GetTaskByTaskID(setup.task.ID); err == nil {
			newURL := updatedTask.LlamaServerURL
			newModel := updatedTask.ModelName
			curURL, curModel := lc.mc.get()
			if newURL != "" && newURL != curURL || newModel != "" && newModel != curModel {
				// Skip model switch if target is Pi agent — requires different executor
				if newURL == PiSentinelURL {
					log.Printf("[model-switch] setup.task=%s skipping switch to Pi agent (requires executor restart)", setup.task.ID)
				} else {
					if newURL == "" {
						newURL = curURL
					}
					if newModel == "" {
						newModel = curModel
					}
					switched := te.applyModelSwitch(lc.mc, setup.task, newURL, newModel, false, true)
					lc.client = switched.client
					lc.providerName = switched.providerName
					if switched.contextWindow != nil {
						lc.contextWindow = switched.contextWindow
					}
					lc.detector.reset() // model changed, clear stale text window
					lc.detector.clearHints()
					log.Printf("[model-switch] setup.task=%s url=%s model=%s context_window=%v", setup.task.ID, newURL, newModel, lc.contextWindow)
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
					createErr, assignedPorts := te.sandboxMgr.Create(setup.task.ID, setup.worktreePath, setup.project.Name, setup.projectPath, setup.projectID, kanban_sandbox.SandboxOpts{
					BaseImage:        imageToUse,
					NetworkIsolation: setup.sandboxOpts.NetworkIsolation,
					MemoryLimit:      setup.sandboxOpts.MemoryLimit,
					ForwardPorts:     setup.sandboxOpts.ForwardPorts,
					PiAgentDir:       te.configStore.GetAgentConfigDir(),
					SkillsDir:        prompts.SkillsDir(),
					SandboxMounts:    setup.sandboxOpts.SandboxMounts,
					SandboxEnvs:      setup.sandboxOpts.SandboxEnvs,
					PlanMode:         updatedTask.IsPlanMode(),
				})
					if createErr != nil {
						log.Printf("[mode-switch] setup.task=%s failed to recreate sandbox: %v", setup.task.ID, createErr)
						setup.task.ExecutionState = "failed"
						setup.task.Status = "review"
						setup.task.Output = fmt.Sprintf("Failed to recreate sandbox for mode change: %v", createErr)
						te.store.UpdateTask(setup.task)
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
						te.store.UpdateTask(setup.task)
						te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
						te.sendAndPersistError(setup.task.ID, fmt.Sprintf("Agent connection failed after mode change: %v", clientErr), "agent_connection_failed")
						return
					}
					te.taskMu.Lock()
					state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite = makeSandboxCallbacks(newAgentClient)
					state.sandboxClient = newAgentClient
					state.sandboxOperatingMode = newOperatingMode
					te.taskMu.Unlock()
					// Update setup.task reference with latest mode
					setup.task.OperatingMode = updatedTask.OperatingMode
									// Update tools if mode affects them
				if updatedTask.IsPlanMode() {
					tools = llama.GetPlanModeToolDefinitions()
				} else if updatedTask.IsIdeationMode() {
					tools = llama.GetIdeationModeToolDefinitions()
				} else {
					tools = llama.GetToolDefinitions()
				}
					// Broadcast assigned ports if any changed
					if len(assignedPorts) > 0 {
						setup.task.AssignedPorts = assignedPorts
						te.store.UpdateTask(setup.task)
						te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
						te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypePorts, Data: types.StreamPortsEvent{TaskID: setup.task.ID, Ports: assignedPorts}})
					}
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Sandbox recreated for mode change (%s)", newOperatingMode)}})
				}
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
				if um.ModelName != "" && um.ServerURL != PiSentinelURL {
					switched := te.applyModelSwitch(lc.mc, setup.task, um.ServerURL, um.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
			}
		default:
		}

		// Acquire concurrency gate slot before LLM call.
		_, activeModel := lc.mc.get()
		concurrency := te.configStore.GetProviderConcurrency(setup.serverURL)

		// Queue status polling — runs while waiting for a gate slot.
		queueDone := make(chan struct{})
		var entry *queueEntry
		go func() {
			tick := time.NewTicker(1 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-queueDone:
					return
				case <-setup.ctx.Done():
					return
				case <-tick.C:
					pos := te.gate.Position(setup.serverURL, entry)
					est := te.gate.EstimateWait(setup.serverURL, entry)
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeQueue, Data: map[string]any{
						"position":               pos,
						"estimated_wait_seconds": est,
					}})
				}
			}
		}()

		_, entry, err := te.gate.Acquire(setup.ctx, setup.serverURL, concurrency)
		if err != nil {
			close(queueDone) // cleanup polling goroutine
			te.handleStopped(setup.task, setup.projectID, setup.task.ID, "Stopped by user", "stopped", true)
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

		// Stream flusher: batches token/reasoning/tool_call events and sends
		// at most once every 500ms to reduce WebSocket write overhead.
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
					flusher.appendToolCall(tc.ID, tc.Function.Name, llama.GetToolDisplayName(tc.Function.Name), path, tc.Function.Arguments, argLen, prevLen, isNew, lc.sandboxEnabled && isSandboxableTool(tc.Function.Name))
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
			// Send stats event when predicted token count changes.
			// LLM client accumulates usage/timings from each SSE frame.
			if lc.usage != nil {
				predictedN := lc.usage.CompletionTokens
				if predictedN != lastPredictedN {
					lastPredictedN = predictedN
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
						"model_name":       activeModel,
						"prompt_tokens":    lc.usage.PromptTokens,
						"predicted_tokens": predictedN,
						"predicted_ms":     lc.usage.PredictedMs,
					}})
				}
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
			lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, tokenCb, progressCb)

			// Retry on transient errors (skip if cancelled by user message).
			if streamErr != nil && callCtx.Err() == nil {
				for i := range 3 {
					select {
					case <-callCtx.Done():
						flusher.flush()
						close(streamDone)
						return
					case <-time.After(time.Duration(i+1) * 2 * time.Second):
					}
					// Notify user of retry attempt
					retryMsg := fmt.Sprintf("Connection interrupted (%v), retrying... (attempt %d/3)", streamErr, i+2)
					log.Printf("[retry] setup.task=%s %s", setup.task.ID, retryMsg)
					te.store.CreateTaskMessage(&types.TaskMessage{
						ID: uuid.New().String(), TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID, Role: "system",
						Content: retryMsg, CreatedAt: time.Now().UTC(),
					})
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeHint, Data: map[string]any{
						"content":    retryMsg,
						"created_at": time.Now().UTC().Format(time.RFC3339),
					}})
					// Flush any remaining buffered events before retry.
					flusher.flush()
					// Reset builders so retry doesn't concatenate partial content.
					lc.assistantContent.Reset()
					lc.reasoningContent.Reset()
					lc.pendingToolCalls = nil
					lc.finishReason = ""
					flusher.reset()
					lc.callStartTime = time.Now()
					lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, tokenCb, progressCb)
					if streamErr == nil {
						break
					}
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
			te.gate.Release(setup.serverURL) // Release gate immediately so other tasks can proceed
			<-streamDone               // Then wait for goroutine to finish
			te.handleStopped(setup.task, setup.projectID, setup.task.ID, "Stopped by user", "stopped", false)
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
				te.gate.Release(setup.serverURL)
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
			te.gate.Release(setup.serverURL)

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
					switched := te.applyModelSwitch(lc.mc, setup.task, qm.ServerURL, qm.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
			}

			// Notify frontend that response was cancelled.
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "cancelled", "activity": "Response cancelled, processing new message"}})

			continue
		}
		callCancel()

		if streamErr != nil {
			log.Printf("[connect] setup.task=%s connection failed: %v, retrying...", setup.task.ID, streamErr)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Connection failed: %v, retrying...", streamErr)}})
			time.Sleep(5 * time.Second)
			continue
		}

		// Release gate slot after LLM call (before tool execution).
		te.gate.Release(setup.serverURL)

		assistantText := lc.assistantContent.String()

		type toolResult struct {
			toolCallID string
			output     string
		}
		var toolResults []toolResult
		var turnFailures []turnResult

		if len(lc.pendingToolCalls) > 0 {
			// Persist assistant message BEFORE tool calls so DB ordering matches LLM output order
			msgCountBefore := len(lc.messages)
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages[:msgCountBefore], assistantText, lc.reasoningContent.String())
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
					"sandbox":           lc.sandboxEnabled && isSandboxableTool(tc.Function.Name),
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
				if !lc.sandboxEnabled {
					te.GetWorktreeMu(setup.task.ID).Lock()
				}
				result, success := te.executeTool(setup.ctx, tc.Function.Name, args, setup.worktreePath, setup.task, state, func(toolID string, text string) {
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolOutput, Data: map[string]any{"tool_id": tc.ID, "text": text}})
				})
				if !lc.sandboxEnabled {
					te.GetWorktreeMu(setup.task.ID).Unlock()
				}
				toolDuration := time.Since(toolStart).Milliseconds()
				isSandboxed := lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]any{"tool_id": tc.ID, "success": success, "output": result, "duration": toolDuration, "args": args, "raw_args": tc.Function.Arguments, "path": extractPathFromArgs(tc.Function.Arguments), "sandbox": isSandboxed}})
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result})

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
							setup.task.ExecutionState = "completed"
							setup.task.Status = "done"
							if te.sandboxMgr != nil {
								_ = te.sandboxMgr.Stop(setup.task.ID)
							}
							te.store.UpdateTask(setup.task)
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
								_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID, setup.task.Status)
							}
							return
						}

						// No merge conflict — normal auto-commit path
						commitMsg := te.GenerateCommitMessageForTask(setup.ctx, setup.task.ID, setup.task)
						mergeErr := te.CommitAndMerge(setup.projectID, setup.task.ID, setup.worktreePath, commitMsg, setup.task.AutoMerge)
						setup.task, _ = te.store.GetTaskByTaskID(setup.task.ID)
						if setup.task.HasMergeConflict {
							// Merge conflict on first attempt — inject resolution prompt
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
							// Merge failed (non-conflict) — don't mark done
							log.Printf("[merge] task=%s merge failed, moving to review: %v", setup.task.ID, mergeErr)
							setup.task.ExecutionState = "idle"
							setup.task.Status = "review"
							setup.task.Output = "Merge failed: " + mergeErr.Error()
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
						// Task has a merge conflict (e.g., from HandleCommitAndDone).
						// LLM resolved conflicts by editing files — commit resolution,
						// finish the sync merge, then attempt the final merge.
						_, setup.task = te.commitConflictResolution(setup.projectID, setup.task.ID, setup.worktreePath, setup.task)
						if setup.task.HasMergeConflict {
							// Still conflicted after LLM called request_review — fail.
							log.Printf("[merge] setup.task=%s merge still conflicted after request_review", setup.task.ID)
							setup.task.ExecutionState = "failed"
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
					te.store.UpdateTask(setup.task)
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
						"state":                setup.task.ExecutionState,
						"assistant_message_id": assistantMsgID,
						"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
						"cache_tokens": cacheTokens,
						"prompt_ms":    promptMs, "predicted_ms": predictedMs,
						"model_name": activeModel, "duration": time.Since(lc.callStartTime).Milliseconds(),
					}})
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					// Post-tool git check: broadcast diff if a git command changed the state
					if hadGitCmd {
						te.broadcastDiffIfGitCmd(setup.projectID, setup.task.ID, setup.task)
					}
					// If this is a subtask, auto-start the next sibling
					if setup.task.ParentTaskID != "" {
						_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID, setup.task.Status)
					}
					return
				}
			}
			// Post-tool git check: broadcast diff if a git command changed the state
			if hadGitCmd {
				te.broadcastDiffIfGitCmd(setup.projectID, setup.task.ID, setup.task)
			}

			// Check if ask_question was called — stop executor, wait for user to restart
			for i, tc := range lc.pendingToolCalls {
				if tc.Function.Name == "ask_question" {
					question := toolResults[i].output
					setup.task.ExecutionState = "idle"
					setup.task.Status = "review"
					te.store.UpdateTask(setup.task)
					te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
					te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
						"state":    "idle",
						"question": question,
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

			// Check consecutive identical tool call failures (turn-level).
			if count := lc.tracker.recordTurn(turnFailures); count >= maxFailures {
				setup.task.ExecutionState = "failed"
				setup.task.Status = "review"
				setup.task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
				te.store.UpdateTask(setup.task)
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
				if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, loopType, target, &lc.messages, setup.task.IsIdeationMode()) {
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
				if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, loopType, "", &lc.messages, setup.task.IsIdeationMode()) {
					return
				}
				if !setup.task.IsIdeationMode() && !setup.task.IsPlanMode() {
					// In act mode: loop detected, inject hint, continue
					setup.task.ExecutionState = "running"
					te.store.UpdateTask(setup.task)
					continue
				}
				// In ideation/plan mode: text-only responses are valid (brainstorming / planning).
				// Fall through to end the turn.
			}
			// Valid text-only response: remove previously injected hints from context
			lc.messages = lc.detector.removeHints(lc.messages)

			// Check for queued user messages before sending complete.
			select {
			case um := <-setup.userMsgChan:
				if um.ContextChanged {
					log.Printf("[context-changed] setup.task=%s conversation context changed after response, re-syncing", setup.task.ID)
					lc.detector.reset()
					lc.detector.clearHints()
					lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
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
					switched := te.applyModelSwitch(lc.mc, setup.task, um.ServerURL, um.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
				setup.task.ExecutionState = "running"
				te.store.UpdateTask(setup.task)
				te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
				continue
			default:
			}

			setup.task.Status = "review"
			setup.task.ExecutionState = "idle"
			te.store.UpdateTask(setup.task)
			te.appStreamMgr.BroadcastTaskUpdated(setup.projectID, setup.task)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
				"state": "idle", "summary": "Agent finished responding",
				"assistant_message_id": assistantMsgID,
				"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
				"cache_tokens": cacheTokens,
				"prompt_ms":    promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": duration,
			}})
			// If this is a subtask, auto-start the next sibling
			if setup.task.ParentTaskID != "" {
				_ = te.AutoStartNextSibling(setup.projectID, setup.task.ID, setup.task.ParentTaskID, setup.task.Status)
			}
		} else if lc.reasoningContent.Len() > 0 {
			// Reasoning-only response: no visible content, no tool calls.
			// Persist the reasoning, inject a hint, and retry.
			reasoningText := lc.reasoningContent.String()

			// Persist the reasoning-only assistant message
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages, "", reasoningText)
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
			if handleLoopDetected(te, setup.task, setup.projectID, setup.task.ID, lc.detector, LoopReasoningOnly, "", &lc.messages, setup.task.IsIdeationMode()) {
				return
			}

			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()
			lc.finishReason = ""
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Reasoning-only response, requesting action"}})
			continue
		}

		// Auto-summarize if context is approaching the window limit.
		if lc.contextWindow != nil {
			if shouldSummarize(lc.messages, *lc.contextWindow) {
				lc.messages = te.applySummarization(setup.ctx, setup.task, setup.projectID, setup.task.ID, lc.messages, setup.serverURL, activeModel, nil)
				lc.detector.reset() // context changed, clear stale text window
				lc.detector.clearHints()
			}
		}

		// Block for user message before next LLM call.
		select {
		case <-setup.ctx.Done():
			te.handleStopped(setup.task, setup.projectID, setup.task.ID, "Stopped by user", "stopped", true)
			return
		case req := <-setup.summarizeCh:
			// Manual summarization request from HandleSummarize.
			// Check if it was cancelled (user clicked ✕ or new request arrived).
			if req.ctx.Err() != nil {
				if req.ctx.Err() == context.Canceled {
					log.Printf("[summarize] setup.task=%s summarization cancelled", setup.task.ID)
				}
				req.resultCh <- req.ctx.Err()
				continue
			}
			log.Printf("[summarize] setup.task=%s manual summarization requested", setup.task.ID)
			lc.messages = te.applySummarization(req.ctx, setup.task, setup.projectID, setup.task.ID, lc.messages, setup.serverURL, activeModel, req.resultCh)
			lc.detector.reset() // context changed, clear stale text window
			lc.detector.clearHints()
			continue
		case um := <-setup.userMsgChan:
			if um.ContextChanged {
				log.Printf("[context-changed] setup.task=%s conversation context changed while idle, re-syncing", setup.task.ID)
				lc.detector.reset()
				lc.detector.clearHints()
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
				continue
			}
			log.Printf("[user-msg] setup.task=%s received message while idle, continuing (setup.regenerate=%v)", setup.task.ID, um.Regenerate)
			lc.detector.reset()
			lc.detector.clearHints()
			if um.Regenerate {
				lc.messages = truncateForRegenerate(lc.messages)
				lc.messages = append(lc.messages, userMessageToChatMessage(um))
			} else {
				// Re-sync with DB to pick up the user message (already created by HandleSend)
				// and any deleted messages. Do NOT append — syncMessagesFromDB already includes it.
				lc.messages, setup.task = te.syncMessagesFromDB(setup.task, setup.worktreePath, setup.project.Name, setup.sandboxEnabled)
			}
			if um.ModelName != "" && um.ServerURL != PiSentinelURL {
				switched := te.applyModelSwitch(lc.mc, setup.task, um.ServerURL, um.ModelName, true, false)
				lc.client = switched.client
				lc.providerName = switched.providerName
			}
			setup.task.ExecutionState = "running"
			te.store.UpdateTask(setup.task)
			te.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
		}
	}
}

// sendAndPersistError sends an error via WebSocket and persists it.
func (te *TaskExecutor) sendAndPersistError(taskID, message, code string) {
	task, _ := te.store.GetTaskByTaskID(taskID)
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
	if updatedTask, err := te.store.GetTaskByTaskID(task.ID); err == nil && updatedTask != nil {
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

func computeStats(usage *types.ChatUsage, messages []types.ChatMessage, assistantText, reasoningContent string) (promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int) {
	if usage != nil {
		return usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, int(usage.PromptMs), int(usage.PredictedMs)
	}
	// usage is nil only when ChatStream returns no SSE data at all (empty/error stream).
	// Callers should treat zero stats as "unknown" — no estimation needed.
	return 0, 0, 0, 0, 0
}
