package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"mike/backend/httputil"
	"mike/backend/store"
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
	// Read again with the error kept: this is the response body, and a retried task the store cannot
	// answer is not a 200 carrying null — the client's onTaskUpdated(null) would blank the card it just
	// asked to retry.
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
		ClientToken     string `json:"client_token"`
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

	// A duplicate is the same submit arriving twice (double-click, WS replay, offline queue
	// flush): the thread's last row is still that identical user message. Once the agent has
	// answered anything since, the same words are a new answer.
	//
	// A regenerate is not one: the row it matches is the question being re-answered, and the
	// request is to run the thread as it stands — that is how "New Thread" arrives, the fork
	// writing the edited question and this send starting it. So the row is not written twice,
	// but the executor still runs.
	msgID := uuid.New().String()
	lastRowIsThisMessage := false
	if last, err := te.store.GetLastMessageInThread(threadID); err == nil && last != nil &&
		last.Role == "user" && last.Content == req.Content && last.Attachments == req.Attachments {
		lastRowIsThisMessage = true
		msgID = last.ID
	}
	isDuplicate := lastRowIsThisMessage && !req.Regenerate

	// allowed Pending: a message that will run starts the task, so it leaves review here.
	// A deduped message starts nothing and must not move the card.
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

	if err := te.store.ClaimWrite(req.ClientToken, projectID, taskID, store.ClaimSend, msgID); err != nil {
		logging.For(logging.Exec).With(logging.TaskID, taskID).Warnf("send %s went unclaimed: %v", msgID, err)
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

// HandleCompact triggers agent context compaction via RPC.
// If the task has an active agent runner, compaction is performed immediately.
// If no runner exists (task is idle/stopped), the executor starts a temporary
// agent runner, performs compaction, and keeps the runner alive for future
// follow-up operations (the runner is normally kept alive after task completion).
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

	// Ensure we have an active agent runner.
	runner, err := te.getAgentRunner(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "start agent", "task not found")
		return
	}

	// Use the runner to perform compaction.
	compactCtx, compactCancel := context.WithTimeout(r.Context(), 120*time.Second)
	compactResult, err := runner.Compact(compactCtx)
	compactCancel()
	if err != nil {
		httputil.Fault(w, "compact", err)
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]any{"status": "compacted", "summary": compactResult.Summary})
}
