package exec

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"kanban/backend/httputil"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

func (te *TaskExecutor) HandleStart(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Printf("[start] task=%s project=%s", taskID, projectID)
	if err := te.StartTask(projectID, taskID); err != nil {
		log.Printf("[start] task=%s error: %v", taskID, err)
		if strings.Contains(err.Error(), "already running") {
			httputil.JSONError(w, http.StatusConflict, err.Error())
		} else {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	log.Printf("[start] task=%s started", taskID)
	task, _ := te.store.GetTask(projectID, taskID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

func (te *TaskExecutor) HandleStop(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.StopTask(taskID); err != nil {
		httputil.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	task, _ := te.store.GetTask(projectID, taskID)
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

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
		_ = te.store.UpdateTask(task)
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

	// Forward to executor or lazy-start
	te.ForwardUserMessage(projectID, taskID, stream.UserMessage{
		Content:         req.Content,
		ModelName:       req.ModelName,
		ServerURL:       req.LlamaServerURL,
		ReasoningEffort: req.ReasoningEffort,
		Attachments:     req.Attachments,
		Regenerate:      req.Regenerate,
	})

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

	// If task is actively running, send request to the execution loop
	te.taskMu.Lock()
	state := te.tasks[taskID]
	te.taskMu.Unlock()

	if state != nil {
		// Create a cancellable context for this summarize request.
		// Cancelled when: user clicks ✕ (HTTP abort), new summarize request arrives, or timeout.
		reqCtx, reqCancel := context.WithTimeout(r.Context(), 60*time.Second)
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
			// Request sent, wait for result (respects HTTP abort via r.Context)
			select {
			case err := <-resultCh:
				if err != nil {
					httputil.JSONError(w, http.StatusInternalServerError, err.Error())
					return
				}
			case <-r.Context().Done():
				// User clicked ✕ or disconnected — handler exits, loop sees ctx.Done
				return
			case <-reqCtx.Done():
				if reqCtx.Err() == context.DeadlineExceeded {
					httputil.JSONError(w, http.StatusInternalServerError, "summarization timed out")
					return
				}
				// Cancelled by new request or HTTP abort
				return
			}
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

	// Task is idle/stopped — summarize from DB
	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	project, _ := te.store.GetProject(projectID)
	projectName := ""
	if project != nil {
		projectName = project.Name
	}
	worktreePath, _ := te.worktreeMgr.WorktreePath(taskID)

	messages := buildMessagesFromDB(task, taskMsgs, worktreePath, projectName, false)

	previousSummary := extractPrevSummary(messages)

	sumResult, err := te.summarizeContext(r.Context(), messages, task.LlamaServerURL, task.ModelName, previousSummary)
	if err != nil {
		log.Printf("[summarize] task=%s manual summarization failed: %v", taskID, err)
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Persist summary with stats
	summaryMsgID := uuid.New().String()
	_ = te.store.CreateTaskMessage(&types.TaskMessage{
		ID:              summaryMsgID,
		TaskID:          task.ID,
		ThreadID:        task.CurrentThreadID,
		Role:            "summarization",
		Content:         sumResult.summary,
		ModelName:       task.ModelName,
		PromptTokens:    sumResult.promptTokens,
		PredictedTokens: sumResult.predictedTokens,
		PromptMs:        int(sumResult.promptMs),
		PredictedMs:     int(sumResult.predictedMs),
		CreatedAt:       time.Now().UTC(),
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

	// Return updated token estimate
	httputil.JSONResponse(w, http.StatusOK, map[string]any{
		"summary":     sumResult.summary,
		"old_count":   sumResult.oldLen,
		"new_count":   sumResult.oldLen + 1, // summary appended, all messages preserved
	})
}

// HandleCompact triggers agent context compaction via RPC.
// Only works for tasks actively running with agent executor.
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

	// Check if task has an agent runner (running or idle).
	// Agent runner stays alive after completion for on-demand compaction.
	te.taskMu.Lock()
	state := te.tasks[taskID]
	te.taskMu.Unlock()

	if state == nil {
		// Task not in active executors — it may have been stopped, restarted, or
		// the server may have restarted. The task still exists in the store.
		httputil.JSONError(w, http.StatusNotFound, "task not found in active executors")
		return
	}
	if state.executorType != "agent" {
		httputil.JSONError(w, http.StatusBadRequest, "compaction only available for agent tasks")
		return
	}

	// If task is not actively running, we need to send the compact command
	// directly to the agent runner (not through the event loop's compactCh).
	if state.agentRunner != nil {
		compactCtx, compactCancel := context.WithTimeout(r.Context(), 120*time.Second)
		compactResult, err := state.agentRunner.Compact(compactCtx)
		compactCancel()
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "compacted", "summary": compactResult.Summary})
		return
	}

	// Task is actively running — use the event loop's compact channel.
	resultCh := make(chan *CompactResult, 1)
	var compactResult *CompactResult
	select {
	case state.compactCh <- resultCh:
		select {
		case compactResult = <-resultCh:
			if compactResult.Error != nil {
				httputil.JSONError(w, http.StatusInternalServerError, compactResult.Error.Error())
				return
			}
		case <-time.After(120 * time.Second):
			httputil.JSONError(w, http.StatusInternalServerError, "compaction timed out")
			return
		}
		httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "compacted", "summary": compactResult.Summary})
	default:
		httputil.JSONError(w, http.StatusConflict, "compaction already in progress")
	}
}
