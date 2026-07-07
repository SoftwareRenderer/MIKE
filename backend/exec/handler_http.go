package exec

import (
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content == "" {
		httputil.JSONError(w, http.StatusBadRequest, "content is required")
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

	// Check for duplicate (same content as last user message in thread)
	msgID := uuid.New().String()
	existingID, lookupErr := te.store.GetLastUserMessageInThread(threadID)
	isDuplicate := false
	if lookupErr == nil && existingID != "" {
		existingMsgs, _ := te.store.GetTaskMessages(taskID, threadID)
		for i := len(existingMsgs) - 1; i >= 0; i-- {
			if existingMsgs[i].ID == existingID {
				if existingMsgs[i].Content == req.Content {
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
	te.forwardUserMessage(projectID, taskID, stream.UserMessage{
		Content:         req.Content,
		ModelName:       req.ModelName,
		ServerURL:       req.LlamaServerURL,
		ReasoningEffort: req.ReasoningEffort,
		Attachments:     req.Attachments,
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
		resultCh := make(chan error, 1)
		select {
		case state.summarizeCh <- resultCh:
			// Request sent, wait for result
			select {
			case err := <-resultCh:
				if err != nil {
					httputil.JSONError(w, http.StatusInternalServerError, err.Error())
					return
				}
			case <-time.After(60 * time.Second):
				httputil.JSONError(w, http.StatusInternalServerError, "summarization timed out")
				return
			}
			// Result is sent via WebSocket, return minimal response
			httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "summarizing"})
			return
		default:
			// Channel full, fall through to DB-based approach
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

	// Build system prompt for summarizeContext (same as used in buildMessagesFromDB)
	systemPrompt := messages[0].Content
	previousSummary := extractPrevSummary(messages)

	sumResult, err := te.summarizeContext(messages, systemPrompt, task.LlamaServerURL, task.ModelName, previousSummary)
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
