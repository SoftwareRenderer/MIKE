package exec

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/prompts"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"

	"github.com/google/uuid"
	"mike/pkg/logging"
)

// summarizeOrchestrator owns a task's context window: deciding when the conversation approaches
// the model's limit, producing the rolling summary, installing it in the system prompt, and the
// two paths a manual summarize takes (the running loop's channel, or a DB read when no executor
// holds the task).
//
// It takes neither taskMu nor sandboxMu. executionState is reached only through runningState,
// whose body takes taskMu on the TaskExecutor side; across that seam only the summarize group
// (summarizeCh, activeSummarize) may be read, never the taskMu- or sandboxMu-guarded fields.
type summarizeOrchestrator struct {
	store        *store.Store
	configStore  *config.ConfigStore
	appStreamMgr *stream.AppStreamManager
	worktreeMgr  *git.WorktreeManager
	// executionState of a running task, nil when none. See the type comment for what may be read.
	runningState func(taskID string) *executionState
	// starts a task that has no executor, for the blocked message a summarization left behind.
	lazyStart func(projectID, taskID, content, modelName, serverURL, reasoningEffort string, regenerate bool)
	// sends the "Proceed" instruction. See TaskExecutor.sendProceed.
	sendProceed func(projectID, taskID, threadID string) error
	// So a summary and a chat turn cannot disagree about which provider answers.
	utilityTarget     func(task *types.Task) (serverURL, model string, ok bool)
	chatCompletion    func(ctx context.Context, serverURL, modelName string, messages []types.ChatMessage, opts *ChatCompletionOpts) (*types.ChatUsage, string, error)
	dbSummarizeCancel struct {
		mu    sync.Mutex
		tasks map[string]context.CancelFunc
	}
	blockedMessages struct {
		mu    sync.Mutex
		tasks map[string]struct{}
	}
}

// One wiring definition, so every executor's summarize collaborator is wired the same way.
func (te *TaskExecutor) newSummarizeOrchestrator() *summarizeOrchestrator {
	s := &summarizeOrchestrator{
		store:          te.store,
		configStore:    te.configStore,
		appStreamMgr:   te.appStreamMgr,
		worktreeMgr:    te.worktreeMgr,
		runningState:   te.runningState,
		lazyStart:      te.handleLazyStart,
		sendProceed:    te.sendProceed,
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

type summarizeResult struct {
	summary         string
	promptTokens    int
	predictedTokens int
	promptMs        int64
	predictedMs     int64
}

// defaultSummarizeThreshold is the auto-summarization threshold used when no config value
// is set. It is config's default, not a copy: the Settings UI shows that number, so a turn
// must summarize at the percentage it advertises.
const defaultSummarizeThreshold = config.DefaultAutoSummarizeThreshold

// shouldSummarize reports whether usage has crossed the prompt budget.
func shouldSummarize(usedTokens, contextWindow, thresholdPct int) bool {
	budget := promptBudget(contextWindow, thresholdPct)
	return budget > 0 && usedTokens >= budget
}

// contextTokensUsed is what the conversation costs: prompt + completion of the last call, the number the
// frontend shows, or that estimate when usage is unreported. It leaves out the tool results appended since,
// which fitRequestToWindow judges — counting them here would summarize away a read the model just asked
// for.
func contextTokensUsed(lc *loopContext) int {
	if lc != nil && lc.usage != nil && lc.usage.PromptTokens > 0 {
		return lc.usage.PromptTokens + lc.usage.CompletionTokens
	}
	return conversationTokens(buildLLMContext(lc.messages))
}

// summarizeContext produces the rolling summary. It never modifies the messages slice.
func (s *summarizeOrchestrator) summarizeContext(ctx context.Context, messages []types.ChatMessage, serverURL, modelName, previousSummary string) (summarizeResult, error) {
	chatHistory := formatChatHistory(buildLLMContext(messages), previousSummary != "")
	systemPrompt, userPrompt, err := prompts.RenderSummarizePrompt(prompts.SummarizeData{
		ChatHistory:     chatHistory,
		PreviousSummary: previousSummary,
	})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("load summarize prompt: %w", err)
	}
	// Some chat templates reject a system-only request, so the content rides the user message.
	summarizeMessages := []types.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// ReasoningEffort "" means reasoning on, not off.
	usage, summary, err := s.chatCompletion(ctx, serverURL, modelName, summarizeMessages, &ChatCompletionOpts{ReasoningEffort: ""})
	if err != nil {
		return summarizeResult{}, fmt.Errorf("summarization call failed: %w", err)
	}
	// An empty summary must be rejected: buildLLMContext drops everything before the
	// summarization entry, so installing one erases the conversation.
	if strings.TrimSpace(summary) == "" {
		return summarizeResult{}, fmt.Errorf("summarization produced an empty summary")
	}

	res := summarizeResult{summary: summary}
	if usage != nil {
		res.promptTokens = usage.PromptTokens
		res.predictedTokens = usage.CompletionTokens
		res.promptMs = usage.PromptMs
		res.predictedMs = usage.PredictedMs
	}
	return res, nil
}

// buildLLMContext drops the summarization message and everything it replaced: their content lives
// in the system prompt. The system message itself is always kept.
func buildLLMContext(messages []types.ChatMessage) []types.ChatMessage {
	lastSummaryIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			lastSummaryIdx = i
			break
		}
	}

	var result []types.ChatMessage
	if len(messages) > 0 {
		result = append(result, messages[0])
	}
	startIdx := 1
	if lastSummaryIdx >= 0 {
		startIdx = lastSummaryIdx + 1
	}
	for i := startIdx; i < len(messages); i++ {
		m := messages[i]
		result = append(result, m)
	}
	return result
}

// formatChatHistory renders the conversation as plain text for the summarize prompt.
func formatChatHistory(messages []types.ChatMessage, skipSummaryMsg bool) string {
	toolResults := make(map[string]string)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

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
				continue // its content arrives as PreviousSummary
			}
			sb.WriteString("Assistant: [Context summary: ")
			sb.WriteString(m.Content)
			sb.WriteString("]\n")
			continue
		case "tool":
			// already folded into the assistant line above
			continue
		case "advisory":
			// LSP notes are noise for a summary
			continue
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		case "assistant":
			if len(m.ToolCalls) > 0 {
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
			// multi-modal: the text is in ContentParts, not Content
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

// extractPrevSummary returns the summary in effect: the content of the last summarization message, since
// a rolling summary appends a message rather than rewriting one. store.LastSummarizationMessageID is the
// same rule over stored rows, and the two must name the same one — that id decides which edited row is
// worth waking a running task for.
func extractPrevSummary(messages []types.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "summarization" {
			return messages[i].Content
		}
	}
	return ""
}

// setSystemPrompt installs prompt as the system message. Index 0 is the system message wherever
// one is built, so the normal case is an in-place replace; prepending is the fallback.
func setSystemPrompt(messages []types.ChatMessage, prompt string) []types.ChatMessage {
	if len(messages) > 0 && messages[0].Role == "system" {
		messages[0].Content = prompt
		return messages
	}
	logging.For(logging.Summarize).Infof("warning: conversation has no system message at index 0; prepending one to carry the summary")
	return append([]types.ChatMessage{{Role: "system", Content: prompt}}, messages...)
}

// installSummary returns the conversation as the next model call will see it — the marker row
// appended and the summary rewritten into the system prompt, its only route to the model — and what
// that call will consume. The number is estimated: no provider reports usage for a context that has
// not been sent.
func installSummary(messages []types.ChatMessage, task *types.Task, worktreePath string, scope promptScope, summary string) ([]types.ChatMessage, int) {
	messages = append(messages, types.ChatMessage{Role: "summarization", Content: summary})
	messages = setSystemPrompt(messages, composeSystemPrompt(task, worktreePath, scope, summary))
	return messages, estimatePromptTokens(buildLLMContext(messages))
}

// injectProceedMessage puts the instruction in front of the run that will be fed the summary: the row
// first, then the same message in the conversation the next call reads.
func (s *summarizeOrchestrator) injectProceedMessage(setup *taskExecSetup, lc *loopContext) {
	if err := s.sendProceed(setup.projectID, setup.task.ID, setup.task.CurrentThreadID); err != nil {
		logging.For(logging.Summarize).With(logging.TaskID, setup.task.ID).Warnf("proceed instruction not recorded: %v", err)
		return
	}
	lc.messages = append(lc.messages, types.ChatMessage{Role: "user", Content: proceedInstruction})
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

	origTask := task
	task.Summarizing = true
	_ = s.store.SetTaskSummarizing(taskID, true)
	s.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	clearFlag := func() {
		// Clear the caller's reference too: `task` may have been reloaded below.
		origTask.Summarizing = false
		task.Summarizing = false
		_ = s.store.SetTaskSummarizing(taskID, false)
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	}

	prevSummary := extractPrevSummary(messages)
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

	summaryMsgID := uuid.New().String()
	summaryCreatedAt := time.Now().UTC()
	// The anchor is the row's UpdatedAt, stamped when the Summarizing flag was set.
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

	_ = s.store.FixPostSummaryTimestamps(taskID, task.CurrentThreadID, summaryAnchor, summaryCreatedAt)

	messages, compressedUsed := installSummary(messages, task, setup.worktreePath, setup.promptScope(), sumResult.summary)

	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeSummarization, Data: map[string]any{
		"id":               summaryMsgID,
		"summary":          sumResult.summary,
		"created_at":       summaryCreatedAt.Format(time.RFC3339),
		"prompt_tokens":    sumResult.promptTokens,
		"predicted_tokens": sumResult.predictedTokens,
		"prompt_ms":        sumResult.promptMs,
		"predicted_ms":     sumResult.predictedMs,
		"model_name":       modelName,
		"context_used":     compressedUsed,
	}})
	logging.For(logging.Summarize).With(logging.TaskID, taskID).Infof("summarized context (messages preserved, LLM context compressed)")

	clearFlag()

	if resultCh != nil {
		resultCh <- nil
	}
	return messages, sumResult.summary
}

// maybeAutoSummarize summarizes when usage crosses the threshold. It runs right after an assistant
// message is appended, so it fires on the crossing rather than when the task goes idle. With
// injectProceed it appends a persisted "Proceed" user message for the next call.
func (s *summarizeOrchestrator) maybeAutoSummarize(setup *taskExecSetup, lc *loopContext, activeModel string, injectProceed bool) {
	if lc.contextWindow == nil {
		return
	}
	if !shouldSummarize(contextTokensUsed(lc), *lc.contextWindow, s.configStore.AutoSummarizeThreshold()) {
		return
	}
	s.summarizeNow(setup, lc, activeModel, injectProceed)
}

// summarizeNow compacts the conversation and installs the summary for the loop's next call. Both triggers
// come here, and neither gets a second summary out of a conversation that has not grown.
func (s *summarizeOrchestrator) summarizeNow(setup *taskExecSetup, lc *loopContext, activeModel string, injectProceed bool) {
	if len(lc.messages) == lc.summarizedAtLen {
		return
	}
	// Summarization goes to the task's CURRENT provider — lc.mc follows mid-run switches, setup.serverURL
	// does not.
	serverURL, _ := lc.mc.get()
	messages, summary := s.applySummarization(setup, setup.ctx, lc.messages, serverURL, activeModel, nil)
	if summary != "" {
		lc.messages = messages
		if injectProceed {
			s.injectProceedMessage(setup, lc)
		}
		// The Proceed instruction belongs to what this summarization left behind.
		lc.summarizedAtLen = len(lc.messages)
	}
	lc.detector.reset()
	lc.detector.clearHints()
}

// cancelSummarize cancels an in-progress summarization for a task.
func (s *summarizeOrchestrator) cancelSummarize(taskID string) {
	s.dbSummarizeCancel.mu.Lock()
	if cancel, ok := s.dbSummarizeCancel.tasks[taskID]; ok {
		cancel()
		delete(s.dbSummarizeCancel.tasks, taskID)
	}
	s.dbSummarizeCancel.mu.Unlock()

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
		s.cancelSummarize(taskID)
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

	state := s.runningState(taskID)

	if state != nil {
		defer func() {
			task.Summarizing = false
			_ = s.store.SetTaskSummarizing(taskID, false)
			// The row may have moved during summarization; broadcast what it says now.
			if fresh, err := s.store.GetTask(projectID, taskID); err == nil && fresh != nil {
				fresh.Summarizing = false
				task = fresh
			}
			s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}()
		// A background context: the summarize must survive the browser going away. A newer
		// request or the timeout cancels it.
		reqCtx, reqCancel := context.WithTimeout(context.Background(), 120*time.Second)
		resultCh := make(chan error, 1)
		req := &summarizeRequest{ctx: reqCtx, resultCh: resultCh, cancel: reqCancel}

		// A re-click or a ✕ cancels the request already in flight.
		state.activeSummarize.mu.Lock()
		if old := state.activeSummarize.request; old != nil {
			old.cancel()
		}
		state.activeSummarize.request = req
		state.activeSummarize.mu.Unlock()

		select {
		case state.summarizeCh <- req:
			select {
			case err := <-resultCh:
				if err != nil {
					httputil.Fault(w, "summarize", err)
					return
				}
			case <-reqCtx.Done():
				if reqCtx.Err() == context.DeadlineExceeded {
					httputil.JSONError(w, http.StatusInternalServerError, "summarization timed out")
					return
				}
				// not the timeout, so a newer request cancelled this one
				http.Error(w, "summarization cancelled", http.StatusRequestTimeout)
				return
			}
			s.blockedMessages.mu.Lock()
			delete(s.blockedMessages.tasks, taskID)
			s.blockedMessages.mu.Unlock()
			// the result itself goes out over the websocket
			httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "summarizing"})
			return
		default:
			// the loop is not taking work; fall through to the DB path
			reqCancel()
			state.activeSummarize.mu.Lock()
			if state.activeSummarize.request == req {
				state.activeSummarize.request = nil
			}
			state.activeSummarize.mu.Unlock()
		}
	}

	// Idle task: summarize from the DB, on a context the browser cannot cancel.
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

		clearFlag := func() {
			task.Summarizing = false
			_ = s.store.SetTaskSummarizing(taskID, false)
			if fresh, err := s.store.GetTask(projectID, taskID); err == nil && fresh != nil {
				fresh.Summarizing = false
				task = fresh
			}
			s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}

		taskMsgs, _ := s.store.GetTaskMessages(task.ID, task.CurrentThreadID)
		project, _ := s.store.GetProject(projectID)
		scope := promptScopeOf(project)
		worktreePath, _ := s.worktreeMgr.WorktreePath(taskID)

		messages := buildMessagesFromDB(task, taskMsgs, worktreePath, scope)
		previousSummary := extractPrevSummary(messages)

		serverURL, modelName, _ := s.utilityTarget(task)
		sumResult, err := s.summarizeContext(ctx, messages, serverURL, modelName, previousSummary)
		if err != nil {
			logging.For(logging.Summarize).With(logging.TaskID, taskID).Warnf("manual summarization failed: %v", err)
			clearFlag()
			return
		}

		summaryMsgID := uuid.New().String()
		summaryCreatedAt := time.Now().UTC()
		// Here the anchor is the newest existing message: every taskMsg predates the summary.
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

		// Only the estimate is wanted here; this path holds no live conversation.
		_, compressedUsed := installSummary(messages, task, worktreePath, scope, sumResult.summary)

		s.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: types.WSTypeSummarization, Data: map[string]any{
			"id":               summaryMsgID,
			"summary":          sumResult.summary,
			"created_at":       time.Now().UTC().Format(time.RFC3339),
			"prompt_tokens":    sumResult.promptTokens,
			"predicted_tokens": sumResult.predictedTokens,
			"prompt_ms":        sumResult.promptMs,
			"predicted_ms":     sumResult.predictedMs,
			"model_name":       task.ModelName,
			"context_used":     compressedUsed,
		}})

		logging.For(logging.Summarize).With(logging.TaskID, taskID).Infof("manual summarization complete (messages preserved, LLM context compressed)")

		// so the window's messages sort after the summary
		_ = s.store.FixPostSummaryTimestamps(taskID, task.CurrentThreadID, summaryAnchor, summaryCreatedAt)

		clearFlag()

		s.blockedMessages.mu.Lock()
		_, hasBlocked := s.blockedMessages.tasks[taskID]
		delete(s.blockedMessages.tasks, taskID)
		s.blockedMessages.mu.Unlock()
		if hasBlocked {
			s.lazyStart(projectID, taskID, "", "", "", "", false)
		}
	}()

	httputil.JSONResponse(w, http.StatusOK, map[string]any{
		"status": "summarizing",
	})
}

func (te *TaskExecutor) HandleSummarize(w http.ResponseWriter, r *http.Request) {
	te.summarize.HandleSummarize(w, r)
}
