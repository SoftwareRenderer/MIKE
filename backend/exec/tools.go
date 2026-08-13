package exec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kanban/backend/git"
	"kanban/backend/types"
	sandbox "kanban/pkg/bridge"

	"github.com/google/uuid"
)

// execFunc runs a shell command. Provided by the sandbox in container mode.
type execFunc func(ctx context.Context, command string, timeout int64) (string, error)

// execStreamFunc runs a shell command with streaming output.
// outputCallback is called for each chunk of output as it arrives.
type execStreamFunc func(ctx context.Context, command string, timeout int64, outputCallback func(text string)) (string, error)

// readFunc reads a file. Provided by the sandbox in container mode.
type readFunc func(ctx context.Context, path string) ([]byte, error)

// writeFunc writes a file. Provided by the sandbox in container mode.
type writeFunc func(ctx context.Context, path, content string) error

// makeSandboxCallbacks creates exec/read/write callbacks that route through
// the sandbox agent client inside a container.
func makeSandboxCallbacks(client *sandbox.Client) (execFunc, execStreamFunc, readFunc, writeFunc) {
	execCb := func(ctx context.Context, command string, timeout int64) (string, error) {
		holder, err := client.Bash(ctx, command, timeout)
		if err != nil {
			return "", fmt.Errorf("bash: %w", err)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(holder.Output())
		exitCode := <-holder.ExitCode()
		if exitCode != 0 {
			return buf.String(), fmt.Errorf("exit code %d", exitCode)
		}
		return buf.String(), nil
	}
	execStreamCb := func(ctx context.Context, command string, timeout int64, outputCallback func(text string)) (string, error) {
		holder, err := client.Bash(ctx, command, timeout)
		if err != nil {
			return "", fmt.Errorf("bash: %w", err)
		}
		var buf bytes.Buffer
		streamReader := holder.Output()
		scanner := bufio.NewScanner(streamReader)
		for scanner.Scan() {
			line := scanner.Text() + "\n"
			buf.WriteString(line)
			if outputCallback != nil {
				outputCallback(line)
			}
		}
		exitCode := <-holder.ExitCode()
		if exitCode != 0 {
			return buf.String(), fmt.Errorf("exit code %d", exitCode)
		}
		return buf.String(), nil
	}
	readCb := func(ctx context.Context, path string) ([]byte, error) {
		content, _, err := client.ReadFile(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		return []byte(content), nil
	}
	writeCb := func(ctx context.Context, path, content string) error {
		_, err := client.WriteFile(ctx, path, content)
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return nil
	}
	return execCb, execStreamCb, readCb, writeCb
}

// makeRootCallbacks creates read/write callbacks that use os.Root for folder isolation.
func makeRootCallbacks(root *os.Root, worktreePath string) (readFunc, writeFunc) {
	readCb := func(_ context.Context, path string) ([]byte, error) {
		f, err := root.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(f)
	}
	writeCb := func(_ context.Context, path, content string) error {
		dir := filepath.Dir(filepath.Join(worktreePath, path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		f, err := root.Create(path)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(content)
		closeErr := f.Close()
		if writeErr != nil {
			_ = root.Remove(path)
			return writeErr
		}
		if closeErr != nil {
			_ = root.Remove(path)
			return closeErr
		}
		return os.Chmod(filepath.Join(worktreePath, path), 0o644)
	}
	return readCb, writeCb
}

// executeTool dispatches a tool call to the appropriate handler.
// Sandbox callbacks are read from state.
// When nil, os.Root provides folder isolation on the host.
// ctx is used to cancel long-running operations (e.g., bash) when the task is stopped.
// outputCallback, if non-nil, is called with incremental output for streaming tools (e.g., bash).
func (te *TaskExecutor) executeTool(ctx context.Context, name string, args map[string]any, worktreePath string, task *types.Task, state *executionState, outputCallback func(toolID string, text string)) (string, bool) {
	result, ok := te.executeToolInner(ctx, name, args, worktreePath, task, state, outputCallback)
	// A successful tool call proves the sandbox bridge is healthy (or unused),
	// so clear the bridge-recovery failure latch.
	if ok && state != nil && state.recoverBridge != nil {
		te.taskMu.Lock()
		state.bridgeRecoveryAttempts = 0
		te.taskMu.Unlock()
	}
	return result, ok
}

func (te *TaskExecutor) executeToolInner(ctx context.Context, name string, args map[string]any, worktreePath string, task *types.Task, state *executionState, outputCallback func(toolID string, text string)) (string, bool) {
	sandboxExec := state.sandboxExec
	sandboxExecStream := state.sandboxExecStream
	sandboxRead := state.sandboxRead
	sandboxWrite := state.sandboxWrite

	// recoverBridge detects bridge errors and attempts automatic recovery.
	// Returns true if recovery succeeded and callbacks were refreshed.
	// The caller should re-read sandboxExec/Stream/Read/Write/Client from state.
	recoverBridge := func(errStr string) bool {
		if !strings.Contains(errStr, "broken pipe") &&
			!strings.Contains(errStr, "client closed") &&
			!strings.Contains(errStr, "connection closed") {
			return false
		}
		if state.recoverBridge == nil {
			return false
		}
		return state.recoverBridge()
	}

	// Lazy os.Root fallback (only used when sandbox callbacks are nil).
	// In production, loop.go always provides sandboxWrite via os.Root.
	// Read uses plain os.ReadFile (no isolation) when sandbox is disabled.
	makeRead := func() readFunc {
		return func(_ context.Context, p string) ([]byte, error) {
			norm := normalizePath(state.projectName, p)
			if !filepath.IsAbs(norm) {
				norm = filepath.Join(worktreePath, norm)
			}
			return os.ReadFile(norm)
		}
	}
	makeWrite := func() writeFunc {
		// Use existing root from state, or create one lazily.
		var root *os.Root
		te.taskMu.Lock()
		if state.root != nil {
			root = state.root
		} else {
			r, err := os.OpenRoot(worktreePath)
			if err != nil {
				te.taskMu.Unlock()
				return func(_ context.Context, p, c string) error { return fmt.Errorf("open root: %w", err) }
			}
			state.root = r
			root = r
		}
		te.taskMu.Unlock()
		return func(_ context.Context, p, c string) error {
			norm := normalizePath(state.projectName, p)
			dir := filepath.Dir(filepath.Join(worktreePath, norm))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			f, err := root.Create(norm)
			if err != nil {
				return err
			}
			_, writeErr := f.WriteString(c)
			closeErr := f.Close()
			if writeErr != nil {
				_ = root.Remove(norm)
				return writeErr
			}
			if closeErr != nil {
				_ = root.Remove(norm)
				return closeErr
			}
			return os.Chmod(filepath.Join(worktreePath, norm), 0o644)
		}
	}

	switch name {
	case "read_file":
		path, _ := args["path"].(string)
		read := sandboxRead
		if read == nil {
			read = makeRead()
		}
		data, err := read(ctx, path)
		if err != nil {
			if recoverBridge(err.Error()) {
				read = state.sandboxRead
				if read == nil {
					read = makeRead()
				}
				data, err = read(ctx, path)
				if err != nil {
					return fmt.Sprintf("Error: %v", err), false
				}
			} else {
				return fmt.Sprintf("Error: %v", err), false
			}
		}
		content := string(data)
		// Apply offset/limit first so the LLM can read any region of large files.
		if offset, ok := args["offset"].(float64); ok && int(offset) > 0 {
			lines := strings.Split(content, "\n")
			offsetIdx := int(offset) - 1 // 1-indexed to 0-indexed
			if offsetIdx >= len(lines) {
				// Invalid offset: surface an explicit error so the LLM is made aware
				// and can correct its request, instead of silently returning empty.
				return fmt.Sprintf("Error: offset %d is past the end of the file (file has %d line(s)). Use a valid offset.", int(offset), len(lines)), false
			}
			lines = lines[offsetIdx:]
			// Valid offset: silently clamp an oversized limit to the remaining lines.
			if limit, ok := args["limit"].(float64); ok && int(limit) > 0 && int(limit) < len(lines) {
				lines = lines[:int(limit)]
			}
			content = strings.Join(lines, "\n")
		} else if limit, ok := args["limit"].(float64); ok && int(limit) > 0 {
			// limit without offset: take first N lines, silently clamping an oversized limit.
			lines := strings.Split(content, "\n")
			if int(limit) < len(lines) {
				lines = lines[:int(limit)]
			}
			content = strings.Join(lines, "\n")
		}
		// Truncate result to prevent excessive memory usage in WebSocket frames.
		// The LLM doesn't need the full content of huge files; the truncation notice
		// informs it that more content exists beyond what was returned.
		const maxReadSize = 50 * 1024 // 50KB
		if len(content) > maxReadSize {
			content = content[:maxReadSize] + "\n\n... (file truncated at 50KB, use offset/limit parameters to read remaining lines) ..."
		}
		return content, true
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		write := sandboxWrite
		if write == nil {
			write = makeWrite()
		}
		err := write(ctx, path, content)
		if err != nil {
			if recoverBridge(err.Error()) {
				write = state.sandboxWrite
				if write == nil {
					write = makeWrite()
				}
				err = write(ctx, path, content)
				if err != nil {
					return fmt.Sprintf("Error: %v", err), false
				}
			} else {
				return fmt.Sprintf("Error: %v", err), false
			}
		}
		return fmt.Sprintf("Wrote %d bytes to %s", len(content), path), true
	case "edit_file":
		path, _ := args["path"].(string)
		read := sandboxRead
		write := sandboxWrite
		if read == nil {
			read = makeRead()
		}
		if write == nil {
			write = makeWrite()
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
		data, err := read(ctx, path)
		if err != nil {
			if recoverBridge(err.Error()) {
				read = state.sandboxRead
				if read == nil {
					read = makeRead()
				}
				data, err = read(ctx, path)
				if err != nil {
					return fmt.Sprintf("Error: %v", err), false
				}
			} else {
				return fmt.Sprintf("Error: %v", err), false
			}
		}
		fileContent := string(data)

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
		if err := write(ctx, path, result); err != nil {
			if recoverBridge(err.Error()) {
				write = state.sandboxWrite
				if write == nil {
					write = makeWrite()
				}
				if err := write(ctx, path, result); err != nil {
					return fmt.Sprintf("Error: %v", err), false
				}
			} else {
				return fmt.Sprintf("Error: %v", err), false
			}
		}
		return fmt.Sprintf("Edited %s (%d edit(s) applied)", path, len(edits)), true
	case "bash":
		cmd, _ := args["command"].(string)
		timeout := int64(5)
		if t, ok := args["timeout"].(float64); ok {
			timeout = int64(t)
		}
		if timeout > 300 {
			timeout = 300
		}

		var out string
		var err error

		// Run sandbox bash with automatic bridge recovery + retry.
		// Bridge errors mean the command never reached the agent, so retry is safe.
		runBash := func() (string, error) {
			if sandboxExecStream != nil && outputCallback != nil {
				// Use streaming sandbox execution — output is streamed line by line
				return sandboxExecStream(ctx, cmd, timeout, func(text string) {
					outputCallback(task.ID, text)
				})
			} else if sandboxExec != nil {
				// Use sandbox execution (non-streaming fallback) — ctx propagates task cancellation
				return sandboxExec(ctx, cmd, timeout)
			} else {
				// Direct execution (no sandbox) with streaming output.
				c := exec.CommandContext(ctx, "bash", "-c", cmd)
				c.Dir = worktreePath
				stdout, pipeErr := c.StdoutPipe()
				if pipeErr != nil {
					return "", fmt.Errorf("failed: %v", pipeErr)
				}
				stderr, pipeErr := c.StderrPipe()
				if pipeErr != nil {
					return "", fmt.Errorf("failed: %v", pipeErr)
				}
				if startErr := c.Start(); startErr != nil {
					return "", fmt.Errorf("failed: %v", startErr)
				}
				// Read stdout and stderr concurrently, stream output line by line.
				var buf bytes.Buffer
				var bufMu sync.Mutex // protect concurrent writes from stdout/stderr goroutines
				var scanErr error
				var scanMu sync.Mutex
				var wg sync.WaitGroup
				stream := outputCallback != nil
				readStream := func(r io.Reader) {
					defer wg.Done()
					scanner := bufio.NewScanner(r)
					for scanner.Scan() {
						line := scanner.Text() + "\n"
						bufMu.Lock()
						buf.WriteString(line)
						bufMu.Unlock()
						if stream {
							outputCallback(task.ID, line)
						}
					}
					if err := scanner.Err(); err != nil {
						scanMu.Lock()
						if scanErr == nil {
							scanErr = fmt.Errorf("scan output: %w", err)
						}
						scanMu.Unlock()
					}
				}
				wg.Add(2)
				go readStream(stdout)
				go readStream(stderr)
				waitErr := c.Wait()
				wg.Wait()
				if scanErr != nil {
					return fmt.Sprintf("Failed: %v\n%s", scanErr, buf.String()), waitErr
				}
				return buf.String(), waitErr
			}
		}

		out, err = runBash()
		if err != nil && recoverBridge(err.Error()) {
			// Bridge recovered — refresh callbacks and retry
			sandboxExec = state.sandboxExec
			sandboxExecStream = state.sandboxExecStream
			out, err = runBash()
		}

		// Strip ANSI escape codes from output so the LLM sees clean text
		// and the frontend displays without garbled sequences.
		out = stripANSI(out)

		if err != nil {
			return fmt.Sprintf("Failed: %v\n%s", err, out), false
		}
		// Truncate large outputs to prevent excessive memory usage in WebSocket frames.
		// The truncation notice informs the LLM that more output exists.
		const maxOutputSize = 50 * 1024 // 50KB
		if len(out) > maxOutputSize {
			out = out[:maxOutputSize] + "\n\n... (output truncated at 50KB) ..."
		}
		return out, true
	case "ask_question":
		question, _ := args["question"].(string)
		if question == "" {
			return "Error: question is required", false
		}
		return question, true
	case "request_review":
		// In plan mode, require at least one task created during planning before allowing completion
		if task.IsPlanMode() {
			if state == nil {
				return "Error: Cannot complete planning without creating tasks. Use create_task() to add at least one task first.", false
			}
			createdIDs := state.createdIDs
			if len(createdIDs) == 0 {
				return "Error: Cannot complete planning without creating tasks. Use create_task() to add at least one task first.", false
			}
			// Count independent tasks vs nested subtasks
			independentCount := 0
			nestedCount := 0
			for _, id := range createdIDs {
				t, err := te.store.GetTaskByTaskID(id)
				if err != nil {
					continue
				}
				if t.ParentTaskID == "" {
					independentCount++
				} else {
					nestedCount++
				}
			}
			// Build accurate summary
			var summary string
			if independentCount > 0 && nestedCount > 0 {
				summary = fmt.Sprintf("Plan created with %d task%s and %d subtask%s. Awaiting approval.",
					independentCount, git.PluralS(independentCount), nestedCount, git.PluralS(nestedCount))
			} else if independentCount > 0 {
				summary = fmt.Sprintf("Plan created with %d task%s. Awaiting approval.",
					independentCount, git.PluralS(independentCount))
			} else {
				summary = fmt.Sprintf("Plan created with %d subtask%s. Awaiting approval.",
					nestedCount, git.PluralS(nestedCount))
			}
			log.Printf("[request_review] plan mode: %s", summary)
		}
		return "Task complete", true
	case "create_task":
		result, err := te.handleCreateTask(args, task, state)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return result, true
	case "view_tasks":
		result, err := te.handleViewTasks(task)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return result, true
	case "edit_task":
		result, err := te.handleEditTask(args, task)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return result, true
	case "lsp_references":
		result, err := te.handleLSPSymbolRequest(args, worktreePath, "textDocument/references", map[string]any{"includeDeclaration": true})
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return result, true
	case "lsp_definition":
		result, err := te.handleLSPSymbolRequest(args, worktreePath, "textDocument/definition", nil)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return result, true
	case "lsp_code_actions":
		result, err := te.handleLSPSymbolRequest(args, worktreePath, "textDocument/codeAction", nil)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return result, true
	case "lsp_hover":
		result, err := te.handleLSPHover(args, worktreePath)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return result, true
	default:
		return fmt.Sprintf("Unknown tool: %s", name), false
	}
}

func parseToolArgs(s string) map[string]any {
	result := make(map[string]any)
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
		// Don't log for clearly incomplete (streaming) JSON — e.g. just "{" or "{"path":"
		if len(s) >= 1 && s[0] == '{' && s[len(s)-1] != '}' {
			return result
		}
		log.Printf("[tool] failed to parse args: %v (raw: %q)", err, s)
		return map[string]any{"raw": s}
	}
	return result
}

// normalizePath strips the sandbox mount prefix from absolute paths.
// Relative paths are returned as-is (os.Root handles traversal prevention).
func normalizePath(projectName, path string) string {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return cleaned
	}
	// Strip sandbox mount prefix: /home/sandbox/<projectName>/...
	mountPath := "/home/sandbox/" + projectName
	mountPrefix := mountPath + "/"
	if cleaned == mountPath {
		return "."
	}
	if strings.HasPrefix(cleaned, mountPrefix) {
		rel := cleaned[len(mountPath):]
		if len(rel) > 0 && rel[0] == os.PathSeparator {
			rel = rel[1:]
		}
		return rel
	}
	// Other absolute paths — os.Root treats them as relative to root
	return cleaned
}

// handleCreateTask creates a new task on the board, used by the LLM in plan mode
// to decompose work into subtasks. The new task inherits settings from the parent.
func (te *TaskExecutor) handleCreateTask(args map[string]any, parent *types.Task, state *executionState) (string, error) {
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
		parentTask, err = te.store.GetTaskByTaskID(actualParentID)
		if err != nil {
			return "", fmt.Errorf("parent task %s not found", actualParentID)
		}
		if parentTask.Status != "backlog" && parentTask.Status != "in_progress" {
			return "", fmt.Errorf("parent task %s is not in backlog or in_progress (status: %s)", actualParentID, parentTask.Status)
		}
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
		ID:                        newTaskID,
		ProjectID:                 parent.ProjectID,
		Title:                     title,
		Prompt:                    prompt,
		Status:                    "backlog",
		ExecutionState:            "idle",
		LlamaServerURL:            parent.LlamaServerURL,
		ModelName:                 parent.ModelName,
		ReasoningEffort:           parent.ReasoningEffort,
		MaxConsecutiveFailedTurns: parent.MaxConsecutiveFailedTurns,
		OperatingMode:             "act",
		ParentTaskID:              actualParentID,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}

	// Apply config defaults + parent inheritance (same logic as UI task creation)
	if te.configStore != nil {
		if cfg, err := te.configStore.GetConfig(); err == nil {
			types.ApplyTaskCreationDefaults(newTask, cfg, parentTask, nil)
		}
	}

	if err := te.store.CreateTask(newTask); err != nil {
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
		if _, err := te.store.GetThread(defaultThreadID); err != nil {
			break // unique
		}
		defaultThreadID = types.GenerateThreadID()
	}
	if err := te.store.CreateThread(&types.TaskThread{
		ID:        defaultThreadID,
		TaskID:    newTaskID,
		Name:      "Main",
		IsActive:  true,
		CreatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("failed to create thread: %w", err)
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
	te.appStreamMgr.BroadcastTaskUpdated(parent.ProjectID, *newTask)

	if actualParentID != "" {
		log.Printf("[create_task] task=%s created subtask %s (parent=%s): %q", parent.ID, newTaskID, actualParentID, title)
	} else {
		log.Printf("[create_task] task=%s created task %s: %q", parent.ID, newTaskID, title)
	}

	// Return JSON with task_id so the LLM can reference it for nested subtasks
	result, _ := json.Marshal(map[string]string{"task_id": newTaskID, "title": title})
	return string(result), nil
}

// handleViewTasks returns all backlog tasks for the current project.
// Returns a JSON array with id, title, prompt, and parent_task_id for each task.
func (te *TaskExecutor) handleViewTasks(currentTask *types.Task) (string, error) {
	tasks, err := te.store.GetTasksByProject(currentTask.ProjectID)
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
		if t.Status == "backlog" {
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

	log.Printf("[view_tasks] task=%s listed %d backlog task(s) in project %s", currentTask.ID, len(summaries), currentTask.ProjectID)
	return string(data), nil
}

// handleEditTask edits title, prompt, or parent_task_id of a backlog task.
// Only tasks with status 'backlog' in the current project can be edited.
func (te *TaskExecutor) handleEditTask(args map[string]any, currentTask *types.Task) (string, error) {
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

	target, err := te.store.GetTaskByTaskID(taskID)
	if err != nil {
		return "", fmt.Errorf("task %s not found", taskID)
	}

	// Must belong to the same project
	if target.ProjectID != currentTask.ProjectID {
		return "", fmt.Errorf("task %s does not belong to the current project", taskID)
	}

	// Must be in backlog status
	if target.Status != "backlog" {
		return "", fmt.Errorf("task %s is not in backlog (status: %s). Only backlog tasks can be edited", taskID, target.Status)
	}

	// If changing parent, validate the parent exists and is also backlog
	if hasParent {
		if parentTaskID != "" {
			parent, err := te.store.GetTaskByTaskID(parentTaskID)
			if err != nil {
				return "", fmt.Errorf("parent task %s not found", parentTaskID)
			}
			if parent.Status != "backlog" {
				return "", fmt.Errorf("parent task %s is not in backlog (status: %s). Parent must be a backlog task", parentTaskID, parent.Status)
			}
			// Only re-derive auto-merge when the parent actually changes
			if target.ParentTaskID != parentTaskID {
				target.ParentTaskID = parentTaskID
				// Respect the parent's "allow subtask to auto merge" preference
				te.applyAutoMergeOnParentChange(target, parent)
			}
		} else if target.ParentTaskID != "" {
			// Empty string means remove parent — reset auto-merge to config defaults.
			target.ParentTaskID = ""
			te.applyAutoMergeOnParentChange(target, nil)
		}
	}

	if hasTitle {
		target.Title = title
	}
	if hasPrompt {
		target.Prompt = prompt
	}

	if err := te.store.UpdateTask(target); err != nil {
		return "", fmt.Errorf("failed to update task: %w", err)
	}

	// Broadcast the updated task to the frontend
	te.appStreamMgr.BroadcastTaskUpdated(currentTask.ProjectID, *target)

	log.Printf("[edit_task] task=%s edited task %s: %q", currentTask.ID, taskID, target.Title)

	result, _ := json.Marshal(map[string]string{"task_id": taskID, "title": target.Title, "status": "updated"})
	return string(result), nil
}

// applyAutoMergeOnParentChange re-derives auto_merge/auto_commit preferences
func (te *TaskExecutor) applyAutoMergeOnParentChange(target *types.Task, parent *types.Task) {
	var cfg *types.Config
	if te.configStore != nil {
		if c, err := te.configStore.GetConfig(); err == nil {
			cfg = c
		}
	}
	types.ApplyTaskCreationDefaults(target, cfg, parent, map[string]struct{}{"subtasks_auto_merge": {}})
}

// handleLSPSymbolRequest resolves a symbol name to a position via text search,
// then calls the given LSP method at that position.
func (te *TaskExecutor) handleLSPSymbolRequest(args map[string]any, worktreePath string, method string, extraParams map[string]any) (string, error) {
	if te.lspMgr == nil {
		return "", fmt.Errorf("LSP manager not available")
	}
	path, _ := args["path"].(string)
	symbol, _ := args["symbol"].(string)
	if path == "" || symbol == "" {
		return "", fmt.Errorf("path and symbol are required")
	}

	// Check if tool calls are enabled for the LSP extension that handles this file
	if !te.lspMgr.IsToolCallsEnabledForFile(path) {
		return "", fmt.Errorf("LSP tool calls are disabled for this file type")
	}

	absPath := filepath.Join(worktreePath, path)
	absWorkspace, err := filepath.Abs(worktreePath)
	if err != nil {
		absWorkspace = worktreePath
	}

	// Read the file and find the symbol by text search
	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	position := findSymbolInText(string(content), symbol)
	if position == nil {
		return fmt.Sprintf("Symbol %q not found in %s. Use read_file to inspect the file.", symbol, path), nil
	}

	// Build the LSP request with the resolved position
	uri := "file://" + absPath
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
	}
	for k, v := range extraParams {
		params[k] = v
	}

	response, err := te.lspMgr.LSPRequest(absWorkspace, path, method, params)
	if err != nil {
		return "", err
	}

	return formatLSPResponse(response, method, absWorkspace), nil
}

// handleLSPHover resolves a symbol name to a position via text search,
// then calls textDocument/hover at that position.
func (te *TaskExecutor) handleLSPHover(args map[string]any, worktreePath string) (string, error) {
	return te.handleLSPSymbolRequest(args, worktreePath, "textDocument/hover", nil)
}

// findSymbolInText searches file content for an identifier and returns its LSP position
// (0-indexed line, 0-indexed character). Uses word boundary matching to avoid partial matches.
func findSymbolInText(content, symbol string) map[string]any {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		idx := findWordIndex(line, symbol)
		if idx >= 0 {
			return map[string]any{"line": i, "character": idx}
		}
	}
	return nil
}

// findWordIndex finds the start position of a word in a line, matching whole words only.
// Returns -1 if not found.
func findWordIndex(line, word string) int {
	for i := 0; i <= len(line)-len(word); i++ {
		if line[i:i+len(word)] == word {
			// Check word boundaries
			beforeOK := i == 0 || !isIdentChar(line[i-1])
			afterOK := i+len(word) >= len(line) || !isIdentChar(line[i+len(word)])
			if beforeOK && afterOK {
				return i
			}
		}
	}
	return -1
}

// isIdentChar checks if a character is valid in an identifier (letter, digit, underscore).
func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// formatLSPResponse formats a raw LSP JSON response into a human-readable string.
func formatLSPResponse(response json.RawMessage, method string, worktreePath string) string {
	// Check for LSP error
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err == nil && envelope.Error != nil {
		return fmt.Sprintf("LSP error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}

	// For success, unmarshal the result
	var result any
	if err := json.Unmarshal(response, &envelope); err != nil {
		return string(response)
	}
	if envelope.Result == nil || string(envelope.Result) == "null" {
		return "(no result)"
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return string(envelope.Result)
	}

	switch method {
	case "textDocument/references":
		return formatLocationArray(result, worktreePath, "references")
	case "textDocument/definition":
		return formatDefinitionResult(result, worktreePath)
	case "textDocument/codeAction":
		return formatCodeActions(result)
	case "textDocument/hover":
		return formatHoverResult(result)
	default:
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
}

// formatLocationArray formats an array of LSP Location objects.
func formatLocationArray(result any, worktreePath string, label string) string {
	locs, ok := result.([]any)
	if !ok {
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
	if len(locs) == 0 {
		return fmt.Sprintf("No %s found", label)
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "Found %d %s:\n", len(locs), label)
	for i, locAny := range locs {
		loc, ok := locAny.(map[string]any)
		if !ok {
			continue
		}
		uri, _ := loc["uri"].(string)
		rangeMap, _ := loc["range"].(map[string]any)
		start, _ := rangeMap["start"].(map[string]any)
		line, _ := start["line"].(float64)
		character, _ := start["character"].(float64)

		// Extract relative path from URI
		path := strings.TrimPrefix(uri, "file://")
		rel, _ := filepath.Rel(worktreePath, path)

		fmt.Fprintf(&buf, "  %d. %s:%d:%d\n", i+1, rel, int(line)+1, int(character)+1)
	}
	return buf.String()
}

// formatDefinitionResult formats a definition result (Location or Location[]).
func formatDefinitionResult(result any, worktreePath string) string {
	// Can be a single Location or LocationLink[] or Location[]
	switch v := result.(type) {
	case map[string]any:
		// Single Location
		loc := v
		uri, _ := loc["uri"].(string)
		rangeMap, _ := loc["range"].(map[string]any)
		start, _ := rangeMap["start"].(map[string]any)
		line, _ := start["line"].(float64)
		character, _ := start["character"].(float64)

		path := strings.TrimPrefix(uri, "file://")
		rel, _ := filepath.Rel(worktreePath, path)

		return fmt.Sprintf("Definition found at %s:%d:%d", rel, int(line)+1, int(character)+1)
	case []any:
		// Location[] or LocationLink[]
		if len(v) == 0 {
			return "No definition found"
		}
		var buf strings.Builder
		fmt.Fprintf(&buf, "Found %d definition(s):\n", len(v))
		for i, locAny := range v {
			loc, ok := locAny.(map[string]any)
			if !ok {
				continue
			}
			uri, _ := loc["uri"].(string)
			// Handle LocationLink (targetRange) vs Location (range)
			rangeMap, _ := loc["range"].(map[string]any)
			if rangeMap == nil {
				rangeMap, _ = loc["targetSelectionRange"].(map[string]any)
			}
			start, _ := rangeMap["start"].(map[string]any)
			line, _ := start["line"].(float64)
			character, _ := start["character"].(float64)

			path := strings.TrimPrefix(uri, "file://")
			rel, _ := filepath.Rel(worktreePath, path)

			fmt.Fprintf(&buf, "  %d. %s:%d:%d\n", i+1, rel, int(line)+1, int(character)+1)
		}
		return buf.String()
	default:
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
}

// formatCodeActions formats code actions result.
func formatCodeActions(result any) string {
	actions, ok := result.([]any)
	if !ok {
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}
	if len(actions) == 0 {
		return "No code actions available at this position"
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "Found %d code action(s):\n", len(actions))
	for i, actAny := range actions {
		act, ok := actAny.(map[string]any)
		if !ok {
			continue
		}
		title, _ := act["title"].(string)
		kind, _ := act["kind"].(string)
		fmt.Fprintf(&buf, "  %d. %s", i+1, title)
		if kind != "" {
			fmt.Fprintf(&buf, " [%s]", kind)
		}
		fmt.Fprintln(&buf)
	}
	return buf.String()
}

// formatHoverResult formats hover information result.
func formatHoverResult(result any) string {
	hover, ok := result.(map[string]any)
	if !ok {
		data, _ := json.MarshalIndent(result, "", "  ")
		return string(data)
	}

	contents, _ := hover["contents"].(map[string]any)
	if contents == nil {
		return "No hover information available"
	}

	// Handle MarkupContent { kind, value }
	if kind, ok := contents["kind"].(string); ok {
		value, _ := contents["value"].(string)
		return fmt.Sprintf("[%s]\n%s", kind, value)
	}

	// Handle MarkupContent as plain string
	if value, ok := contents["value"].(string); ok {
		return value
	}

	// Fallback: marshal the whole thing
	data, _ := json.MarshalIndent(contents, "", "  ")
	return string(data)
}

// stripANSI removes ANSI escape sequences from terminal output.
// Handles CSI (\x1b[...m), OSC (\x1b]...\x07), and single-byte escapes (\x1bH).
// CSI final bytes are in range 0x40–0x7E (@ through ~).
func stripANSI(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			i++
			if i < len(s) && s[i] == '[' {
				// CSI sequence — skip parameters/intermediates until final byte (>= 0x40)
				i++
				for i < len(s) && s[i] < '@' {
					i++
				}
				if i < len(s) {
					i++ // skip the final byte
				}
			} else if i < len(s) && s[i] == ']' {
				// OSC sequence — skip until ST (\x1b\\ or \x07)
				i++
				for i < len(s) {
					if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					if s[i] == '\x07' {
						i++
						break
					}
					i++
				}
			} else {
				// Other single-byte escape: \x1bA, \x1bH, \x1bM, etc.
				i++ // skip the next byte too
			}
		} else {
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}
