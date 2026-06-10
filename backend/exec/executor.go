package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/stream"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// TaskExecutor handles task execution endpoints and orchestrates AI task execution.
type TaskExecutor struct {
	store          *store.Store
	configStore    *config.ConfigStore
	boardStreamMgr *stream.BoardStreamManager
	worktreeMgr    *git.WorktreeManager
	streamMgr      *stream.StreamManager
	taskMu         sync.Mutex
	tasks          map[string]*executionState
}

type executionState struct {
	ctx         context.Context
	cancel      context.CancelFunc
	userMsgChan chan stream.UserMessage
}

// modelConfig holds mutable model/provider settings for an executor.
type modelConfig struct {
	mu        sync.Mutex
	serverURL string
	modelName string
}

func (mc *modelConfig) get() (string, string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.serverURL, mc.modelName
}

func (mc *modelConfig) set(serverURL, modelName string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.serverURL = serverURL
	mc.modelName = modelName
}

// NewTaskExecutor creates a new TaskExecutor.
func NewTaskExecutor(store *store.Store, configStore *config.ConfigStore, boardStreamMgr *stream.BoardStreamManager) *TaskExecutor {
	te := &TaskExecutor{
		store:          store,
		configStore:    configStore,
		boardStreamMgr: boardStreamMgr,
		streamMgr:      stream.NewStreamManager(store),
		tasks:          make(map[string]*executionState),
	}
	te.worktreeMgr = git.NewWorktreeManager(store, configStore)
	te.streamMgr.SetLazyStartHandler(te.handleLazyStart)
	return te
}

// HandleStart starts a task's AI execution.
func (te *TaskExecutor) HandleStart(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
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
	te.boardStreamMgr.Broadcast(projectID)
	task, _ := te.store.GetTask(projectID, taskID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStop stops a running task.
func (te *TaskExecutor) HandleStop(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.StopTask(taskID); err != nil {
		httputil.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	te.boardStreamMgr.Broadcast(projectID)
	task, _ := te.store.GetTask(projectID, taskID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleRetry retries a failed task.
func (te *TaskExecutor) HandleRetry(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.RetryTask(projectID, taskID); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	te.boardStreamMgr.Broadcast(projectID)
	task, _ := te.store.GetTask(projectID, taskID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleResume resumes a paused task (same as retry for now).
func (te *TaskExecutor) HandleResume(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	te.HandleRetry(w, r, projectID, taskID)
}

// HandleStream handles WebSocket stream for task output.
func (te *TaskExecutor) HandleStream(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	te.streamMgr.HandleStream(w, r, taskID)
}

// RemoveStream removes and closes the broadcaster for a task.
func (te *TaskExecutor) RemoveStream(taskID string) {
	te.streamMgr.Remove(taskID)
}

// WorktreeMgr returns the WorktreeManager. Used by server wiring.
func (te *TaskExecutor) WorktreeMgr() *git.WorktreeManager {
	return te.worktreeMgr
}

// ResetForTest stops all running executors and clears state.
func (te *TaskExecutor) ResetForTest() {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	for taskID, state := range te.tasks {
		state.cancel()
		delete(te.tasks, taskID)
	}
	te.streamMgr.ResetForTest()
}

// StartTask begins AI execution for a task.
func (te *TaskExecutor) StartTask(projectID, taskID string) error {
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return err
	}
	if task.StatusState == "running" {
		te.taskMu.Lock()
		_, activelyRunning := te.tasks[taskID]
		te.taskMu.Unlock()
		if !activelyRunning {
			log.Printf("[start] task=%s stale running state detected, resetting to idle", taskID)
			task.StatusState = "idle"
			task.Status = "todo"
			if err := te.store.UpdateTask(task); err != nil {
				return fmt.Errorf("reset stale state: %w", err)
			}
		} else {
			return fmt.Errorf("task already running")
		}
	}

	serverURL := task.LlamaServerURL
	if serverURL == "" {
		return fmt.Errorf("llama server URL not configured")
	}

	modelName := task.ModelName
	branchName := fmt.Sprintf("task/%s", task.ID)

	worktreePath, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName)
	if err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}

	task.Branch = branchName
	task.StatusState = "running"
	task.Status = "in_progress"
	if err := te.store.UpdateTask(task); err != nil {
		return err
	}

	existingMsgs, _ := te.store.GetTaskMessages(taskID)
	if len(existingMsgs) == 0 {
		_ = te.store.CreateTaskMessage(&types.TaskMessage{
			ID:        uuid.New().String(),
			TaskID:    taskID,
			Role:      "user",
			Content:   task.Prompt,
			CreatedAt: time.Now().UTC(),
		})
	}

	b := te.streamMgr.GetOrCreate(taskID)
	b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})

	ctx, cancel := context.WithCancel(context.Background())
	userMsgChan := make(chan stream.UserMessage, 16)
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan}
	te.taskMu.Unlock()

	b.SetUserMessageHandler(func(um stream.UserMessage) {
		select {
		case userMsgChan <- um:
		default:
		}
	})

	go te.executeTask(ctx, task, projectID, worktreePath, serverURL, modelName, b, userMsgChan)
	return nil
}

// StopTask cancels a running task.
func (te *TaskExecutor) StopTask(taskID string) error {
	te.taskMu.Lock()
	defer te.taskMu.Unlock()
	state, ok := te.tasks[taskID]
	if !ok {
		return fmt.Errorf("task not running")
	}
	state.cancel()
	delete(te.tasks, taskID)
	return nil
}

// RetryTask retries a failed task.
func (te *TaskExecutor) RetryTask(projectID, taskID string) error {
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return err
	}
	task.StatusState = "idle"
	te.store.UpdateTask(task)
	return te.StartTask(projectID, taskID)
}

// handleLazyStart is called when a user message arrives for a task with no active executor.
func (te *TaskExecutor) handleLazyStart(projectID, taskID, content, modelName, serverURL string) {
	log.Printf("[lazy-start] task=%s project=%s (override: model=%s, url=%s)", taskID, projectID, modelName, serverURL)

	te.taskMu.Lock()
	if _, exists := te.tasks[taskID]; exists {
		te.taskMu.Unlock()
		return
	}
	te.taskMu.Unlock()

	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil {
		log.Printf("[lazy-start] task=%s get error: %v", taskID, err)
		return
	}
	if task.StatusState == "completed" {
		return
	}

	task.StatusState = "running"
	task.Status = "in_progress"
	_ = te.store.UpdateTask(task)
	te.boardStreamMgr.Broadcast(projectID)

	if serverURL == "" {
		serverURL = task.LlamaServerURL
	}
	if serverURL == "" {
		return
	}

	if modelName == "" {
		modelName = task.ModelName
	}
	log.Printf("[lazy-start] task=%s url=%s model=%s", taskID, serverURL, modelName)

	branchName := fmt.Sprintf("task/%s", task.ID)

	wtPath, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName)
	if err != nil {
		log.Printf("[lazy-start] task=%s worktree error: %v", taskID, err)
		return
	}
	log.Printf("[lazy-start] task=%s worktree=%s", taskID, wtPath)

	b := te.streamMgr.GetOrCreate(taskID)
	b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Agent responding"}})

	ctx, cancel := context.WithCancel(context.Background())
	userMsgChan := make(chan stream.UserMessage, 16)
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: userMsgChan}
	te.taskMu.Unlock()

	b.SetUserMessageHandler(func(um stream.UserMessage) {
		select {
		case userMsgChan <- um:
		default:
		}
	})

	log.Printf("[lazy-start] task=%s starting executor", taskID)
	go te.executeTask(ctx, task, projectID, wtPath, serverURL, modelName, b, userMsgChan)
}

// -- Helper functions --

var wordRe = regexp.MustCompile(`\S+`)

func estimateTokensFromText(text string) int {
	words := wordRe.FindAllString(text, -1)
	return int(float64(len(words)) * 1.3)
}

func estimatePromptTokens(messages []types.ChatMessage) int {
	total := 0
	for _, m := range messages {
		total += estimateTokensFromText(m.Content)
		total += estimateTokensFromText(m.ReasoningContent)
	}
	return total
}

func estimateTotalTokens(content, reasoning string) int {
	return estimateTokensFromText(content) + estimateTokensFromText(reasoning)
}

func parseModelMetadata(msg string) (content, modelName, serverURL string) {
	idx := strings.Index(msg, "\n")
	if idx <= 0 {
		return msg, "", ""
	}
	head := msg[:idx]
	content = msg[idx+1:]
	var meta map[string]string
	if err := json.Unmarshal([]byte(head), &meta); err != nil {
		return msg, "", ""
	}
	return content, meta["model_name"], meta["llama_server_url"]
}
