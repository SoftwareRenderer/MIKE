package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kanban/backend/types"

	"github.com/google/uuid"
)

// execFunc is a function that executes a command, either in a sandbox or directly.
type execFunc func(command string) (string, error)

// readFunc reads a file's contents. When sandboxed, runs via container exec.
type readFunc func(path string) ([]byte, error)

// writeFunc writes content to a file. When sandboxed, runs via container exec with stdin.
type writeFunc func(path, content string) error

func (te *TaskExecutor) executeTool(name string, args map[string]interface{}, worktreePath string, task *types.Task, execFn execFunc, readFn readFunc, writeFn writeFunc) (string, bool) {
	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		fullPath := validateWorktreePath(worktreePath, path)
		if fullPath == "" {
			return "Error: path escape", false
		}
		var data []byte
		var err error
		if readFn != nil {
			data, err = readFn(path)
		} else {
			data, err = os.ReadFile(fullPath)
		}
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		content := string(data)
		// Apply offset/limit if provided
		if offset, ok := args["offset"].(float64); ok && int(offset) > 0 {
			lines := strings.Split(content, "\n")
			offsetIdx := int(offset) - 1 // 1-indexed to 0-indexed
			if offsetIdx >= len(lines) {
				return "(empty — offset past end of file)", true
			}
			lines = lines[offsetIdx:]
			if limit, ok := args["limit"].(float64); ok && int(limit) > 0 && int(limit) < len(lines) {
				lines = lines[:int(limit)]
			}
			content = strings.Join(lines, "\n")
		} else if limit, ok := args["limit"].(float64); ok && int(limit) > 0 {
			// limit without offset: take first N lines
			lines := strings.Split(content, "\n")
			if int(limit) < len(lines) {
				lines = lines[:int(limit)]
			}
			content = strings.Join(lines, "\n")
		}
		return content, true
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		fullPath := validateWorktreePath(worktreePath, path)
		if fullPath == "" {
			return "Error: path escape", false
		}
		var err error
		if writeFn != nil {
			err = writeFn(path, content)
		} else {
			os.MkdirAll(filepath.Dir(fullPath), 0o755)
			err = os.WriteFile(fullPath, []byte(content), 0o644)
		}
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return fmt.Sprintf("Wrote %d bytes", len(content)), true
	case "edit_file":
		path, _ := args["path"].(string)
		fullPath := validateWorktreePath(worktreePath, path)
		if fullPath == "" {
			return "Error: path escape", false
		}
		editsRaw, ok := args["edits"].(json.RawMessage)
		if !ok {
			// Try to marshal from the interface{}
			editsRaw, _ = json.Marshal(args["edits"])
		}
		var edits []struct {
			OldText string `json:"oldText"`
			NewText string `json:"newText"`
		}
		if err := json.Unmarshal(editsRaw, &edits); err != nil {
			return fmt.Sprintf("Error: failed to parse edits: %v", err), false
		}
		if len(edits) == 0 {
			return "Error: no edits provided", false
		}

		// Read current file content
		var fileContent string
		if readFn != nil {
			data, err := readFn(path)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), false
			}
			fileContent = string(data)
		} else {
			data, err := os.ReadFile(fullPath)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), false
			}
			fileContent = string(data)
		}

		// Find all oldText positions in the original content
		type editPos struct {
			index   int
			start   int
			end     int
			newText string
		}
		var positions []editPos
		for i, e := range edits {
			if e.OldText == "" {
				return fmt.Sprintf("Error: edits[%d].oldText is empty", i), false
			}
			idx := strings.Index(fileContent, e.OldText)
			if idx == -1 {
				// Show context around where it might be expected
				return fmt.Sprintf("Error: edits[%d].oldText not found in file. Ensure it matches exactly (whitespace, indentation). Got %d lines in file.", i, len(strings.Split(fileContent, "\n"))), false
			}
			// Check for duplicate matches (oldText must be unique)
			secondIdx := strings.Index(fileContent[idx+1:], e.OldText)
			if secondIdx != -1 {
				return fmt.Sprintf("Error: edits[%d].oldText appears multiple times in file. Make it more specific by including surrounding context.", i), false
			}
			positions = append(positions, editPos{index: i, start: idx, end: idx + len(e.OldText), newText: e.NewText})
		}

		// Sort positions by start index for overlap check
		sort.Slice(positions, func(i, j int) bool {
			return positions[i].start < positions[j].start
		})

		// Check for overlapping edits
		for i := 0; i < len(positions)-1; i++ {
			if positions[i].end > positions[i+1].start {
				return fmt.Sprintf("Error: edits[%d] and edits[%d] overlap. Merge them into a single edit or ensure they don't overlap.", positions[i].index, positions[i+1].index), false
			}
		}

		// Apply edits in reverse order to preserve positions
		result := fileContent
		for i := len(positions) - 1; i >= 0; i-- {
			p := positions[i]
			result = result[:p.start] + p.newText + result[p.end:]
		}

		// Write the result
		if writeFn != nil {
			err := writeFn(path, result)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), false
			}
		} else {
			os.MkdirAll(filepath.Dir(fullPath), 0o755)
			err := os.WriteFile(fullPath, []byte(result), 0o644)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), false
			}
		}
		return fmt.Sprintf("Edited %s (%d edit(s) applied)", path, len(edits)), true
	case "bash":
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

		var out string
		var err error
		if execFn != nil {
			// Use sandbox execution
			out, err = execFn(cmd)
		} else {
			// Direct execution (no sandbox)
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, "bash", "-c", cmd)
			c.Dir = worktreePath
			rawOut, cmdErr := c.CombinedOutput()
			out = string(rawOut)
			err = cmdErr
		}

		if err != nil {
			return fmt.Sprintf("Failed: %v\n%s", err, out), false
		}
		return out, true
	case "task_complete":
		return "Task complete", true
	case "create_task":
		return te.handleCreateTask(args, task), true
	default:
		return fmt.Sprintf("Unknown tool: %s", name), false
	}
}

func parseToolArgs(s string) map[string]interface{} {
	result := make(map[string]interface{})
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" {
		return result
	}
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		var innerStr string
		if err2 := json.Unmarshal([]byte(s), &innerStr); err2 == nil && innerStr != "" {
			if err3 := json.Unmarshal([]byte(innerStr), &result); err3 == nil {
				return result
			}
		}
		if len(s) > 1 && s[0] == '{' && s[1] == '{' {
			trimmed := s[1:]
			if err4 := json.Unmarshal([]byte(trimmed), &result); err4 == nil {
				return result
			}
		}
		log.Printf("[tool] failed to parse args: %v (raw: %q)", err, s)
		return map[string]interface{}{"raw": s}
	}
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

// validateWorktreePath resolves a relative path against the worktree root
// and returns the absolute path, or empty string if it escapes the worktree.
// Handles: .. traversal, absolute paths, and symlink-style escapes.
func validateWorktreePath(worktreePath, path string) string {
	// Ensure worktree path is absolute and has trailing separator for prefix check
	wt := worktreePath
	if !filepath.IsAbs(wt) {
		return ""
	}
	if !strings.HasSuffix(wt, string(filepath.Separator)) {
		wt = wt + string(filepath.Separator)
	}

	// Clean the input path
	cleaned := filepath.Clean(path)
	// Reject absolute paths (they shouldn't be used for worktree-relative paths)
	if filepath.IsAbs(cleaned) {
		return ""
	}
	// Reject paths that escape with ..
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}

	fullPath := filepath.Join(worktreePath, cleaned)
	// Final safety check: resolved path must be within worktree
	// (exact match or starts with worktree + separator)
	if fullPath != worktreePath && !strings.HasPrefix(fullPath, wt) {
		return ""
	}
	return fullPath
}

// handleCreateTask creates a new task on the board, used by the LLM in plan mode
// to decompose work into subtasks. The new task inherits settings from the parent.
func (te *TaskExecutor) handleCreateTask(args map[string]interface{}, parent *types.Task) string {
	title, _ := args["title"].(string)
	prompt, _ := args["prompt"].(string)
	parentTaskID, _ := args["parent_task_id"].(string)

	if title == "" || prompt == "" {
		return "Error: title and prompt are required"
	}

	// Determine the actual parent: if parent_task_id is provided, use it (nested subtask).
	// Otherwise, nest under the current executing task.
	actualParentID := parentTaskID
	if actualParentID == "" {
		actualParentID = parent.ID
	}

	// Validate that the parent exists and is in backlog (can accept subtasks)
	parentTask, err := te.store.GetTaskByTaskID(actualParentID)
	if err != nil {
		return fmt.Sprintf("Error: parent task %s not found", actualParentID)
	}
	if parentTask.Status != "backlog" && parentTask.Status != "in_progress" {
		return fmt.Sprintf("Error: parent task %s is not in backlog or in_progress (status: %s)", actualParentID, parentTask.Status)
	}

	// Create the new task
	now := time.Now().UTC()
	newTaskID := types.GenerateTaskID()
	// Ensure uniqueness (unlikely collision, but guard against it)
	for {
		if _, err := te.store.GetTaskByTaskID(newTaskID); err != nil {
			break // unique
		}
		newTaskID = types.GenerateTaskID()
	}

	newTask := &types.Task{
		ID:                      newTaskID,
		ProjectID:               parent.ProjectID,
		Title:                   title,
		Prompt:                  prompt,
		Status:                  "backlog",
		StatusState:             "idle",
		LlamaServerURL:          parent.LlamaServerURL,
		ModelName:               parent.ModelName,
		ReasoningEffort:         parent.ReasoningEffort,
		MaxConsecutiveFailedTurns: parent.MaxConsecutiveFailedTurns,
		AutoCommit:              parent.AutoCommit,
		AutoPR:                  parent.AutoPR,
		PlanMode:                parent.PlanMode,
		ParentTaskID:            actualParentID,
		CreatedAt:               now,
		UpdatedAt:               now,
	}

	if err := te.store.CreateTask(newTask); err != nil {
		return fmt.Sprintf("Error: failed to create task: %v", err)
	}

	// Create default "Main" thread for the new task
	defaultThreadID := types.GenerateThreadID()
	if err := te.store.CreateThread(&types.TaskThread{
		ID:        defaultThreadID,
		TaskID:    newTaskID,
		Name:      "Main",
		IsActive:  true,
		CreatedAt: now,
	}); err != nil {
		return fmt.Sprintf("Error: failed to create thread: %v", err)
	}
	newTask.CurrentThreadID = defaultThreadID
	_ = te.store.UpdateTask(newTask)

	// Store the prompt as the first user message
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    newTaskID,
		ThreadID:  defaultThreadID,
		Role:      "user",
		Content:   prompt,
		CreatedAt: now,
	}); err != nil {
		log.Printf("[create_task] task=%s failed to store prompt message: %v", newTaskID, err)
	}

	// Broadcast the new task to the frontend
	te.boardStreamMgr.BroadcastTaskUpdated(parent.ProjectID, *newTask)

	log.Printf("[create_task] task=%s created subtask %s (parent=%s): %q", parent.ID, newTaskID, actualParentID, title)

	// Return JSON with task_id so the LLM can reference it for nested subtasks
	result, _ := json.Marshal(map[string]string{"task_id": newTaskID, "title": title})
	return string(result)
}
