package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"mike/backend/httputil"
	"mike/backend/stream"
	"mike/backend/types"

	"github.com/google/uuid"
	"mike/pkg/logging"
)

// HandleRetry restarts a task's turn on its existing conversation tree. The route is registered as
// `resume`: the client's Resume control and a restart of a failed turn are the same operation here.
func (te *TaskExecutor) HandleRetry(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if err := te.RetryTask(projectID, taskID); err != nil {
		// Fail, not Fault: RetryTask propagates StartTask, which refuses with ErrTaskAlreadyRunning,
		// and that is the caller's own doing (409), not a server fault.
		httputil.Fail(w, err, "retry task", "task not found")
		return
	}
	// Read task again; a 200 with null would cause the client to blank the task card.
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	te.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleSend accepts a user message and triggers task execution.
func (te *TaskExecutor) HandleSend(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	var req struct {
		Content         string `json:"content"`
		ModelName       string `json:"model_name"`
		LlamaServerURL  string `json:"llama_server_url"`
		ReasoningEffort string `json:"reasoning_effort"`
		Attachments     string `json:"attachments"`
		Regenerate      bool   `json:"regenerate"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Content == "" && req.Attachments == "" {
		httputil.JSONError(w, http.StatusBadRequest, "content or attachments required")
		return
	}

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}

	// Every user message arrives here, and it replaces any commit-and-done click still waiting on a
	// merge conflict.
	te.merge.clearCommitAndDone(taskID)

	threadID := task.CurrentThreadID

	// A duplicate is an identical message arriving twice (e.g. double-click, offline queue flush). If the agent has since replied, it is no longer a duplicate.
	//
	// Regenerate requests are not duplicates; they rerun the thread as it stands. For a "New Thread", the fork writes the question and this handler starts execution.
	msgID := uuid.New().String()
	lastRowIsThisMessage := false
	if last, err := te.store.GetLastMessageInThread(threadID); err == nil && last != nil &&
		last.Role == "user" && last.Content == req.Content && last.Attachments == req.Attachments {
		lastRowIsThisMessage = true
		msgID = last.ID
	}
	isDuplicate := lastRowIsThisMessage && !req.Regenerate

	// Start the task if it was in Review or Done. Deduped messages do not move the task.
	if !isDuplicate && (task.Status == types.StatusReview || task.Status == types.StatusDone) {
		task.Status = types.StatusInProgress
		task.ExecutionState = types.ExecStateIdle
		task.UpdatedAt = time.Now().UTC()
		_ = te.storeTask(task, projectID)
		te.appStreamMgr.BroadcastTaskUpdated(task.ProjectID, task)
	}

	if !lastRowIsThisMessage {
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
	te.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeUserMessage, Data: echoData})

	if isDuplicate {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Infof("duplicate user message (%q) not persisted, skipping executor trigger", req.Content)
		te.appStreamMgr.SendTaskEvent(projectID, taskID, types.TurnStatusEvent("idle", "Duplicate message ignored"))
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

// HandleCompact triggers agent context compaction via RPC. If no agent runner
// exists, one is started to perform the operation.
func (te *TaskExecutor) HandleCompact(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	if !te.configStore.IsAgentProvider(task.LlamaServerURL) {
		httputil.JSONError(w, http.StatusBadRequest, "compaction only available for agent tasks")
		return
	}

	runner, err := te.getAgentRunner(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "start agent", "task not found")
		return
	}

	compactCtx, compactCancel := context.WithTimeout(r.Context(), 120*time.Second)
	compactResult, err := runner.Compact(compactCtx)
	compactCancel()
	if err != nil {
		httputil.Fault(w, "compact", err)
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "compacted", "summary": compactResult.Summary})
}
