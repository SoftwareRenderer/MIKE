package exec

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"kanban/backend/httputil"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

func (te *TaskExecutor) HandleRetry(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.RetryTask(projectID, taskID); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, _ := te.store.GetTask(projectID, taskID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleResume resumes a paused task (delegates to HandleRetry).
func (te *TaskExecutor) HandleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	te.HandleRetry(w, r)
}

// HandleSend accepts a user message and triggers task execution.
func (te *TaskExecutor) HandleSend(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Content         string `json:"content"`
		ModelName       string `json:"model_name"`
		LlamaServerURL  string `json:"llama_server_url"`
		ReasoningEffort string `json:"reasoning_effort"`
		Attachments     string `json:"attachments"`
		Regenerate      bool   `json:"regenerate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" && req.Attachments == "" {
		httputil.JSONError(w, http.StatusBadRequest, "content or attachments required")
		return
	}

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	threadID := task.CurrentThreadID

	// Move task to in_progress if it's in review or done
	if task.Status == "review" || task.Status == "done" {
		task.Status = "in_progress"
		task.ExecutionState = "idle"
		task.UpdatedAt = time.Now().UTC()
		_ = te.storeTask(task, projectID)
		te.appStreamMgr.BroadcastTaskUpdated(task.ProjectID, task)
	}

	// Check for duplicate — compare both content and attachments
	msgID := uuid.New().String()
	existingID, lookupErr := te.store.GetLastUserMessageInThread(threadID)
	isDuplicate := false
	if lookupErr == nil && existingID != "" {
		existingMsgs, _ := te.store.GetTaskMessages(taskID, threadID)
		for i := len(existingMsgs) - 1; i >= 0; i-- {
			if existingMsgs[i].ID == existingID {
				if existingMsgs[i].Content == req.Content && existingMsgs[i].Attachments == req.Attachments {
					isDuplicate = true
					msgID = existingID
				}
				break
			}
		}
	}

	if !isDuplicate {
		tm := &types.TaskMessage{
			ID:        msgID,
			TaskID:    taskID,
			ThreadID:  threadID,
			Role:      "user",
			Content:   req.Content,
			CreatedAt: time.Now().UTC(),
		}
		if req.Attachments != "" {
			tm.Attachments = req.Attachments
		}
		te.store.CreateTaskMessage(tm)
	}

	// Broadcast echo to WS clients
	echoData := map[string]any{"id": msgID, "role": "user", "content": req.Content}
	if threadID != "" {
		echoData["thread_id"] = threadID
	}
	if req.Attachments != "" {
		var atts []any
		if err := json.Unmarshal([]byte(req.Attachments), &atts); err == nil && len(atts) > 0 {
			echoData["attachments"] = atts
		}
	}
	// Broadcast user_message echo to task subscribers
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeUserMessage, Data: echoData})

	// Forward to executor or lazy-start. Skip when the message was deduped
	if isDuplicate {
		log.Printf("[send] task=%s duplicate user message (%q) not persisted, skipping executor trigger", taskID, req.Content)
		te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "idle", "activity": "Duplicate message ignored"}})
	} else {
		te.ForwardUserMessage(projectID, taskID, stream.UserMessage{
			Content:         req.Content,
			ModelName:       req.ModelName,
			ServerURL:       req.LlamaServerURL,
			ReasoningEffort: req.ReasoningEffort,
			Attachments:     req.Attachments,
			Regenerate:      req.Regenerate,
		})
	}

	// Return the message
	result := map[string]any{
		"id":      msgID,
		"role":    "user",
		"content": req.Content,
	}
	if threadID != "" {
		result["thread_id"] = threadID
	}
	httputil.JSONResponse(w, http.StatusCreated, result)
}

// cancelSummarize cancels an in-progress summarization for a task.
func (te *TaskExecutor) cancelSummarize(taskID, projectID string) {
	// Cancel DB-based background summarization.
	te.dbSummarizeCancel.mu.Lock()
	if cancel, ok := te.dbSummarizeCancel.tasks[taskID]; ok {
		cancel()
		delete(te.dbSummarizeCancel.tasks, taskID)
	}
	te.dbSummarizeCancel.mu.Unlock()

	// Cancel channel-based summarization (executor running).
	te.taskMu.Lock()
	state := te.tasks[taskID]
	te.taskMu.Unlock()
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
func (te *TaskExecutor) HandleSummarize(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if task.LlamaServerURL == "" || task.ModelName == "" {
		httputil.JSONError(w, http.StatusBadRequest, "task has no LLM configured")
		return
	}

	// If already summarizing, cancel it (acts as the cancel endpoint).
	if task.Summarizing {
		log.Printf("[summarize] task=%s cancelling in-progress summarization", taskID)
		te.cancelSummarize(taskID, projectID)
		task.Summarizing = false
		_ = te.store.SetTaskSummarizing(taskID, false)
		te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "cancelled"})
		return
	}

	// Set summarizing flag so the UI survives refresh and ForwardUserMessage blocks lazy-start.
	task.Summarizing = true
	_ = te.store.SetTaskSummarizing(taskID, true)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)

	// If task is actively running, send request to the execution loop
	te.taskMu.Lock()
	state := te.tasks[taskID]
	te.taskMu.Unlock()

	if state != nil {
		defer func() {
			task.Summarizing = false
			_ = te.store.SetTaskSummarizing(taskID, false)
			// Broadcast the freshest row
			if fresh, err := te.store.GetTask(projectID, taskID); err == nil && fresh != nil {
				fresh.Summarizing = false
				task = fresh
			}
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
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
			te.blockedMessages.mu.Lock()
			delete(te.blockedMessages.tasks, taskID)
			te.blockedMessages.mu.Unlock()
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
	te.dbSummarizeCancel.mu.Lock()
	te.dbSummarizeCancel.tasks[taskID] = cancel
	te.dbSummarizeCancel.mu.Unlock()

	go func() {
		defer func() {
			te.dbSummarizeCancel.mu.Lock()
			delete(te.dbSummarizeCancel.tasks, taskID)
			te.dbSummarizeCancel.mu.Unlock()
		}()

		// Clear the summarizing flag and broadcast the task
		clearFlag := func() {
			task.Summarizing = false
			_ = te.store.SetTaskSummarizing(taskID, false)
			// Broadcast the freshest row
			if fresh, err := te.store.GetTask(projectID, taskID); err == nil && fresh != nil {
				fresh.Summarizing = false
				task = fresh
			}
			te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		}

		taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
		project, _ := te.store.GetProject(projectID)
		projectName := ""
		if project != nil {
			projectName = project.Name
		}
		worktreePath, _ := te.worktreeMgr.WorktreePath(taskID)

		messages := buildMessagesFromDB(task, taskMsgs, worktreePath, projectName, false)
		previousSummary := extractPrevSummary(messages)

		// Route summarization through the configured Utility provider/model.
		serverURL, modelName, _ := te.utilityTarget(task)
		sumResult, err := te.summarizeContext(ctx, messages, serverURL, modelName, previousSummary)
		if err != nil {
			log.Printf("[summarize] task=%s manual summarization failed: %v", taskID, err)
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
		_ = te.store.CreateTaskMessage(&types.TaskMessage{
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
		te.appStreamMgr.SendTaskEvent(projectID, task.ID, types.StreamEvent{Type: "summarization", Data: map[string]any{
			"id":               summaryMsgID,
			"summary":          sumResult.summary,
			"created_at":       time.Now().UTC().Format(time.RFC3339),
			"prompt_tokens":    sumResult.promptTokens,
			"predicted_tokens": sumResult.predictedTokens,
			"prompt_ms":        sumResult.promptMs,
			"predicted_ms":     sumResult.predictedMs,
			"model_name":       task.ModelName,
		}})

		log.Printf("[summarize] task=%s manual summarization complete (messages preserved, LLM context compressed)", taskID)

		// Fix timestamps of messages sent during the summarization window
		// so they sort after the summary in the database.
		_ = te.store.FixPostSummaryTimestamps(taskID, task.CurrentThreadID, summaryAnchor, summaryCreatedAt)

		clearFlag()

		// Only start executor if there are blocked messages for this task.
		te.blockedMessages.mu.Lock()
		_, hasBlocked := te.blockedMessages.tasks[taskID]
		delete(te.blockedMessages.tasks, taskID)
		te.blockedMessages.mu.Unlock()
		if hasBlocked {
			te.handleLazyStart(projectID, taskID, "", "", "", "", false)
		}
	}()

	// Return immediately — summarization runs in background.
	httputil.JSONResponse(w, http.StatusOK, map[string]any{
		"status": "summarizing",
	})
}

// HandleCompact triggers agent context compaction via RPC.
// If the task has an active agent runner, compaction is performed immediately.
// If no runner exists (task is idle/stopped), the executor starts a temporary
// agent runner, performs compaction, and keeps the runner alive for future
// follow-up operations (the runner is normally kept alive after task completion).
func (te *TaskExecutor) HandleCompact(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if !te.configStore.IsAgentProvider(task.LlamaServerURL) {
		httputil.JSONError(w, http.StatusBadRequest, "compaction only available for agent tasks")
		return
	}

	// Ensure we have an active agent runner.
	runner, err := te.getAgentRunner(r.Context(), projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Use the runner to perform compaction.
	compactCtx, compactCancel := context.WithTimeout(r.Context(), 120*time.Second)
	compactResult, err := runner.Compact(compactCtx)
	compactCancel()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "Compaction failed: "+err.Error())
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "compacted", "summary": compactResult.Summary})
}
