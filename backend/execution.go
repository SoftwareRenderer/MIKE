package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TaskExecutor handles task execution endpoints and orchestrates AI task execution.
type TaskExecutor struct {
	store       *Store
	configStore *ConfigStore
	worktreeMgr *WorktreeManager
	streamMgr   *StreamManager
	taskMu      sync.Mutex
	tasks       map[string]*executionState
}

type executionState struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTaskExecutor creates a new TaskExecutor.
func NewTaskExecutor(store *Store, configStore *ConfigStore) *TaskExecutor {
	te := &TaskExecutor{
		store:       store,
		configStore: configStore,
		streamMgr:   NewStreamManager(),
		tasks:       make(map[string]*executionState),
	}
	te.worktreeMgr = NewWorktreeManager(store, configStore)
	return te
}

// HandleStart starts a task's AI execution.
func (te *TaskExecutor) HandleStart(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.StartTask(projectID, taskID); err != nil {
		if strings.Contains(err.Error(), "already running") {
			jsonError(w, http.StatusConflict, err.Error())
		} else {
			jsonError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	task, _ := te.store.GetTask(projectID, taskID)
	jsonResponse(w, http.StatusOK, task)
}

// HandleStop stops a running task.
func (te *TaskExecutor) HandleStop(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.StopTask(taskID); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	task, _ := te.store.GetTask(projectID, taskID)
	jsonResponse(w, http.StatusOK, task)
}

// HandleRetry retries a failed task.
func (te *TaskExecutor) HandleRetry(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := te.RetryTask(projectID, taskID); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, _ := te.store.GetTask(projectID, taskID)
	jsonResponse(w, http.StatusOK, task)
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

// StartTask begins AI execution for a task.
func (te *TaskExecutor) StartTask(projectID, taskID string) error {
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		return err
	}
	if task.StatusState == "running" {
		return fmt.Errorf("task already running")
	}

	cfg, err := te.configStore.GetConfig()
	if err != nil {
		return fmt.Errorf("get config: %w", err)
	}

	serverURL := task.LlamaServerURL
	if serverURL == "" {
		serverURL = cfg.LlamaServerURL
	}
	if serverURL == "" {
		return fmt.Errorf("llama server URL not configured")
	}

	modelName := task.ModelName
	if modelName == "" {
		modelName = cfg.ModelName
	}

	branchName := fmt.Sprintf("task/%s", task.ID[:8])
	if len(task.ID) < 8 {
		branchName = fmt.Sprintf("task/%s", task.ID)
	}

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

	b := te.streamMgr.GetOrCreate(taskID)
	b.send(StreamEvent{Type: WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Starting task execution"}})

	ctx, cancel := context.WithCancel(context.Background())
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel}
	te.taskMu.Unlock()

	go te.executeTask(ctx, task, worktreePath, serverURL, modelName, b)
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
	te.store.ClearConversationHistory(taskID)
	return te.StartTask(projectID, taskID)
}

// executeTask runs the AI conversation loop.
func (te *TaskExecutor) executeTask(ctx context.Context, task *Task, worktreePath, serverURL, modelName string, b *broadcaster) {
	defer func() {
		te.taskMu.Lock()
		delete(te.tasks, task.ID)
		te.taskMu.Unlock()
		te.streamMgr.Remove(task.ID)
	}()

	client := NewLlamaClient(serverURL, modelName)
	tools := GetToolDefinitions()
	var systemPrompt string
	if task.PlanMode {
		systemPrompt = BuildPlanModeSystemPrompt(worktreePath)
	} else {
		systemPrompt = BuildSystemPrompt(worktreePath)
	}

	history, _ := te.store.GetConversationHistory(task.ID)
	messages := []ChatMessage{{Role: "system", Content: systemPrompt}}
	for _, h := range history {
		messages = append(messages, ChatMessage{Role: h.Role, Content: h.Content})
	}
	messages = append(messages, ChatMessage{Role: "user", Content: task.Prompt})

	consecutiveFailures := 0
	maxFailures := task.MaxConsecutiveFailedTurns
	if maxFailures == 0 {
		maxFailures = 3
	}

	var assistantContent strings.Builder
	var pendingToolCalls []ChatToolCall
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			task.StatusState = "failed"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			b.send(StreamEvent{Type: WSTypeError, Data: map[string]string{"message": "Task stopped", "code": "stopped"}})
			return
		default:
		}

		usage, err := client.ChatStream(messages, tools, func(choice ChatChoice) {
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				assistantContent.WriteString(*choice.Delta.Content)
				b.send(StreamEvent{Type: WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
			}
			if len(choice.Delta.ToolCalls) > 0 {
				pendingToolCalls = append(pendingToolCalls, choice.Delta.ToolCalls...)
			}
		})

		if err != nil {
			for i := 0; i < 3; i++ {
				select {
				case <-ctx.Done():
					task.StatusState = "failed"
					task.Output = "Stopped by user"
					te.store.UpdateTask(task)
					return
				case <-time.After(time.Duration(i+1) * 2 * time.Second):
				}
				usage, err = client.ChatStream(messages, tools, func(choice ChatChoice) {
					if choice.Delta.Content != nil {
						b.send(StreamEvent{Type: WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
					}
				})
				if err == nil {
					break
				}
			}
			if err != nil {
				task.StatusState = "paused"
				task.Output = fmt.Sprintf("Connection failed: %v", err)
				te.store.UpdateTask(task)
				b.send(StreamEvent{Type: WSTypeError, Data: map[string]string{"message": err.Error(), "code": "connection_failed"}})
				return
			}
		}

		assistantText := assistantContent.String()

		if len(pendingToolCalls) > 0 {
			for _, tc := range pendingToolCalls {
				args := parseToolArgs(tc.Function.Arguments)
				result, success := te.executeTool(tc.Function.Name, args, worktreePath, task)
				b.send(StreamEvent{Type: WSTypeToolCall, Data: map[string]interface{}{"tool_id": tc.ID, "tool_name": tc.Function.Name, "args": args}})
				b.send(StreamEvent{Type: WSTypeToolResult, Data: map[string]interface{}{"tool_id": tc.ID, "success": success, "output": result, "duration": time.Since(startTime).Milliseconds()}})

				if !success {
					consecutiveFailures++
					if consecutiveFailures >= maxFailures {
						task.StatusState = "failed"
						task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
						te.store.UpdateTask(task)
						b.send(StreamEvent{Type: WSTypeError, Data: map[string]string{"message": "Max failures", "code": "max_failures"}})
						return
					}
				} else {
					consecutiveFailures = 0
				}

				if tc.Function.Name == "task_complete" {
					summary := args["summary"].(string)
					if task.AutoCommit {
						te.gitRun(worktreePath, "add", "-A")
						te.gitRun(worktreePath, "commit", "-m", summary)
						task.StatusState = "completed"
						task.Status = "done"
					} else {
						task.StatusState = "awaiting_review"
						task.Status = "review"
					}
					task.Output = summary
					te.store.UpdateTask(task)
					b.send(StreamEvent{Type: WSTypeComplete, Data: map[string]interface{}{"state": task.StatusState, "summary": summary, "tokens": usage, "duration": time.Since(startTime).Milliseconds()}})
					return
				}
			}
			messages = append(messages, ChatMessage{Role: "assistant", Content: assistantText})
			messages = append(messages, ChatMessage{Role: "tool", Content: fmt.Sprintf("Tool results for %d calls", len(pendingToolCalls))})
			pendingToolCalls = nil
			assistantContent.Reset()
		} else if assistantText != "" {
			messages = append(messages, ChatMessage{Role: "assistant", Content: assistantText})
			messages = append(messages, ChatMessage{Role: "user", Content: "Please continue working. Use the available tools to make progress."})
			assistantContent.Reset()
		}

		if len(messages) > 100 {
			task.StatusState = "failed"
			task.Output = "Conversation limit reached"
			te.store.UpdateTask(task)
			return
		}
	}
}

func (te *TaskExecutor) executeTool(name string, args map[string]interface{}, worktreePath string, task *Task) (string, bool) {
	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		fullPath := filepath.Join(worktreePath, filepath.Clean("/"+path))
		if !strings.HasPrefix(fullPath, worktreePath) {
			return "Error: path escape", false
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return string(data), true
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		fullPath := filepath.Join(worktreePath, filepath.Clean("/"+path))
		if !strings.HasPrefix(fullPath, worktreePath) {
			return "Error: path escape", false
		}
		os.MkdirAll(filepath.Dir(fullPath), 0o755)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return fmt.Sprintf("Wrote %d bytes", len(content)), true
	case "execute_command":
		cmd, _ := args["command"].(string)
		if isDangerousCommand(cmd) {
			return "Error: command blocked", false
		}
		timeout := int64(5)
		if t, ok := args["timeout"].(float64); ok {
			timeout = int64(t)
		}
		if timeout > 300 {
			timeout = 300
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "bash", "-c", cmd)
		c.Dir = worktreePath
		out, err := c.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("Failed: %v\n%s", err, out), false
		}
		return string(out), true
	case "task_complete":
		return "Task complete", true
	default:
		return fmt.Sprintf("Unknown tool: %s", name), false
	}
}

func (te *TaskExecutor) gitRun(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	_, err := c.CombinedOutput()
	return err
}

func parseToolArgs(s string) map[string]interface{} {
	result := make(map[string]interface{})
	json.Unmarshal([]byte(s), &result)
	return result
}

func isDangerousCommand(cmd string) bool {
	dangerous := []string{"sudo", "mkfs", "dd if=/dev/", ":(){ :|: };:", "chmod -R 777 /"}
	cmd = strings.TrimSpace(cmd)
	for _, d := range dangerous {
		if strings.Contains(cmd, d) {
			return true
		}
	}
	return strings.HasPrefix(cmd, "rm -rf /") || strings.HasPrefix(cmd, "rm -rf /*")
}