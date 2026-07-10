package exec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
			outputCallback(line)
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
	sandboxExec := state.sandboxExec
	sandboxExecStream := state.sandboxExecStream
	sandboxRead := state.sandboxRead
	sandboxWrite := state.sandboxWrite
	sandboxClient := state.sandboxClient
	// Lazy os.Root fallback (only used when sandbox callbacks are nil).
	// In production, loop.go always provides sandboxWrite via os.Root.
	// Read uses plain os.ReadFile (no isolation) when sandbox is disabled.
	makeRead := func() readFunc {
		return func(_ context.Context, p string) ([]byte, error) {
			norm := normalizePath(worktreePath, state.projectName, p)
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
			norm := normalizePath(worktreePath, state.projectName, p)
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
		// Check sandbox bridge liveness before I/O
		if sandboxClient != nil && !sandboxClient.IsAlive() {
			return "Error: sandbox bridge is not responding", false
		}
		data, err := read(ctx, path)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		content := string(data)
		// Apply offset/limit first so the LLM can read any region of large files.
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
		// Check sandbox bridge liveness before I/O
		if sandboxClient != nil && !sandboxClient.IsAlive() {
			return "Error: sandbox bridge is not responding", false
		}
		err := write(ctx, path, content)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		return fmt.Sprintf("Wrote %d bytes", len(content)), true
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
		// Check sandbox bridge liveness before I/O
		if sandboxClient != nil && !sandboxClient.IsAlive() {
			return "Error: sandbox bridge is not responding", false
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
			return fmt.Sprintf("Error: %v", err), false
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
			return fmt.Sprintf("Error: %v", err), false
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
		if sandboxExecStream != nil && outputCallback != nil {
			// Use streaming sandbox execution — output is streamed line by line
			out, err = sandboxExecStream(ctx, cmd, timeout, func(text string) {
				outputCallback(task.ID, text)
			})
		} else if sandboxExec != nil {
			// Use sandbox execution (non-streaming fallback) — ctx propagates task cancellation
			out, err = sandboxExec(ctx, cmd, timeout)
		} else {
			// Direct execution (no sandbox) with streaming output.
			c := exec.CommandContext(ctx, "bash", "-c", cmd)
			c.Dir = worktreePath
			stdout, pipeErr := c.StdoutPipe()
			if pipeErr != nil {
				return fmt.Sprintf("Failed: %v", pipeErr), false
			}
			stderr, pipeErr := c.StderrPipe()
			if pipeErr != nil {
				return fmt.Sprintf("Failed: %v", pipeErr), false
			}
			if startErr := c.Start(); startErr != nil {
				return fmt.Sprintf("Failed: %v", startErr), false
			}
			// Read stdout and stderr concurrently, stream output line by line.
			var buf bytes.Buffer
			var wg sync.WaitGroup
			stream := outputCallback != nil
			readStream := func(r io.Reader) {
				defer wg.Done()
				scanner := bufio.NewScanner(r)
				for scanner.Scan() {
					line := scanner.Text() + "\n"
					buf.WriteString(line)
					if stream {
						outputCallback(task.ID, line)
					}
				}
			}
			wg.Add(2)
			go readStream(stdout)
			go readStream(stderr)
			err = c.Wait()
			wg.Wait()
			out = buf.String()
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
	case "request_review":
		// In plan mode, require at least one task created during planning before allowing completion
		if task.PlanMode {
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
					independentCount, pluralS(independentCount), nestedCount, pluralS(nestedCount))
			} else if independentCount > 0 {
				summary = fmt.Sprintf("Plan created with %d task%s. Awaiting approval.",
					independentCount, pluralS(independentCount))
			} else {
				summary = fmt.Sprintf("Plan created with %d subtask%s. Awaiting approval.",
					nestedCount, pluralS(nestedCount))
			}
			log.Printf("[request_review] plan mode: %s", summary)
		}
		return "Task complete", true
	case "create_task":
		return te.handleCreateTask(args, task, state), true
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
func normalizePath(worktreePath, projectName, path string) string {
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
func (te *TaskExecutor) handleCreateTask(args map[string]any, parent *types.Task, state *executionState) string {
	title, _ := args["title"].(string)
	prompt, _ := args["prompt"].(string)
	parentTaskID, _ := args["parent_task_id"].(string)

	if title == "" || prompt == "" {
		return "Error: title and prompt are required"
	}

	// Determine the actual parent: if parent_task_id is provided, use it (nested subtask).
	// Otherwise, create as an independent task (no parent).
	actualParentID := parentTaskID
	var parentTask *types.Task
	if actualParentID != "" {
		var err error
		parentTask, err = te.store.GetTaskByTaskID(actualParentID)
		if err != nil {
			return fmt.Sprintf("Error: parent task %s not found", actualParentID)
		}
		if parentTask.Status != "backlog" && parentTask.Status != "in_progress" {
			return fmt.Sprintf("Error: parent task %s is not in backlog or in_progress (status: %s)", actualParentID, parentTask.Status)
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
		AutoCommit:                parent.AutoCommit,
		PlanMode:                  false,
		ParentTaskID:              actualParentID,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}

	if err := te.store.CreateTask(newTask); err != nil {
		return fmt.Sprintf("Error: failed to create task: %v", err)
	}

	// Track the created task ID for plan mode validation
	if state != nil {
		state.createdIDs = append(state.createdIDs, newTaskID)
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
	te.appStreamMgr.BroadcastTaskUpdated(parent.ProjectID, *newTask)

	if actualParentID != "" {
		log.Printf("[create_task] task=%s created subtask %s (parent=%s): %q", parent.ID, newTaskID, actualParentID, title)
	} else {
		log.Printf("[create_task] task=%s created task %s: %q", parent.ID, newTaskID, title)
	}

	// Return JSON with task_id so the LLM can reference it for nested subtasks
	result, _ := json.Marshal(map[string]string{"task_id": newTaskID, "title": title})
	return string(result)
}

// pluralS returns "s" if count > 1, empty string otherwise.
func pluralS(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
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
