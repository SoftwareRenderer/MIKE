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
	"kanban/backend/llama"
	"kanban/backend/prompts"
	"kanban/backend/sandbox"
	"kanban/backend/stream"
	"kanban/backend/types"

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
// and sends them at most once every throttleInterval (500ms).
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
	detector         *loopDetector
	tracker          failureTracker
	mc               *modelConfig
	client           *llama.LlamaClient
	providerName     string
	contextWindow    *int
	systemPrompt     string
	sandboxEnabled   bool
	startTime        time.Time
	usage            *types.ChatUsage
	checkpointIdx    int // messages index of last checkpoint
}

func (te *TaskExecutor) executeTask(ctx context.Context, task *types.Task, projectID, projectName, worktreePath, projectPath, serverURL, modelName string, userMsgChan chan stream.UserMessage, summarizeCh chan chan error, sandboxEnabled bool, networkIsolation bool, baseImage, sandboxDockerfilePath string, memoryLimit int, initialContent string, regenerate bool, forwardPorts []types.PortForward, sandboxMounts []types.SandboxMount) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[panic] task=%s recovered from panic: %v", task.ID, r)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
		// Close os.Root to release file descriptor (non-sandbox tasks)
		te.taskMu.Lock()
		if state, exists := te.tasks[task.ID]; exists && state.root != nil {
			_ = state.root.Close()
		}
		te.taskMu.Unlock()
		// Only remove from tasks map if we're still the active executor
		// (prevents old executor from deleting a replacement during type switch)
		te.taskMu.Lock()
		if state, exists := te.tasks[task.ID]; exists && state.ctx == ctx {
			delete(te.tasks, task.ID)
		}
		te.taskMu.Unlock()
		// Clear assigned ports when task execution ends
		if len(task.AssignedPorts) > 0 {
			task.AssignedPorts = nil
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}
		// Don't remove the stream here — the broadcaster lives as long as clients are connected.
		// It's removed only when the task is deleted (via RemoveStream in cleanupTask).
		// Sandbox container is also kept alive until task deletion.
	}()

	// Create sandbox if enabled
	// Get state reference to store sandbox callbacks
	te.taskMu.Lock()
	state := te.tasks[task.ID]
	te.taskMu.Unlock()

	if sandboxEnabled {
		if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
			task.ExecutionState = "failed"
			task.Status = "review"
			task.Output = "Sandbox enabled but gVisor (runsc) is not available. Install gVisor: sudo bash scripts/install-gvisor.sh"
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, "gVisor not available", "sandbox_unavailable")
			return
		}
		// Determine image to use: Dockerfile build takes precedence over pre-built image
		imageToUse := ""
		if sandboxDockerfilePath != "" {
			imageToUse = sandbox.ProjectImageName(projectName)
			if !te.sandboxMgr.ImageReadyForProject(projectName) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "building_image", "activity": "Building sandbox image from Dockerfile"}})
				if err := te.sandboxMgr.BuildImageWithProgress(projectName, sandboxDockerfilePath, func(line string) {
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
				}); err != nil {
					task.ExecutionState = "failed"
					task.Status = "review"
					task.Output = fmt.Sprintf("Failed to build sandbox image: %v", err)
					te.store.UpdateTask(task)
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(task.ID, fmt.Sprintf("Image build failed: %v", err), "image_build_failed")
					return
				}
			}
		} else {
			// Pull pre-built image if not available locally
			imageToUse = baseImage
			if imageToUse == "" {
				imageToUse = sandbox.BaseImage
			}
			if !te.sandboxMgr.ImageReady(imageToUse) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "pulling_image", "activity": "Pulling " + imageToUse}})
				if err := te.sandboxMgr.PullImageWithProgress(imageToUse, func(line string) {
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
				}); err != nil {
					task.ExecutionState = "failed"
					task.Status = "review"
					task.Output = fmt.Sprintf("Failed to pull sandbox image: %v", err)
					te.store.UpdateTask(task)
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(task.ID, fmt.Sprintf("Image pull failed: %v", err), "image_pull_failed")
					return
				}
			}
		}
		// Mount agent config dir if any coding agent is configured.
		piAgentDir := te.configStore.GetAgentConfigDir()
		// Reuse existing container (lazy-start). Create only if none exists.
		hasExistingContainer := te.sandboxMgr.ContainerRunning(task.ID)
		if !hasExistingContainer {
			var assignedPorts []types.AssignedPort
			createErr, assignedPorts := te.sandboxMgr.Create(task.ID, worktreePath, imageToUse, projectName, projectPath, projectID, networkIsolation, memoryLimit, forwardPorts, piAgentDir, sandboxMounts)
			if createErr != nil {
				task.ExecutionState = "failed"
				task.Status = "review"
				task.Output = fmt.Sprintf("Failed to create sandbox: %v", createErr)
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.sendAndPersistError(task.ID, fmt.Sprintf("Sandbox creation failed: %v", createErr), "sandbox_create_failed")
				return
			}
			log.Printf("[sandbox] task=%s sandbox created with image %s", task.ID, imageToUse)
			// Store and broadcast assigned ports
			if len(assignedPorts) > 0 {
				task.AssignedPorts = assignedPorts
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypePorts, Data: types.StreamPortsEvent{TaskID: task.ID, Ports: assignedPorts}})
			}
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Sandbox container started"}})
		} else {
			log.Printf("[sandbox] task=%s reusing existing sandbox container", task.ID)
		}

		// Get agent client for tool calls (required — sandbox creation ensures agent is running)
		agentClient, err := te.sandboxMgr.GetClient(task.ID)
		if err != nil {
			task.ExecutionState = "failed"
			task.Status = "review"
			task.Output = fmt.Sprintf("Failed to connect to sandbox bridge: %v", err)
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Agent connection failed: %v", err), "agent_connection_failed")
			return
		}
		state.sandboxExec, state.sandboxExecStream, state.sandboxRead, state.sandboxWrite = makeSandboxCallbacks(agentClient)
		state.sandboxClient = agentClient
	} else {
		// Non-sandbox: use os.Root only for write isolation.
		// Read uses plain os.ReadFile (no isolation).
		root, err := os.OpenRoot(worktreePath)
		if err != nil {
			task.ExecutionState = "failed"
			task.Status = "review"
			task.Output = fmt.Sprintf("Failed to create worktree root: %v", err)
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(task.ID, fmt.Sprintf("Root creation failed: %v", err), "root_create_failed")
			return
		}
		state.root = root
		_, state.sandboxWrite = makeRootCallbacks(root, worktreePath)
	}

	// Check if this task uses Pi agent
	if serverURL == PiSentinelURL {
		log.Printf("[exec] task=%s detected Pi agent (url=%s), calling executeTaskWithPi", task.ID, serverURL)
		te.executeTaskWithPi(ctx, task, projectID, projectName, worktreePath, userMsgChan, summarizeCh, sandboxEnabled, initialContent)
		log.Printf("[exec] task=%s executeTaskWithPi returned", task.ID)
		return
	}

	mc := &modelConfig{serverURL: serverURL, modelName: modelName}
	providerName := te.configStore.GetProviderNameByURL(serverURL)
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	// In plan mode, restrict tools to read-only + create_task + task_complete
	var tools []types.ChatTool
	if task.PlanMode {
		tools = llama.GetPlanModeToolDefinitions()
	} else {
		tools = llama.GetToolDefinitions()
	}
	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	messages := buildMessagesFromDB(task, taskMsgs, worktreePath, projectName, sandboxEnabled)

	// During regeneration, HandleSend creates the re-sent user message in the DB.
	// If the DELETE hasn't completed yet (race between DELETE and POST handlers),
	// the old user message may still be in the DB alongside the new one, creating
	// duplicate consecutive user messages. Remove the stale copy.
	if regenerate {
		messages = removeDuplicateUserMessage(messages)
	}

	// Context tracking: load context window from provider config, discover if unknown
	var contextWindow *int
	if cw := te.configStore.GetModelContextWindow(modelName, serverURL); cw != nil {
		contextWindow = cw
	} else if discovered := te.configStore.DiscoverContextWindow(modelName, serverURL); discovered != nil {
		contextWindow = discovered
		te.configStore.SaveModelContextWindow(modelName, serverURL, *discovered)
		log.Printf("[context] discovered context window for %s: %d", modelName, *discovered)
	}

	maxFailures := task.MaxConsecutiveFailedTurns
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
		sandboxEnabled:   sandboxEnabled,
		startTime:        time.Now(),
	}

	for {
		select {
		case <-ctx.Done():
			te.handleStopped(task, projectID, task.ID, "Stopped by user", "stopped", true)
			return
		default:
		}

		// Check for model/provider changes before each LLM call.
		if updatedTask, err := te.store.GetTaskByTaskID(task.ID); err == nil {
			newURL := updatedTask.LlamaServerURL
			newModel := updatedTask.ModelName
			curURL, curModel := lc.mc.get()
			if newURL != "" && newURL != curURL || newModel != "" && newModel != curModel {
				// Skip model switch if target is Pi agent — requires different executor
				if newURL == PiSentinelURL {
					log.Printf("[model-switch] task=%s skipping switch to Pi agent (requires executor restart)", task.ID)
				} else {
					if newURL == "" {
						newURL = curURL
					}
					if newModel == "" {
						newModel = curModel
					}
					switched := te.applyModelSwitch(lc.mc, task, newURL, newModel, false, true)
					lc.client = switched.client
					lc.providerName = switched.providerName
					if switched.contextWindow != nil {
						lc.contextWindow = switched.contextWindow
					}
					lc.detector.reset() // model changed, clear stale text window
					log.Printf("[model-switch] task=%s url=%s model=%s context_window=%v", task.ID, newURL, newModel, lc.contextWindow)
				}
			}
		}

		// Check for user messages that arrived during tool execution.
		select {
		case um := <-userMsgChan:
			log.Printf("[user-msg] task=%s received message before LLM call (regenerate=%v)", task.ID, um.Regenerate)
			lc.detector.reset() // user input breaks any loop pattern
			if um.Regenerate {
				lc.messages = truncateForRegenerate(lc.messages)
			}
			lc.messages = append(lc.messages, userMessageToChatMessage(um))
			if um.ModelName != "" && um.ServerURL != PiSentinelURL {
				switched := te.applyModelSwitch(lc.mc, task, um.ServerURL, um.ModelName, true, false)
				lc.client = switched.client
				lc.providerName = switched.providerName
			}
		default:
		}

		// Acquire concurrency gate slot before LLM call.
		serverURL, activeModel := lc.mc.get()
		concurrency := te.configStore.GetProviderConcurrency(serverURL)

		// Queue status polling — runs while waiting for a gate slot.
		queueDone := make(chan struct{})
		go func() {
			tick := time.NewTicker(1 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-queueDone:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					pos := te.gate.Position(serverURL)
					est := te.gate.EstimateWait(serverURL)
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeQueue, Data: map[string]any{
						"position":               pos,
						"estimated_wait_seconds": est,
					}})
				}
			}
		}()

		_, err := te.gate.Acquire(ctx, serverURL, concurrency)
		if err != nil {
			close(queueDone) // cleanup polling goroutine
			te.handleStopped(task, projectID, task.ID, "Stopped by user", "stopped", true)
			return
		}
		close(queueDone)

		// Per-call context — can be cancelled independently to abort the LLM stream.
		callCtx, callCancel := context.WithCancel(ctx)

		streamDone := make(chan struct{})
		var streamErr error
		var lastProgressSend time.Time // throttle progress updates to ~1/2s
		var lastPredictedN int         // track predicted tokens for real-time stats events

		// Assemble LLM context (compresses history via summary if one exists).
		llmMessages := buildLLMContext(lc.messages)

		// Stream flusher: batches token/reasoning/tool_call events and sends
		// at most once every 500ms to reduce WebSocket write overhead.
		flusher := newStreamFlusher(projectID, task.ID, lc.sandboxEnabled,
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

		go func() {
			lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, func(choice types.ChatChoice) {
				if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
					lc.reasoningContent.WriteString(*choice.Delta.ReasoningContent)
					flusher.appendReasoning(*choice.Delta.ReasoningContent)
				}
				if choice.Delta.Content != nil && *choice.Delta.Content != "" {
					lc.assistantContent.WriteString(*choice.Delta.Content)
					flusher.appendToken(*choice.Delta.Content)
					// Real-time phrase repetition detection (not throttled — detection runs per-token)
					if loopType := lc.detector.checkStreamingText(*choice.Delta.Content); loopType == LoopPhrase {
						log.Printf("[loop] task=%s phrase repetition detected during streaming", task.ID)
						te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Repetitive response detected"}})
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
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": extractPathFromArgs(tc.Function.Arguments)}})
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
						te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
							"model_name":       activeModel,
							"prompt_tokens":    lc.usage.PromptTokens,
							"predicted_tokens": predictedN,
							"predicted_ms":     lc.usage.PredictedMs,
						}})
					}
				}
			}, func(progress types.PromptProgressData) {
				// Forward prompt processing progress to the frontend.
				// Throttle to ~2 updates/sec to avoid flooding the WebSocket.
				if time.Since(lastProgressSend) > 500*time.Millisecond {
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypePromptProgress, Data: map[string]any{
						"total":     progress.Total,
						"cache":     progress.Cache,
						"processed": progress.Processed,
						"time_ms":   progress.TimeMs,
					}})
					lastProgressSend = time.Now()
				}
			})

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
					log.Printf("[retry] task=%s %s", task.ID, retryMsg)
					te.store.CreateTaskMessage(&types.TaskMessage{
						ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "system",
						Content: retryMsg, CreatedAt: time.Now().UTC(),
					})
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeHint, Data: map[string]any{
						"content":    retryMsg,
						"created_at": time.Now().UTC().Format(time.RFC3339),
					}})
					// Flush any remaining buffered events before retry.
					flusher.flush()
					// Reset builders so retry doesn't concatenate partial content.
					lc.assistantContent.Reset()
					lc.reasoningContent.Reset()
					lc.pendingToolCalls = nil
					flusher.reset()
					lc.usage, streamErr = lc.client.ChatStream(callCtx, llmMessages, tools, func(choice types.ChatChoice) {
						if choice.Delta.ReasoningContent != nil {
							lc.reasoningContent.WriteString(*choice.Delta.ReasoningContent)
							flusher.appendReasoning(*choice.Delta.ReasoningContent)
						}
						if choice.Delta.Content != nil && *choice.Delta.Content != "" {
							lc.assistantContent.WriteString(*choice.Delta.Content)
							flusher.appendToken(*choice.Delta.Content)
						}
						if len(choice.Delta.ToolCalls) > 0 {
							lc.pendingToolCalls = llama.MergeToolCalls(lc.pendingToolCalls, choice.Delta.ToolCalls)
							for i := 0; i < len(lc.pendingToolCalls); i++ {
								tc := lc.pendingToolCalls[i]
								if tc.ID == "" || tc.Function.Name == "" {
									continue
								}
								argLen := len(tc.Function.Arguments)
								prevLen := lc.prevArgLens[tc.ID]
								lc.prevArgLens[tc.ID] = argLen
								if argLen != prevLen {
									path := extractPathFromArgs(tc.Function.Arguments)
									flusher.appendToolCall(tc.ID, tc.Function.Name, llama.GetToolDisplayName(tc.Function.Name), path, tc.Function.Arguments, argLen, prevLen, prevLen == 0, lc.sandboxEnabled && isSandboxableTool(tc.Function.Name))
								}
							}
						}
						// Send stats event when predicted token count changes (retry path)
						if lc.usage != nil {
							predictedN := lc.usage.CompletionTokens
							if predictedN != lastPredictedN {
								lastPredictedN = predictedN
								te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStats, Data: map[string]any{
									"model_name":       activeModel,
									"prompt_tokens":    lc.usage.PromptTokens,
									"predicted_tokens": predictedN,
									"predicted_ms":     lc.usage.PredictedMs,
								}})
							}
						}
					}, func(progress types.PromptProgressData) {
						if time.Since(lastProgressSend) > 500*time.Millisecond {
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypePromptProgress, Data: map[string]any{
								"total":     progress.Total,
								"cache":     progress.Cache,
								"processed": progress.Processed,
								"time_ms":   progress.TimeMs,
							}})
							lastProgressSend = time.Now()
						}
					})
					if streamErr == nil {
						break
					}
				}
			}

			// Flush any remaining buffered events before signalling completion.
			flusher.flush()
			close(streamDone)
		}()

		// Wait for: stream complete, new user message, or task stop.
		select {
		case <-ctx.Done():
			callCancel()
			te.gate.Release(serverURL) // Release gate immediately so other tasks can proceed
			<-streamDone               // Then wait for goroutine to finish
			te.handleStopped(task, projectID, task.ID, "Stopped by user", "stopped", false)
			return
		case <-streamDone:
			// Stream completed normally (or all retries exhausted)
		case um := <-userMsgChan:
			// New user message — cancel current LLM call.
			log.Printf("[cancel] task=%s user message received during streaming, cancelling (regenerate=%v)", task.ID, um.Regenerate)
			lc.detector.reset() // user input breaks any loop pattern
			callCancel()
			// Wait for goroutine to finish (with timeout in case HTTP stream is slow to abort).
			select {
			case <-streamDone:
			case <-time.After(2 * time.Second):
				log.Printf("[cancel] task=%s streamDone timeout, forcing continue", task.ID)
			}
			te.gate.Release(serverURL)

			// Drain all queued messages from channel (non-blocking).
			var queuedMsgs []stream.UserMessage
			queuedMsgs = append(queuedMsgs, um)
			for range 16 {
				select {
				case q := <-userMsgChan:
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

			// If any queued message is a regenerate, truncate stale assistant messages.
			for _, qm := range queuedMsgs {
				if qm.Regenerate {
					lc.messages = truncateForRegenerate(lc.messages)
					break
				}
			}

			// Add queued messages to conversation history.
			for _, qm := range queuedMsgs {
				lc.messages = append(lc.messages, userMessageToChatMessage(qm))
				if qm.ModelName != "" {
					switched := te.applyModelSwitch(lc.mc, task, qm.ServerURL, qm.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
			}

			// Notify frontend that response was cancelled.
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "cancelled", "activity": "Response cancelled, processing new message"}})

			continue
		}
		callCancel()

		if streamErr != nil {
			log.Printf("[connect] task=%s connection failed: %v, retrying...", task.ID, streamErr)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Connection failed: %v, retrying...", streamErr)}})
			time.Sleep(5 * time.Second)
			continue
		}

		// Release gate slot after LLM call (before tool execution).
		te.gate.Release(serverURL)

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
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages[:msgCountBefore], assistantText, lc.reasoningContent.String(), lc.startTime)
			assistantCreatedAt := time.Now().UTC()
			assistantMsgID := uuid.New().String()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: assistantMsgID, TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: lc.reasoningContent.String(),
				ModelName: activeModel, ProviderName: lc.providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: time.Since(lc.startTime).Milliseconds(), CreatedAt: assistantCreatedAt,
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
						te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolProgress, Data: map[string]any{"tool_id": tc.ID, "bytes_written": len(tc.Function.Arguments), "path": path}})
					}
				}
				// tool_call event already sent during streaming, just execute and send result
				log.Printf("[tool] executing %s for task=%s", tc.Function.Name, task.ID)
				toolStart := time.Now()
				te.taskMu.Lock()
				state := te.tasks[task.ID]
				te.taskMu.Unlock()
				result, success := te.executeTool(ctx, tc.Function.Name, args, worktreePath, task, state, func(toolID string, text string) {
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolOutput, Data: map[string]any{"tool_id": tc.ID, "text": text}})
				})
				toolDuration := time.Since(toolStart).Milliseconds()
				isSandboxed := lc.sandboxEnabled && isSandboxableTool(tc.Function.Name)
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]any{"tool_id": tc.ID, "success": success, "output": result, "duration": toolDuration, "args": args, "raw_args": tc.Function.Arguments, "path": extractPathFromArgs(tc.Function.Arguments), "sandbox": isSandboxed}})
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result})

				tcJSON, _ := json.Marshal(map[string]any{
					"tool_id":           tc.ID,
					"tool_name":         tc.Function.Name,
					"tool_display_name": llama.GetToolDisplayName(tc.Function.Name),
					"args":              args,
					"raw_args":          tc.Function.Arguments,
					"sandbox":           lc.sandboxEnabled && isSandboxableTool(tc.Function.Name),
				})
				resultJSON, _ := json.Marshal(map[string]any{
					"output":   result,
					"success":  success,
					"duration": toolDuration,
				})
				_ = te.store.CreateTaskMessage(&types.TaskMessage{
					ID:         uuid.New().String(),
					TaskID:     task.ID,
					ThreadID:   task.CurrentThreadID,
					Role:       "tool_call",
					Content:    "",
					ToolCalls:  string(tcJSON),
					ToolResult: string(resultJSON),
					CreatedAt:  time.Now().UTC(),
				})

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

				if tc.Function.Name == "task_complete" {
					// Extract summary from tool args
					if summary, ok := args["summary"].(string); ok && summary != "" {
						task.ChangeSummary = summary
					}
					if task.AutoCommit {
						// If a merge conflict was detected (sync-merge or final-merge),
						// the LLM has resolved conflicts by editing files. Commit the
						// resolution, finish the merge, then attempt the final merge.
						if task.HasMergeConflict {
							commitOK := true
							if isMergeInProgress(worktreePath) {
								// Sync-merge conflict: worktree has MERGE_HEAD with conflict markers.
								conflictedFiles := GetResolvedConflictedFiles(worktreePath)
								if len(conflictedFiles) > 0 {
									addArgs := append([]string{"add"}, conflictedFiles...)
									if err := te.gitRun(worktreePath, addArgs...); err != nil {
										log.Printf("[merge] task=%s git add failed: %v", task.ID, err)
										commitOK = false
									}
								}
							} else {
								// Final-merge conflict: worktree has no MERGE_HEAD.
								checkCmd := exec.Command("git", "diff", "--quiet")
								checkCmd.Dir = worktreePath
								if checkCmd.Run() == nil {
									log.Printf("[merge] task=%s no changes to commit for conflict resolution", task.ID)
									commitOK = false
								}
								if commitOK {
									if err := te.gitRun(worktreePath, "add", "-A"); err != nil {
										log.Printf("[merge] task=%s git add -A failed: %v", task.ID, err)
										commitOK = false
									}
								}
							}
							if commitOK {
								if err := te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
									log.Printf("[merge] task=%s git commit failed: %v", task.ID, err)
									commitOK = false
								}
							}
							if commitOK {
								log.Printf("[merge] task=%s conflict resolution committed", task.ID)
								// Drop stale stash (contains LLM's pre-sync edits, already committed).
								// Do NOT pop — that would re-apply old edits as unstaged changes.
								te.worktreeMgr.GetProjectStashMu(projectID).Lock()
								_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "drop")
								te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
							}
							_ = te.store.SetTaskMergeConflict(task.ID, projectID, false)

							if commitOK {
								// Final merge (task branch into target)
								if mergeErr := te.MergeBranch(projectID, task.ID, task); mergeErr != nil {
									log.Printf("[merge] task=%s final merge failed: %v", task.ID, mergeErr)
									task, _ = te.store.GetTaskByTaskID(task.ID)
								}
							}

							task, _ = te.store.GetTaskByTaskID(task.ID)
							if task.HasMergeConflict {
								// Still conflicted — inject resolution prompt and continue
								conflictedFiles := GetConflictedFiles(worktreePath)
								prompt, pErr := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
									TaskBranch:      task.Branch,
									TargetBranch:    task.BaseRef,
									TaskTitle:       task.Title,
									TaskDescription: task.Prompt,
									ConflictedFiles: conflictedFiles,
								})
								if pErr != nil {
									log.Printf("[merge] task=%s failed to render merge conflict prompt: %v", task.ID, pErr)
								}
								lc.detector.reset()
								lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: prompt})
								te.store.CreateTaskMessage(&types.TaskMessage{
									ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID,
									Role: "system", Content: "Merge conflict detected — resolving with tools",
									CreatedAt: time.Now().UTC(),
								})
								te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Resolving merge conflicts"}})
								lc.pendingToolCalls = nil
								lc.assistantContent.Reset()
								lc.reasoningContent.Reset()
								continue
							}
							task.ExecutionState = "completed"
							task.Status = "done"
							if te.sandboxMgr != nil {
								_ = te.sandboxMgr.Stop(task.ID)
							}
							te.store.UpdateTask(task)
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
								"state":                task.ExecutionState,
								"assistant_message_id": assistantMsgID,
								"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
								"cache_tokens": cacheTokens,
								"prompt_ms":    promptMs, "predicted_ms": predictedMs,
								"model_name": activeModel, "duration": time.Since(lc.startTime).Milliseconds(),
							}})
							te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
							if task.ParentTaskID != "" {
								_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
							}
							return
						}

						// No merge conflict — normal auto-commit path
						commitMsg := te.GenerateCommitMessageForTask(ctx, task.ID, task)
						te.CommitAndMerge(projectID, task.ID, worktreePath, commitMsg, task.AutoMerge)
						task, _ = te.store.GetTaskByTaskID(task.ID)
						if task.HasMergeConflict {
							// Merge conflict on first attempt — inject resolution prompt
							conflictedFiles := GetResolvedConflictedFiles(worktreePath)
							prompt, pErr := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
								TaskBranch:      task.Branch,
								TargetBranch:    task.BaseRef,
								TaskTitle:       task.Title,
								TaskDescription: task.Prompt,
								ConflictedFiles: conflictedFiles,
							})
							if pErr != nil {
								log.Printf("[merge] task=%s failed to render merge conflict prompt: %v", task.ID, pErr)
							}
							lc.detector.reset()
							lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: prompt})
							te.store.CreateTaskMessage(&types.TaskMessage{
								ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID,
								Role: "system", Content: "Merge conflict detected — resolving with tools",
								CreatedAt: time.Now().UTC(),
							})
							te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Resolving merge conflicts"}})
							lc.pendingToolCalls = nil
							lc.assistantContent.Reset()
							lc.reasoningContent.Reset()
							continue
						}
						task.ExecutionState = "completed"
						task.Status = "done"
						if te.sandboxMgr != nil {
							_ = te.sandboxMgr.Stop(task.ID)
						}
					} else if task.HasMergeConflict {
						// Task has a merge conflict (e.g., from HandleCommitAndDone).
						// LLM resolved conflicts by editing files — commit resolution,
						// finish the sync merge, then attempt the final merge.
						commitOK := true
						if isMergeInProgress(worktreePath) {
							// Sync-merge conflict: worktree has MERGE_HEAD with conflict markers.
							// Stage the resolved conflicted files.
							conflictedFiles := GetResolvedConflictedFiles(worktreePath)
							if len(conflictedFiles) > 0 {
								addArgs := append([]string{"add"}, conflictedFiles...)
								if err := te.gitRun(worktreePath, addArgs...); err != nil {
									log.Printf("[merge] task=%s git add failed: %v", task.ID, err)
									commitOK = false
								}
							}
						} else {
							// Final-merge conflict: worktree has no MERGE_HEAD.
							// Stage the LLM's file edits (it modified task branch to be merge-compatible).
							checkCmd := exec.Command("git", "diff", "--quiet")
							checkCmd.Dir = worktreePath
							if checkCmd.Run() == nil {
								log.Printf("[merge] task=%s no changes to commit for conflict resolution", task.ID)
								commitOK = false
							}
							if commitOK {
								if err := te.gitRun(worktreePath, "add", "-A"); err != nil {
									log.Printf("[merge] task=%s git add -A failed: %v", task.ID, err)
									commitOK = false
								}
							}
						}
						if commitOK {
							if err := te.gitRun(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", "Resolve merge conflicts")...); err != nil {
								log.Printf("[merge] task=%s git commit failed: %v", task.ID, err)
								commitOK = false
							}
						}
						if commitOK {
							log.Printf("[merge] task=%s conflict resolution committed", task.ID)
							// Drop stale stash (contains LLM's pre-sync edits, already committed).
							// Do NOT pop — that would re-apply old edits as unstaged changes.
							te.worktreeMgr.GetProjectStashMu(projectID).Lock()
							_ = te.worktreeMgr.GitRunInDir(worktreePath, "stash", "drop")
							te.worktreeMgr.GetProjectStashMu(projectID).Unlock()
						}
						_ = te.store.SetTaskMergeConflict(task.ID, projectID, false)

						if commitOK {
							if mergeErr := te.MergeBranch(projectID, task.ID, task); mergeErr != nil {
								log.Printf("[merge] task=%s final merge failed: %v", task.ID, mergeErr)
								task, _ = te.store.GetTaskByTaskID(task.ID)
							}
						}

						task, _ = te.store.GetTaskByTaskID(task.ID)
						if task.HasMergeConflict {
							// Still conflicted after LLM called task_complete — fail.
							log.Printf("[merge] task=%s merge still conflicted after task_complete", task.ID)
							task.ExecutionState = "failed"
							task.Status = "review"
							if te.sandboxMgr != nil {
								_ = te.sandboxMgr.Stop(task.ID)
							}
						} else {
							task.ExecutionState = "completed"
							task.Status = "done"
							if te.sandboxMgr != nil {
								_ = te.sandboxMgr.Stop(task.ID)
							}
						}
					} else {
						task.ExecutionState = "awaiting_review"
						task.Status = "review"
					}
					te.store.UpdateTask(task)
					te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
						"state":                task.ExecutionState,
						"assistant_message_id": assistantMsgID,
						"prompt_tokens":        promptTokens, "predicted_tokens": predictedTokens,
						"cache_tokens": cacheTokens,
						"prompt_ms":    promptMs, "predicted_ms": predictedMs,
						"model_name": activeModel, "duration": time.Since(lc.startTime).Milliseconds(),
					}})
					te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
					// Post-tool git check: broadcast diff if a git command changed the state
					if hadGitCmd {
						te.taskMu.Lock()
						execState := te.tasks[task.ID]
						te.taskMu.Unlock()
						if execState != nil {
							te.checkAndBroadcastDiff(projectID, task.ID, task, execState, "")
						}
					}
					// If this is a subtask, auto-start the next sibling
					if task.ParentTaskID != "" {
						_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
					}
					return
				}
			}
			// Post-tool git check: broadcast diff if a git command changed the state
			if hadGitCmd {
				te.taskMu.Lock()
				execState := te.tasks[task.ID]
				te.taskMu.Unlock()
				if execState != nil {
					te.checkAndBroadcastDiff(projectID, task.ID, task, execState, "")
				}
			}

			// Check consecutive identical tool call failures (turn-level).
			if count := lc.tracker.recordTurn(turnFailures); count >= maxFailures {
				task.ExecutionState = "failed"
				task.Status = "review"
				task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
				te.store.UpdateTask(task)
				te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
				te.sendAndPersistError(task.ID, fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures), "max_failures")
				return
			}

			// Build assistant message with merged reasoning + content + tool calls.
			// Including tool_calls in the message history is critical for models like gemma
			// to understand what tool was called and match it to the result.
			assistantContentFull := assistantText
			if lc.reasoningContent.Len() > 0 {
				assistantContentFull = lc.reasoningContent.String() + "\n" + assistantText
			}
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
			lc.messages = append(lc.messages, types.ChatMessage{
				Role:      "assistant",
				Content:   assistantContentFull,
				ToolCalls: assistantToolCalls,
			})
			for _, tr := range toolResults {
				lc.messages = append(lc.messages, types.ChatMessage{Role: "tool", Content: tr.output, ToolCallID: tr.toolCallID})
			}

			// Loop detection: record this turn and check for repetition
			loopType, _, toolArgs := lc.detector.recordTurn(lc.pendingToolCalls, assistantText)
			if loopType != LoopNone {
				target := ""
				if loopType == LoopTool {
					target = extractPathFromArgs(toolArgs)
				}
				if handleLoopDetected(te, task, projectID, task.ID, lc.detector, loopType, target, &lc.messages) {
					return
				}
			}

			lc.pendingToolCalls = nil
			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()

			// Auto-commit file changes as a checkpoint.
			newMessages := lc.messages[lc.checkpointIdx:]
			te.createCheckpoint(ctx, projectID, task.ID, worktreePath, task.CurrentThreadID, assistantMsgID, newMessages, lc.sandboxEnabled)
			lc.checkpointIdx = len(lc.messages)

			// Continue to call LLM again with tool results — don't fall through to blocking select
			continue
		} else if assistantText != "" {
			// Text-only response resets the failure tracker
			lc.tracker.reset()

			// Merge reasoning into content so the model sees its own thinking
			assistantContentFull := assistantText
			if lc.reasoningContent.Len() > 0 {
				assistantContentFull = lc.reasoningContent.String() + "\n" + assistantText
			}
			lc.messages = append(lc.messages, types.ChatMessage{Role: "assistant", Content: assistantContentFull})
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages[:len(lc.messages)-1], assistantText, lc.reasoningContent.String(), lc.startTime)
			duration := time.Since(lc.startTime).Milliseconds()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: lc.reasoningContent.String(),
				ModelName: activeModel, ProviderName: lc.providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: duration, CreatedAt: time.Now().UTC(),
			})
			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()

			// Loop detection for text-only responses
			loopType, _, _ := lc.detector.recordTurn(nil, assistantText)
			// Also check for phrase repetition detected during streaming
			if loopType == LoopNone && lc.detector.phraseDetected() {
				loopType = LoopPhrase
			}
			if loopType != LoopNone {
				if handleLoopDetected(te, task, projectID, task.ID, lc.detector, loopType, "", &lc.messages) {
					return
				}
				task.ExecutionState = "running"
				te.store.UpdateTask(task)
				continue
			}

			// Check for queued user messages before sending complete.
			select {
			case um := <-userMsgChan:
				log.Printf("[user-msg] task=%s received message after response, continuing (regenerate=%v)", task.ID, um.Regenerate)
				lc.detector.reset() // user input breaks any loop pattern
				if um.Regenerate {
					lc.messages = truncateForRegenerate(lc.messages)
				}
				lc.messages = append(lc.messages, userMessageToChatMessage(um))
				if um.ModelName != "" {
					switched := te.applyModelSwitch(lc.mc, task, um.ServerURL, um.ModelName, true, false)
					lc.client = switched.client
					lc.providerName = switched.providerName
				}
				task.ExecutionState = "running"
				te.store.UpdateTask(task)
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
				continue
			default:
			}

			task.Status = "review"
			task.ExecutionState = "idle"
			te.store.UpdateTask(task)
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]any{
				"state": "idle", "summary": "Agent finished responding",
				"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
				"cache_tokens": cacheTokens,
				"prompt_ms":    promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": duration,
			}})
			// If this is a subtask, auto-start the next sibling
			if task.ParentTaskID != "" {
				_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
			}
		} else if lc.reasoningContent.Len() > 0 {
			// Reasoning-only response: no visible content, no tool calls.
			// Persist the reasoning, inject a hint, and retry.
			reasoningText := lc.reasoningContent.String()

			// Persist the reasoning-only assistant message
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(lc.usage, lc.messages, "", reasoningText, lc.startTime)
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
				Content: "", ReasoningContent: reasoningText,
				ModelName: activeModel, ProviderName: lc.providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: time.Since(lc.startTime).Milliseconds(), CreatedAt: time.Now().UTC(),
			})

			// Append reasoning to conversation history so the LLM sees its own thinking
			lc.messages = append(lc.messages, types.ChatMessage{Role: "assistant", Content: reasoningText})

			// Advance loop detector and check max hints (injects hint into messages + DB + WebSocket)
			lc.detector.recordTurn(nil, "")
			if handleLoopDetected(te, task, projectID, task.ID, lc.detector, LoopReasoningOnly, "", &lc.messages) {
				return
			}

			lc.assistantContent.Reset()
			lc.reasoningContent.Reset()
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Reasoning-only response, requesting action"}})
			continue
		}

		// Auto-summarize if context is approaching the window limit.
		if lc.contextWindow != nil {
			if shouldSummarize(lc.messages, *lc.contextWindow) {
				lc.messages = te.applySummarization(ctx, task, projectID, task.ID, lc.messages, serverURL, activeModel, nil)
				lc.detector.reset() // context changed, clear stale text window
			}
		}

		// Block for user message before next LLM call.
		select {
		case <-ctx.Done():
			te.handleStopped(task, projectID, task.ID, "Stopped by user", "stopped", true)
			return
		case resultCh := <-summarizeCh:
			// Manual summarization request from HandleSummarize
			log.Printf("[summarize] task=%s manual summarization requested", task.ID)
			lc.messages = te.applySummarization(ctx, task, projectID, task.ID, lc.messages, serverURL, activeModel, resultCh)
			lc.detector.reset() // context changed, clear stale text window
			continue
		case um := <-userMsgChan:
			log.Printf("[user-msg] task=%s received message while idle, continuing (regenerate=%v)", task.ID, um.Regenerate)
			lc.detector.reset()
			if um.Regenerate {
				lc.messages = truncateForRegenerate(lc.messages)
			}
			lc.messages = append(lc.messages, userMessageToChatMessage(um))
			if um.ModelName != "" && um.ServerURL != PiSentinelURL {
				switched := te.applyModelSwitch(lc.mc, task, um.ServerURL, um.ModelName, true, false)
				lc.client = switched.client
				lc.providerName = switched.providerName
			}
			task.ExecutionState = "running"
			te.store.UpdateTask(task)
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
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
func removeDuplicateUserMessage(messages []types.ChatMessage) []types.ChatMessage {
	for i := 1; i < len(messages); i++ {
		if messages[i].Role == "user" && messages[i-1].Role == "user" && messages[i].Content == messages[i-1].Content {
			// Keep the newer one (at index i), remove the older one (at index i-1)
			messages = append(messages[:i-1], messages[i:]...)
			break
		}
	}
	return messages
}

func (te *TaskExecutor) gitRun(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	_, err := c.CombinedOutput()
	return err
}

func computeStats(usage *types.ChatUsage, messages []types.ChatMessage, assistantText, reasoningContent string, startTime time.Time) (promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int) {
	if usage != nil {
		return usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, int(usage.PromptMs), int(usage.PredictedMs)
	}
	durationMs := int(time.Since(startTime).Milliseconds())
	promptTokens = estimatePromptTokens(messages)
	predictedTokens = estimateTotalTokens(assistantText, reasoningContent)
	cacheTokens = 0
	promptMs = durationMs * 7 / 10
	predictedMs = durationMs * 3 / 10
	return promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs
}
