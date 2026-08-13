package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"kanban/backend/llama"
	"kanban/backend/prompts"
	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/stream"
	"kanban/backend/types"

	sandbox "kanban/pkg/bridge"

	"github.com/google/uuid"
)

// handleStopped sets the task to stopped state, broadcasts the update, and optionally
// persists an error message. Used by all ctx.Done and cancellation paths.
func (te *TaskExecutor) handleStopped(task *types.Task, projectID, taskID string, output, code string, persistError bool) {
	task.ExecutionState = "idle"
	task.Status = "review"
	task.Output = output
	te.store.UpdateTask(task)
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "idle", "activity": "Task stopped by user"}})
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	if persistError {
		te.sendAndPersistError(taskID, output, code)
	}
}

// modelSwitchResult holds the outputs of applyModelSwitch.
type modelSwitchResult struct {
	client        *llama.LlamaClient
	providerName  string
	contextWindow *int
}

// applyModelSwitch creates a new LlamaClient for the given serverURL/modelName,
// updates the modelConfig, and optionally persists the override to the task DB row.
// If discoverContext is true, it attempts to discover and save the context window.
func (te *TaskExecutor) applyModelSwitch(mc *modelConfig, task *types.Task, serverURL, modelName string, persistToDB, discoverContext bool) modelSwitchResult {
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	providerName := te.configStore.GetProviderIDByURL(serverURL)

	var contextWindow *int
	if discoverContext {
		contextWindow = te.configStore.GetModelContextWindow(modelName, serverURL)
		if contextWindow == nil {
			if discovered := te.configStore.DiscoverContextWindow(modelName, serverURL); discovered != nil {
				contextWindow = discovered
				te.configStore.SaveModelContextWindow(modelName, serverURL, *discovered)
				log.Printf("[context] discovered context window for %s: %d", modelName, *discovered)
			}
		}
	}

	mc.set(serverURL, modelName)
	if persistToDB {
		task.LlamaServerURL = serverURL
		task.ModelName = modelName
		task.ReasoningEffort = reasoningEffort
		_ = te.store.UpdateTask(task)
	}

	return modelSwitchResult{
		client:        client,
		providerName:  providerName,
		contextWindow: contextWindow,
	}
}

// broadcastDiffIfGitCmd looks up the execution state and broadcasts a diff
// if the task has an active executor. Used after tool execution when a git
// command may have changed the working tree state.
func (te *TaskExecutor) broadcastDiffIfGitCmd(projectID, taskID string, task *types.Task) {
	te.taskMu.Lock()
	state := te.tasks[taskID]
	te.taskMu.Unlock()
	if state != nil {
		te.checkAndBroadcastDiff(projectID, taskID, task, state, "")
	}
}

// checkAndBroadcastDiff computes the task diff and broadcasts stats only if the hash
// differs from the last broadcast. This avoids duplicate broadcasts when fsnotify
// already caught the same change. If forkBranch is provided, it resolves the thread
// to get the fork branch for diff baseline.
func (te *TaskExecutor) checkAndBroadcastDiff(projectID, taskID string, task *types.Task, state *executionState, forkBranch string) {
	// Use BaseCommit (fixed hash) when available, matches API handler
	baseline := task.BaseRef
	if task.BaseCommit != "" {
		baseline = task.BaseCommit
	}
	baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, baseline, task.Branch, forkBranch)
	diffResult, err := te.worktreeMgr.GetTaskDiff(taskID, baseRef, "")
	if err != nil || (diffResult.Diff == "" && len(diffResult.Untracked) == 0) {
		return
	}
	hash := stream.ComputeDiffHash(diffResult.Diff, diffResult.CommittedDiff, diffResult.Untracked, diffResult.StagedHash)
	// Skip if hash unchanged — fsnotify already broadcast this state
	if hash == state.lastDiffHash {
		return
	}
	state.lastDiffHash = hash
	fileHashes := diffResult.GetPerFileHashes()
	stats := stream.ComputeDiffStats(diffResult, *task)
	filesN := stats.VisibleTracked + stats.VisibleUntracked
	// Check if task branch commits are already merged into the target branch.
	if targetBranch, ok := stream.CheckAlreadyMerged(*task); ok {
		if project, err := te.store.GetProject(projectID); err == nil && project.Path != "" {
			stats.AlreadyMerged = te.worktreeMgr.IsMergedInto(project.Path, task.Branch, targetBranch)
		}
	}
	te.appStreamMgr.BroadcastDiffStats(stream.DiffStatsEvent{
		ProjectID:           projectID,
		TaskID:              taskID,
		DiffHash:            hash,
		ShortStat:           diffResult.ShortStat,
		FilesN:              filesN,
		UntrackedN:          stats.VisibleUntracked,
		UntrackedLines:      stats.UntrackedLines,
		TrackedAdd:          stats.TrackedAdd,
		TrackedDel:          stats.TrackedDel,
		ChangedHashes:       fileHashes,
		StagedPaths:         diffResult.StagedPaths,
		HasCommittedChanges: diffResult.CommittedDiff != "",
		AlreadyMerged:       stats.AlreadyMerged,
	})
}

// extractPrevSummary returns the content of the most recent summarization message.
func extractPrevSummary(messages []types.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			return messages[i].Content
		}
	}
	return ""
}

// applySummarization runs summarization, appends the summary message, and broadcasts the event.
// Summary is injected into the system prompt on the next LLM call.
func (te *TaskExecutor) applySummarization(ctx context.Context, task *types.Task, projectID, taskID string, messages []types.ChatMessage, serverURL, modelName string, resultCh chan error) []types.ChatMessage {
	prevSummary := extractPrevSummary(messages)
	// Route summarization through the configured Utility provider/model.
	// Only override the active model when a Utility pair is configured; otherwise
	// keep the task's active model/URL passed in by the caller.
	utilURL, utilModel, ok := te.utilityTarget(task)
	if ok {
		serverURL = utilURL
		modelName = utilModel
	}
	sumResult, err := te.summarizeContext(ctx, messages, serverURL, modelName, prevSummary)
	if err != nil {
		log.Printf("[summarize] task=%s summarization failed: %v", taskID, err)
		if resultCh != nil {
			resultCh <- err
		}
		return messages
	}

	// Persist to DB.
	summaryMsgID := uuid.New().String()
	summaryCreatedAt := time.Now().UTC()
	// Anchor: reload task to get UpdatedAt (set when Summarizing flag was set).
	if updated, err := te.store.GetTaskByTaskID(taskID); err == nil && updated != nil {
		task = updated
	}
	summaryAnchor := task.UpdatedAt
	_ = te.store.CreateTaskMessage(&types.TaskMessage{
		ID:              summaryMsgID,
		TaskID:          taskID,
		ThreadID:        task.CurrentThreadID,
		Role:            "summarization",
		Content:         sumResult.summary,
		ModelName:       modelName,
		PromptTokens:    sumResult.promptTokens,
		PredictedTokens: sumResult.predictedTokens,
		PromptMs:        int(sumResult.promptMs),
		PredictedMs:     int(sumResult.predictedMs),
		CreatedAt:       summaryCreatedAt,
	})

	// Fix timestamps of messages sent during the summarization window.
	_ = te.store.FixPostSummaryTimestamps(taskID, task.CurrentThreadID, summaryAnchor, summaryCreatedAt)

	// Append to in-memory messages.
	messages = append(messages, types.ChatMessage{
		Role:    "summarization",
		Content: sumResult.summary,
	})

	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: "summarization", Data: map[string]any{
		"id":               summaryMsgID,
		"summary":          sumResult.summary,
		"created_at":       summaryCreatedAt.Format(time.RFC3339),
		"prompt_tokens":    sumResult.promptTokens,
		"predicted_tokens": sumResult.predictedTokens,
		"prompt_ms":        sumResult.promptMs,
		"predicted_ms":     sumResult.predictedMs,
		"model_name":       modelName,
	}})
	log.Printf("[summarize] task=%s summarized context (messages preserved, LLM context compressed)", taskID)

	if resultCh != nil {
		resultCh <- nil
	}
	return messages
}

// userMessageToChatMessage converts a UserMessage (with optional image attachments)
// into a ChatMessage. If attachments are present, builds multi-modal ContentParts.
func userMessageToChatMessage(um stream.UserMessage) types.ChatMessage {
	if um.Attachments == "" {
		return types.ChatMessage{Role: "user", Content: um.Content}
	}

	var atts []struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal([]byte(um.Attachments), &atts); err != nil {
		return types.ChatMessage{Role: "user", Content: um.Content}
	}

	parts := []types.ContentPart{{Type: "text", Text: um.Content}}
	for _, att := range atts {
		if att.Data == "" {
			continue
		}
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
	return types.ChatMessage{Role: "user", ContentParts: parts}
}

// handleLoopDetected processes a detected loop: increments the consecutive loop counter,
// checks if max consecutive loops are reached (returns true if task should terminate),
// injects a hint message, and notifies the frontend.
// If target is non-empty, it is included in the hint (used for tool loops).
// If exploreMode is true, loop hints are suppressed (no hint injection, no streak tracking).
// Plan mode also suppresses hints — reading files and making statements is normal planning behavior.
// On valid turn (via recordTurn), loopStreak is reset to 0.
func handleLoopDetected(te *TaskExecutor, task *types.Task, projectID, taskID string, detector *loopDetector, loopType LoopType, target string, messages *[]types.ChatMessage, exploreMode bool) bool {
	// In exploration/plan mode, suppress all loop hints — let the LLM freely explore / plan
	if exploreMode || task.IsPlanMode() {
		detector.reset()
		return false // continue without intervention
	}

	detector.loopStreak++
	if detector.loopStreak >= maxLoopHints {
		task.ExecutionState = "failed"
		task.Status = "review"
		task.Output = "Model stuck in loop (max hints exhausted)"
		te.store.UpdateTask(task)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		te.sendAndPersistError(taskID, "Model stuck in loop (max hints exhausted)", "loop_detected")
		return true // terminate
	}

	hint := buildHint(loopType, target)
	log.Printf("[loop] task=%s detected loop type=%d (streak %d/%d), target=%s", taskID, loopType, detector.loopStreak, maxLoopHints, target)
	hintIdx := len(*messages)
	*messages = append(*messages, types.ChatMessage{Role: "user", Content: hint})
	detector.recordHintIndex(hintIdx)

	hintMsgID := uuid.New().String()
	te.store.CreateTaskMessage(&types.TaskMessage{
		ID: hintMsgID, TaskID: taskID, ThreadID: task.CurrentThreadID, Role: "user",
		Content: hint, IsLoopHint: true, CreatedAt: time.Now().UTC(),
	})
	// Preserve loopStreak across hint cycles so consecutive loops accumulate.
	// A valid turn (via recordTurn) resets it to 0.
	streak := detector.loopStreak
	detector.reset()
	detector.loopStreak = streak
	// Broadcast hint with its DB message ID so the client can dedupe on history reload.
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeHint, Data: map[string]any{
		"id":         hintMsgID,
		"content":    hint,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}})
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Loop detected, steering agent"}})
	return false // continue
}

// setupSandbox creates or reuses a sandbox container and connects to the agent.
// Returns the image used, the agent client, and any assigned ports.
// The caller is responsible for creating callbacks from the agent client.
func (te *TaskExecutor) setupSandbox(setup *taskExecSetup) (string, *sandbox.Client, []types.AssignedPort, error) {
	task := setup.task
	projectID := setup.projectID
	projectName := setup.project.Name
	worktreePath := setup.worktreePath
	projectPath := setup.projectPath
	sandboxOpts := setup.sandboxOpts
	sandboxDockerfilePath := setup.sandboxDockerfilePath

	if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
		return "", nil, nil, fmt.Errorf("gVisor not available")
	}

	imageToUse := ""
	// Determine image to use: Dockerfile build takes precedence over pre-built image
	if sandboxDockerfilePath != "" {
		imageToUse = kanban_sandbox.ProjectImageName(projectName)
		if !te.sandboxMgr.ImageReadyForProject(projectName) {
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "building_image", "activity": "Building sandbox image from Dockerfile"}})
			if err := te.sandboxMgr.BuildImageWithProgress(projectName, sandboxDockerfilePath, func(line string) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
			}); err != nil {
				return "", nil, nil, fmt.Errorf("failed to build sandbox image: %w", err)
			}
		}
	} else {
		imageToUse = sandboxOpts.BaseImage
		if imageToUse == "" {
			imageToUse = kanban_sandbox.BaseImage
		}
		if !te.sandboxMgr.ImageReady(imageToUse) {
			te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "pulling_image", "activity": "Pulling " + imageToUse}})
			if err := te.sandboxMgr.PullImageWithProgress(imageToUse, func(line string) {
				te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeBuildProgress, Data: map[string]string{"line": line}})
			}); err != nil {
				return "", nil, nil, fmt.Errorf("failed to pull sandbox image: %w", err)
			}
		}
	}

	// Reuse existing container (lazy-start). Create only if none exists or if stopped/crashed.
	var assignedPorts []types.AssignedPort
	if !te.sandboxMgr.ContainerIsRunning(task.ID) {
		agentSandbox := te.agentRegistry.GetAnyAgentSandbox()
		var createErr error
		assignedPorts, createErr = te.sandboxMgr.Create(task.ID, worktreePath, projectName, projectPath, kanban_sandbox.SandboxOpts{
			BaseImage:        imageToUse,
			NetworkIsolation: sandboxOpts.NetworkIsolation,
			MemoryLimit:      sandboxOpts.MemoryLimit,
			ForwardPorts:     sandboxOpts.ForwardPorts,
			AgentSandbox:     agentSandbox,
			SkillsDir:        prompts.SkillsDir(),
			SandboxMounts:    sandboxOpts.SandboxMounts,
			SandboxEnvs:      sandboxOpts.SandboxEnvs,
			Capabilities:     sandboxOpts.Capabilities,
			PlanMode:         task.IsPlanMode(),
		})
		if createErr != nil {
			return "", nil, nil, fmt.Errorf("failed to create sandbox: %w", createErr)
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
		return "", nil, nil, fmt.Errorf("failed to connect to sandbox bridge: %w", err)
	}

	return imageToUse, agentClient, assignedPorts, nil
}
