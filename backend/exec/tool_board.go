package exec

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mike/backend/llama"
	"mike/backend/types"

	"github.com/google/uuid"
	"mike/pkg/logging"
)

// The task-board tools: what create_task, view_tasks and edit_task do, and the plan-mode scan that
// decides whether request_review may end a planning turn. They reach a task's createdIDs through the
// executionState the call was dispatched with, exactly as the dispatcher's own handlers do.

// handleCreateTask creates a new task on the board, used by the LLM in plan mode
// to decompose work into subtasks. The new task inherits settings from the parent.
func (r *toolRunner) handleCreateTask(args map[string]any, parent *types.Task, state *executionState) (string, error) {
	title, _ := args["title"].(string)
	prompt, _ := args["prompt"].(string)
	parentTaskID, _ := args["parent_task_id"].(string)

	if title == "" || prompt == "" {
		return "", errors.New("title and prompt are required")
	}

	// Determine the actual parent: if parent_task_id is provided, use it (nested subtask).
	// Otherwise, create as an independent task (no parent).
	actualParentID := parentTaskID
	var parentTask *types.Task
	if actualParentID != "" {
		var err error
		parentTask, err = r.store.GetTaskByID(actualParentID)
		if err != nil {
			return "", fmt.Errorf("parent task %s not found", actualParentID)
		}
	}

	// Create the new task
	now := time.Now().UTC()
	// Same rule the task API applies (types.SubtaskStatusFor).
	newTaskStatus := types.StatusBacklog
	if parentTask != nil {
		newTaskStatus = types.SubtaskStatusFor(parentTask.Status)
	}
	newTaskID := types.GenerateTaskID()
	// Ensure uniqueness (unlikely collision, but guard against it)
	for {
		if _, err := r.store.GetTaskByID(newTaskID); err != nil {
			break // unique
		}
		newTaskID = types.GenerateTaskID()
	}

	newTask := &types.Task{
		ID:                        newTaskID,
		ProjectID:                 parent.ProjectID,
		Title:                     title,
		Prompt:                    prompt,
		Status:                    newTaskStatus,
		ExecutionState:            types.ExecStateIdle,
		LlamaServerURL:            parent.LlamaServerURL,
		ModelName:                 parent.ModelName,
		ReasoningEffort:           parent.ReasoningEffort,
		MaxConsecutiveFailedTurns: parent.MaxConsecutiveFailedTurns,
		OperatingMode:             types.ModeAct,
		ParentTaskID:              actualParentID,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}

	// A created root task is one the person could have created themselves, so it inherits the
	// originating task's base branch. A subtask has none to give, and the name must be one git accepts
	// because mergeInto creates a branch by it when it is missing.
	if actualParentID == "" && parent.ParentTaskID == "" && types.ValidateBranchName(parent.BaseRef) == nil {
		newTask.BaseRef = parent.BaseRef
	}

	// Apply config defaults + parent inheritance (same logic as UI task creation)
	if r.configStore != nil {
		if cfg, err := r.configStore.GetConfig(); err == nil {
			types.ApplyTaskCreationDefaults(newTask, cfg, parentTask, nil)
		}
	}

	if err := r.store.CreateTask(newTask); err != nil {
		return "", fmt.Errorf("failed to create task: %w", err)
	}

	// Track the created task ID for plan mode validation
	if state != nil {
		state.createdIDs = append(state.createdIDs, newTaskID)
	}

	// Create default "Main" thread for the new task
	defaultThreadID := types.GenerateThreadID()
	// Ensure uniqueness
	for {
		if _, err := r.store.GetThread(defaultThreadID); err != nil {
			break // unique
		}
		defaultThreadID = types.GenerateThreadID()
	}
	if err := r.store.CreateThread(&types.TaskThread{
		ID:        defaultThreadID,
		TaskID:    newTaskID,
		Name:      "Main",
		IsActive:  true,
		CreatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("failed to create thread: %w", err)
	}
	newTask.CurrentThreadID = defaultThreadID
	_ = r.storeTask(newTask, newTask.ProjectID)

	// Store the prompt as the first user message
	if err := r.store.CreateTaskMessage(&types.TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    newTaskID,
		ThreadID:  defaultThreadID,
		Role:      "user",
		Content:   prompt,
		CreatedAt: now,
	}); err != nil {
		logging.For(logging.Tools).With(logging.TaskID, newTaskID).Warnf("failed to store prompt message: %v", err)
	}

	// Broadcast the new task to the frontend
	r.appStreamMgr.BroadcastTaskUpdated(parent.ProjectID, newTask)

	if actualParentID != "" {
		logging.For(logging.Tools).With(logging.TaskID, parent.ID).Infof("created subtask %s (parent=%s): %q", newTaskID, actualParentID, title)
	} else {
		logging.For(logging.Tools).With(logging.TaskID, parent.ID).Infof("created task %s: %q", newTaskID, title)
	}

	// Return JSON with task_id so the LLM can reference it for nested subtasks
	result, _ := json.Marshal(map[string]string{"task_id": newTaskID, "title": title})
	return string(result), nil
}

// planCreatedTaskIDs returns the IDs successful create_task calls in task
func (r *toolRunner) planCreatedTaskIDs(task *types.Task, state *executionState) []string {
	seen := make(map[string]bool)
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if state != nil {
		for _, id := range state.createdIDs {
			add(id)
		}
	}
	// threadID "" returns all messages for the task (all threads + legacy).
	msgs, err := r.store.GetTaskMessages(task.ID, "")
	if err != nil {
		logging.For(logging.Exec).With(logging.TaskID, task.ID).Warnf("history scan failed: %v", err)
		return ids
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Role != "tool_call" || m.ToolCalls == "" || m.ToolResult == "" {
			continue
		}
		var tc struct {
			ToolName string `json:"tool_name"`
		}
		if err := json.Unmarshal([]byte(m.ToolCalls), &tc); err != nil || tc.ToolName != llama.ToolCreateTask {
			continue
		}
		var res struct {
			Output  string `json:"output"`
			Success bool   `json:"success"`
		}
		if err := json.Unmarshal([]byte(m.ToolResult), &res); err != nil || !res.Success {
			continue
		}
		var out struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			continue
		}
		add(out.TaskID)
	}
	return ids
}

// handleViewTasks returns all backlog tasks for the current project.
// Returns a JSON array with id, title, prompt, and parent_task_id for each task.
func (r *toolRunner) handleViewTasks(currentTask *types.Task) (string, error) {
	tasks, err := r.store.GetTasksByProject(currentTask.ProjectID)
	if err != nil {
		return "", fmt.Errorf("failed to list tasks: %w", err)
	}

	type taskSummary struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Prompt       string `json:"prompt"`
		ParentTaskID string `json:"parent_task_id"`
	}

	var summaries []taskSummary
	for _, t := range tasks {
		if t.Status == types.StatusBacklog {
			summaries = append(summaries, taskSummary{
				ID:           t.ID,
				Title:        t.Title,
				Prompt:       t.Prompt,
				ParentTaskID: t.ParentTaskID,
			})
		}
	}

	if summaries == nil {
		summaries = []taskSummary{}
	}

	data, err := json.Marshal(summaries)
	if err != nil {
		return "", fmt.Errorf("failed to marshal tasks: %w", err)
	}

	logging.For(logging.Tools).With(logging.TaskID, currentTask.ID).Infof("listed %d backlog task(s) in project %s", len(summaries), currentTask.ProjectID)
	return string(data), nil
}

// handleEditTask edits title, prompt, or parent_task_id of a backlog task.
// Only tasks with status 'backlog' in the current project can be edited.
func (r *toolRunner) handleEditTask(args map[string]any, currentTask *types.Task) (string, error) {
	taskID, _ := args["task_id"].(string)
	if taskID == "" {
		return "", errors.New("task_id is required")
	}

	title, hasTitle := args["title"].(string)
	prompt, hasPrompt := args["prompt"].(string)
	parentTaskID, hasParent := args["parent_task_id"].(string)

	if !hasTitle && !hasPrompt && !hasParent {
		return "", errors.New("at least one of title, prompt, or parent_task_id must be provided")
	}

	target, err := r.store.GetTaskByID(taskID)
	if err != nil {
		return "", fmt.Errorf("task %s not found", taskID)
	}

	// Must belong to the same project
	if target.ProjectID != currentTask.ProjectID {
		return "", fmt.Errorf("task %s does not belong to the current project", taskID)
	}

	// Must be in backlog status
	if target.Status != types.StatusBacklog {
		return "", fmt.Errorf("task %s is not in backlog (status: %s). Only backlog tasks can be edited", taskID, target.Status)
	}

	// If changing parent, validate the parent exists; any status may have subtasks.
	// The target above stays backlog-only: editing started work would rewrite a run.
	if hasParent {
		if parentTaskID != "" {
			parent, err := r.store.GetTaskByID(parentTaskID)
			if err != nil {
				return "", fmt.Errorf("parent task %s not found", parentTaskID)
			}
			// Only re-derive auto-merge when the parent actually changes
			if target.ParentTaskID != parentTaskID {
				target.ParentTaskID = parentTaskID
				// Respect the parent's "allow subtask to auto merge" preference
				r.applyAutoMergeOnParentChange(target, parent)
			}
		} else if target.ParentTaskID != "" {
			// Empty string means remove parent — reset auto-merge to config defaults.
			target.ParentTaskID = ""
			r.applyAutoMergeOnParentChange(target, nil)
		}
	}
	types.ClearSubtaskBaseBranch(target)

	if hasTitle {
		target.Title = title
	}
	if hasPrompt {
		target.Prompt = prompt
	}

	if err := r.store.UpdateTask(target); err != nil {
		return "", fmt.Errorf("failed to update task: %w", err)
	}

	// Broadcast the updated task to the frontend
	r.appStreamMgr.BroadcastTaskUpdated(currentTask.ProjectID, target)

	logging.For(logging.Tools).With(logging.TaskID, currentTask.ID).Infof("edited task %s: %q", taskID, target.Title)

	result, _ := json.Marshal(map[string]string{"task_id": taskID, "title": target.Title, "status": "updated"})
	return string(result), nil
}

// applyAutoMergeOnParentChange re-derives auto_merge/auto_commit preferences
func (r *toolRunner) applyAutoMergeOnParentChange(target *types.Task, parent *types.Task) {
	var cfg *types.Config
	if r.configStore != nil {
		if c, err := r.configStore.GetConfig(); err == nil {
			cfg = c
		}
	}
	types.ApplyTaskCreationDefaults(target, cfg, parent, map[string]struct{}{"subtasks_auto_merge": {}})
}
