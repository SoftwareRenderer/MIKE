package exec

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/prompts"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
	"kanban/pkg/logging"
)

// summarizeOrchestrator owns a task's context window: deciding when the conversation is
// approaching the model's limit, producing the rolling summary, installing it in the system
// prompt, and the two paths a manual summarize takes (the running loop's channel, and the DB
// read for a task with no executor).
//
// It takes neither taskMu nor sandboxMu. The one piece of executionState it touches is
// reached through runningState, whose body takes taskMu here on the TaskExecutor side; across
// that seam it may read only the summarize group (summarizeCh, activeSummarize) — the fields
// whose values exist only to serve a summarize request — and never the taskMu- or
// sandboxMu-guarded ones.
type summarizeOrchestrator struct {
	store        *store.Store
	configStore  *config.ConfigStore
	appStreamMgr *stream.AppStreamManager
	worktreeMgr  *git.WorktreeManager
	// runningState finds the executionState of a running task (nil when none). See the type
	// comment for what may be read through it.
	runningState func(taskID string) *executionState
	// lazyStart starts a task that has no executor, for the blocked message a summarization
	// left behind. Injected rather than copied: starting a task belongs to the executor.
	lazyStart func(projectID, taskID, content, modelName, serverURL, reasoningEffort string, regenerate bool)
	// utilityTarget and chatCompletion are TaskExecutor's: which provider answers a summary is
	// the same routing decision a chat turn makes, and the request goes through the executor's
	// concurrency gate. Injected rather than rebuilt, so the two cannot disagree.
	utilityTarget  func(task *types.Task) (serverURL, model string, ok bool)
	chatCompletion func(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage, opts *ChatCompletionOpts) (*types.ChatUsage, string, error)
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

// newSummarizeOrchestrator wires the collaborator from the executor's own fields, so a
// TaskExecutor assembled as a struct literal reaches the same one.
func (te *TaskExecutor) newSummarizeOrchestrator() *summarizeOrchestrator {
	s := &summarizeOrchestrator{
		store:          te.store,
		configStore:    te.configStore,
		appStreamMgr:   te.appStreamMgr,
		worktreeMgr:    te.worktreeMgr,
		runningState:   te.runningState,
		lazyStart:      te.handleLazyStart,
		utilityTarget:  te.utilityTarget,
		chatCompletion: te.ChatCompletion,
	}
	s.dbSummarizeCancel.tasks = make(map[string]context.CancelFunc)
	s.blockedMessages.tasks = make(map[string]struct{})
	return s
}

// summarizeRequest is one manual summarization ask, sent to the running loop over its
// channel and answered on resultCh rather than by a return value.
type summarizeRequest struct {
	ctx      context.Context // cancelled when user clicks ✕ or new request arrives
	resultCh chan error      // receives ok/err from the loop
	cancel   context.CancelFunc
}

// summarizeResult holds the result of a context summarization operation.
type summarizeResult struct {
	summary         string
	oldLen          int
	promptTokens    int
	predictedTokens int
	promptMs        int64
	predictedMs     int64
}

// defaultSummarizeThreshold is the auto-summarization threshold used when no config value
// is set. It is config's default, not a copy: the Settings UI shows that number, so a turn
// must summarize at the percentage it advertises.
const defaultSummarizeThreshold = config.DefaultAutoSummarizeThreshold

// shouldSummarize checks if the LLM context usage is approaching the window limit.
// thresholdPct is the percent of the context window at which auto-summarization
// triggers (1-100).
func shouldSummarize(usedTokens, contextWindow, thresholdPct int) bool {
	if contextWindow <= 0 {
		return false
	}
	if thresholdPct <= 0 || thresholdPct > 100 {
		thresholdPct = defaultSummarizeThreshold
	}
	return usedTokens >= contextWindow*thresholdPct/100
}

// contextTokensUsed returns the real context token usage from the most recent LLM
// call (prompt + completion tokens, matching what the model will consume next).
// This is the same value the frontend displays as the context-usage percentage.
// Falls back to a heuristic estimate when the LLM didn't report usage.
func contextTokensUsed(lc *loopContext) int {
	if lc != nil && lc.usage != nil && lc.usage.PromptTokens > 0 {
		return lc.usage.PromptTokens + lc.usage.CompletionTokens
	}
	return estimatePromptTokens(buildLLMContext(lc.messages))
}

// summarizeContext produces a rolling summary of the conversation history.
// Sends a non-streaming chat completion. Does not modify the messages slice.
func (s *summarizeOrchestrator) summarizeContext(ctx context.Context, messages []types.ChatMessage, serverURL, modelName, previousSummary string) (summarizeResult, error) {
	oldLen := len(messages)

	chatHistory := formatChatHistory(buildLLMContext(messages), previousSummary != "")
	systemPrompt, userPrompt, err := prompts.RenderSummarizePrompt(prompts.SummarizeData{
		ChatHistory:     chatHistory,
		PreviousSummary: previousSummary,
	})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("load summarize prompt: %w", err)
	}
	// Conversation data (previous summary + chat history) goes in the user
	// message; some chat templates reject system-only requests.
	summarizeMessages := []types.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Non-streaming, with reasoning enabled.
	usage, summary, err := s.chatCompletion(ctx, serverURL, modelName, summarizeMessages, &ChatCompletionOpts{ReasoningEffort: ""})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("summarization call failed: %w", err)
	}
	// An empty summary must be rejected: buildLLMContext drops all messages
	// before the summarization entry, so installing one erases the conversation.
	if strings.TrimSpace(summary) == "" {
		return summarizeResult{}, fmt.Errorf("summarization produced an empty summary")
	}

	res := summarizeResult{
		summary: summary,
		oldLen:  oldLen,
	}
	if usage != nil {
		res.promptTokens = usage.PromptTokens
		res.predictedTokens = usage.CompletionTokens
		res.promptMs = usage.PromptMs
		res.predictedMs = usage.PredictedMs
	}
	return res, nil
}

// buildLLMContext filters out summarization messages (summary is in system prompt).
// When a summarization message exists, all messages between the system prompt and
// the last summarization are dropped — their content is captured by the summary
// in the system prompt. The system message itself is always preserved.
func buildLLMContext(messages []types.ChatMessage) []types.ChatMessage {
	// Find the last summarization message — everything between system and it is compressed.
	lastSummaryIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			lastSummaryIdx = i
			break
		}
	}

	// Always keep the system message (index 0, contains summary in system prompt).
	// When a summary exists, skip all messages between system and post-summary.
	var result []types.ChatMessage
	if len(messages) > 0 {
		result = append(result, messages[0]) // system prompt
	}
	startIdx := 1 // skip system
	if lastSummaryIdx >= 0 {
		startIdx = lastSummaryIdx + 1 // skip pre-summary + summarization itself
	}
	for i := startIdx; i < len(messages); i++ {
		m := messages[i]
		result = append(result, m)
	}
	return result
}

// formatChatHistory formats conversation as plain text for the summarization prompt.
// System and tool messages are skipped. Summarization messages are skipped when
// skipSummaryMsg is true (content passed as PreviousSummary instead).
func formatChatHistory(messages []types.ChatMessage, skipSummaryMsg bool) string {
	// Map tool results for status lookup.
	toolResults := make(map[string]string)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

	// Pre-allocate builder capacity
	var totalSize int
	for _, m := range messages {
		totalSize += len(m.Content) + len(m.ReasoningContent)
	}
	sb := strings.Builder{}
	sb.Grow(totalSize + len(messages)*50)
	for _, m := range messages {
		switch m.Role {
		case "system":
			continue
		case "summarization":
			if skipSummaryMsg {
				continue // passed as PreviousSummary
			}
			sb.WriteString("Assistant: [Context summary: ")
			sb.WriteString(m.Content)
			sb.WriteString("]\n")
			continue
		case "tool":
			// Already incorporated into assistant line above
			continue
		case "advisory":
			// LSP notes are noise for the summary
			continue
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		case "assistant":
			if len(m.ToolCalls) > 0 {
				// Format each tool call as a descriptive line
				for _, tc := range m.ToolCalls {
					action := formatToolAction(tc.Function.Name, tc.Function.Arguments)
					status := formatToolStatus(tc.ID, toolResults)
					result := formatToolResult(tc.Function.Name, tc.ID, status, toolResults)
					sb.WriteString(fmt.Sprintf("Assistant: (I used the %s tool to %s %s)\n",
						tc.Function.Name, action, result))
				}
			} else if m.Content != "" {
				sb.WriteString("Assistant: ")
				sb.WriteString(m.Content)
				sb.WriteString("\n")
			}
		}
	}

	return sb.String()
}

var wordRe = regexp.MustCompile(`\S+`)

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

// extractPrevSummary returns the content of the most recent summarization message.
func extractPrevSummary(messages []types.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			return messages[i].Content
		}
	}
	return ""
}

// setSystemPrompt installs prompt as the system message and returns the conversation.
// Index 0 is the system message wherever one is built, so the normal case is an in-place
// replace. Prepending is the fallback: keeping the summary outweighs the index shift, and
// only growing the slice means no index-based reader (lc.checkpointIdx) runs past the end.
func setSystemPrompt(messages []types.ChatMessage, prompt string) []types.ChatMessage {
	if len(messages) > 0 && messages[0].Role == "system" {
		messages[0].Content = prompt
		return messages
	}
	logging.For(logging.Summarize).Infof("warning: conversation has no system message at index 0; prepending one to carry the summary")
	return append([]types.ChatMessage{{Role: "system", Content: prompt}}, messages...)
}

// injectProceedMessage persists and broadcasts a "Proceed" user message
func (s *summarizeOrchestrator) injectProceedMessage(setup *taskExecSetup, lc *loopContext) {
	msgID := uuid.New().String()
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
		ID: msgID, TaskID: setup.task.ID, ThreadID: setup.task.CurrentThreadID,
		Role: "user", Content: "Proceed", CreatedAt: time.Now().UTC(),
	})
	lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: "Proceed"})
	s.appStreamMgr.SendTaskEvent(setup.projectID, setup.task.ID, types.StreamEvent{
		Type: types.WSTypeUserMessage,
		Data: map[string]any{"id": msgID, "role": "user", "content": "Proceed"},
	})
}

// applySummarization summarizes, persists the summary, installs it in the system prompt and
// broadcasts the event. It returns the conversation for the next call and the installed
// summary, which is "" if summarization failed and the messages came back untouched.
//
// The summary row is persisted before anything is published, so a restart at any point resumes
// with it. The prompt is rewritten in place rather than rebuilt from the DB, because the
// in-memory conversation is a superset of the persisted one.
func (s *summarizeOrchestrator) applySummarization(setup *taskExecSetup, ctx context.Context, messages []types.ChatMessage, serverURL, modelName string, resultCh chan error) ([]types.ChatMessage, string) {
	task := setup.task
	taskID := setup.task.ID
	projectID := setup.projectID

	// Signal the UI that summarization is in progress
	origTask := task
	task.Summarizing = true
	_ = s.store.SetTaskSummarizing(taskID, true)
	s.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	clearFlag := func() {
		// Clear on the caller's reference too (task may have been reloaded below).
		origTask.Summarizing = false
		task.Summarizing = false
		_ = s.store.SetTaskSummarizing(taskID, false)
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	}

	prevSummary := extractPrevSummary(messages)
	// Route summarization through the configured Utility provider/model
	utilURL, utilModel, ok := s.utilityTarget(task)
	if ok {
		serverURL = utilURL
		modelName = utilModel
	}
	sumResult, err := s.summarizeContext(ctx, messages, serverURL, modelName, prevSummary)
	if err != nil {
		logging.For(logging.Summarize).With(logging.TaskID, taskID).Warnf("summarization failed: %v", err)
		clearFlag()
		if resultCh != nil {
			resultCh <- err
		}
		return messages, ""
	}

	// Persist to DB.
	summaryMsgID := uuid.New().String()
	summaryCreatedAt := time.Now().UTC()
	// Anchor: reload task to get UpdatedAt (set when Summarizing flag was set).
	if updated, err := s.store.GetTaskByID(taskID); err == nil && updated != nil {
		task = updated
	}
	summaryAnchor := task.UpdatedAt
	_ = s.store.CreateTaskMessage(&types.TaskMessage{
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
	_ = s.store.FixPostSummaryTimestamps(taskID, task.CurrentThreadID, summaryAnchor, summaryCreatedAt)

	// Append to in-memory messages.
	messages = append(messages, types.ChatMessage{
		Role:    "summarization",
		Content: sumResult.summary,
	})

	// Install the summary into the live system prompt (see the doc comment): the
	// summary reaches the LLM through messages[0] only, never through the marker.
	// `task` is the row reloaded above, so the mode is current.
	messages = setSystemPrompt(messages, composeSystemPrompt(task, setup.worktreePath, setup.projectName(), setup.sandboxEnabled, sumResult.summary))

	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: "summarization", Data: map[string]any{
		"id":               summaryMsgID,
		"summary":          sumResult.summary,
		"created_at":       summaryCreatedAt.Format(time.RFC3339),
		"prompt_tokens":    sumResult.promptTokens,
		"predicted_tokens": sumResult.predictedTokens,
		"prompt_ms":        sumResult.promptMs,
		"predicted_ms":     sumResult.predictedMs,
		"model_name":       modelName,
	}})
	logging.For(logging.Summarize).With(logging.TaskID, taskID).Infof("summarized context (messages preserved, LLM context compressed)")

	clearFlag()

	if resultCh != nil {
		resultCh <- nil
	}
	return messages, sumResult.summary
}

// maybeAutoSummarize summarizes when context usage approaches the window limit. It runs
// right after an assistant message is appended, so it fires as the threshold is crossed
// instead of waiting for the task to go idle.
// If injectProceed is set and summarization succeeds, a persisted "Proceed" user message is
// appended for the next call.
func (s *summarizeOrchestrator) maybeAutoSummarize(setup *taskExecSetup, lc *loopContext, activeModel string, injectProceed bool) {
	if lc.contextWindow == nil {
		return
	}
	threshold := s.configStore.AutoSummarizeThreshold()
	if !shouldSummarize(contextTokensUsed(lc), *lc.contextWindow, threshold) {
		return
	}
	// Fallback (non-utility) summarization requests go to the task's CURRENT
	// provider — lc.mc follows mid-run switches, setup.serverURL does not.
	curURL, _ := lc.mc.get()
	var summary string
	lc.messages, summary = s.applySummarization(setup, setup.ctx, lc.messages, curURL, activeModel, nil)
	if summary != "" && injectProceed {
		s.injectProceedMessage(setup, lc)
	}
	lc.detector.reset()
	lc.detector.clearHints()
}

// cancelSummarize cancels an in-progress summarization for a task.
func (s *summarizeOrchestrator) cancelSummarize(taskID, projectID string) {
	// Cancel DB-based background summarization.
	s.dbSummarizeCancel.mu.Lock()
	if cancel, ok := s.dbSummarizeCancel.tasks[taskID]; ok {
		cancel()
		delete(s.dbSummarizeCancel.tasks, taskID)
	}
	s.dbSummarizeCancel.mu.Unlock()

	// Cancel channel-based summarization (executor running).
	state := s.runningState(taskID)
	if state != nil {
		state.activeSummarize.mu.Lock()
		if req := state.activeSummarize.request; req != nil {
			req.cancel()
			state.activeSummarize.request = nil
		}
		state.activeSummarize.mu.Unlock()
	}
}

// HandleSummarize triggers context summarization (live or from DB).
func (s *summarizeOrchestrator) HandleSummarize(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		httputil.MethodNotAllowed(w)
		return
	}

	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	if task.LlamaServerURL == "" || task.ModelName == "" {
		httputil.JSONError(w, http.StatusBadRequest, "task has no LLM configured")
		return
	}

	// If already summarizing, cancel it (acts as the cancel endpoint).
	if task.Summarizing {
		logging.For(logging.Summarize).With(logging.TaskID, taskID).Infof("cancelling in-progress summarization")
		s.cancelSummarize(taskID, projectID)
		task.Summarizing = false
		_ = s.store.SetTaskSummarizing(taskID, false)
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "cancelled"})
		return
	}

	// Set summarizing flag so the UI survives refresh and ForwardUserMessage blocks lazy-start.
	task.Summarizing = true
	_ = s.store.SetTaskSummarizing(taskID, true)
	s.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	// If task is actively running, send request to the execution loop
	state := s.runningState(taskID)

	if state != nil {
		defer func() {
			task.Summarizing = false
			_ = s.store.SetTaskSummarizing(taskID, false)
			// Broadcast the freshest row
			if fresh, err := s.store.GetTask(projectID, taskID); err == nil && fresh != nil {
				fresh.Summarizing = false
				task = fresh
			}
			s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}()
		// Create a cancellable context for this summarize request.
		// Uses background context so summarization survives browser refresh.
		// Cancelled when: new summarize request arrives, or timeout.
		reqCtx, reqCancel := context.WithTimeout(context.Background(), 120*time.Second)
		resultCh := make(chan error, 1)
		req := &summarizeRequest{ctx: reqCtx, resultCh: resultCh, cancel: reqCancel}

		// Cancel any previous active summarize request (user clicked ✕ or re-clicked summarize).
		state.activeSummarize.mu.Lock()
		if old := state.activeSummarize.request; old != nil {
			old.cancel()
		}
		state.activeSummarize.request = req
		state.activeSummarize.mu.Unlock()

		select {
		case state.summarizeCh <- req:
			// Request sent, wait for result (respects timeout via reqCtx).
			select {
			case err := <-resultCh:
				if err != nil {
					httputil.JSONError(w, http.StatusInternalServerError, err.Error())
					return
				}
			case <-reqCtx.Done():
				if reqCtx.Err() == context.DeadlineExceeded {
					httputil.JSONError(w, http.StatusInternalServerError, "summarization timed out")
					return
				}
				// Cancelled by new request.
				http.Error(w, "summarization cancelled", http.StatusRequestTimeout)
				return
			}
			// Clean up blocked messages tracking.
			s.blockedMessages.mu.Lock()
			delete(s.blockedMessages.tasks, taskID)
			s.blockedMessages.mu.Unlock()
			// Result is sent via WebSocket, return minimal response
			httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "summarizing"})
			return
		default:
			// Channel full, fall through to DB-based approach
			reqCancel()
			state.activeSummarize.mu.Lock()
			if state.activeSummarize.request == req {
				state.activeSummarize.request = nil
			}
			state.activeSummarize.mu.Unlock()
		}
	}

	// Task is idle/stopped — summarize from DB in a background goroutine.
	// Uses background context so summarization survives browser refresh.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	s.dbSummarizeCancel.mu.Lock()
	s.dbSummarizeCancel.tasks[taskID] = cancel
	s.dbSummarizeCancel.mu.Unlock()

	go func() {
		defer func() {
			s.dbSummarizeCancel.mu.Lock()
			delete(s.dbSummarizeCancel.tasks, taskID)
			s.dbSummarizeCancel.mu.Unlock()
		}()

		// Clear the summarizing flag and broadcast the task
		clearFlag := func() {
			task.Summarizing = false
			_ = s.store.SetTaskSummarizing(taskID, false)
			// Broadcast the freshest row
			if fresh, err := s.store.GetTask(projectID, taskID); err == nil && fresh != nil {
				fresh.Summarizing = false
				task = fresh
			}
			s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}

		taskMsgs, _ := s.store.GetTaskMessages(task.ID, task.CurrentThreadID)
		project, _ := s.store.GetProject(projectID)
		projectName := ""
		if project != nil {
			projectName = project.Name
		}
		worktreePath, _ := s.worktreeMgr.WorktreePath(taskID)

		messages := buildMessagesFromDB(task, taskMsgs, worktreePath, projectName, false)
		previousSummary := extractPrevSummary(messages)

		// Route summarization through the configured Utility provider/model.
		serverURL, modelName, _ := s.utilityTarget(task)
		sumResult, err := s.summarizeContext(ctx, messages, serverURL, modelName, previousSummary)
		if err != nil {
			logging.For(logging.Summarize).With(logging.TaskID, taskID).Warnf("manual summarization failed: %v", err)
			clearFlag()
			return
		}

		// Persist summary with stats
		summaryMsgID := uuid.New().String()
		summaryCreatedAt := time.Now().UTC()
		// Anchor: latest message before summarization (all taskMsgs are pre-summarization).
		var summaryAnchor time.Time
		for _, m := range taskMsgs {
			if m.CreatedAt.After(summaryAnchor) {
				summaryAnchor = m.CreatedAt
			}
		}
		_ = s.store.CreateTaskMessage(&types.TaskMessage{
			ID:              summaryMsgID,
			TaskID:          task.ID,
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

		// Notify frontend
		s.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: "summarization", Data: map[string]any{
			"id":               summaryMsgID,
			"summary":          sumResult.summary,
			"created_at":       time.Now().UTC().Format(time.RFC3339),
			"prompt_tokens":    sumResult.promptTokens,
			"predicted_tokens": sumResult.predictedTokens,
			"prompt_ms":        sumResult.promptMs,
			"predicted_ms":     sumResult.predictedMs,
			"model_name":       task.ModelName,
		}})

		logging.For(logging.Summarize).With(logging.TaskID, taskID).Infof("manual summarization complete (messages preserved, LLM context compressed)")

		// Fix timestamps of messages sent during the summarization window
		// so they sort after the summary in the database.
		_ = s.store.FixPostSummaryTimestamps(taskID, task.CurrentThreadID, summaryAnchor, summaryCreatedAt)

		clearFlag()

		// Only start executor if there are blocked messages for this task.
		s.blockedMessages.mu.Lock()
		_, hasBlocked := s.blockedMessages.tasks[taskID]
		delete(s.blockedMessages.tasks, taskID)
		s.blockedMessages.mu.Unlock()
		if hasBlocked {
			s.lazyStart(projectID, taskID, "", "", "", "", false)
		}
	}()

	// Return immediately — summarization runs in background.
	httputil.JSONResponse(w, http.StatusOK, map[string]any{
		"status": "summarizing",
	})
}

// HandleSummarize triggers context summarization (live or from DB). The route names this
// method; the behaviour lives in the summarizer.
func (te *TaskExecutor) HandleSummarize(w http.ResponseWriter, r *http.Request) {
	te.summarize.HandleSummarize(w, r)
}
