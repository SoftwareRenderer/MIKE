package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"kanban/backend/llama"
	"kanban/backend/sandbox"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// LoopType indicates the type of loop detected.
type LoopType int

const (
	LoopNone LoopType = iota
	LoopTool // repeated read of same target
	LoopText // repeated similar text-only responses
	LoopPhrase // exact phrase repetition within a response
)

const maxLoopHints = 3

// loopDetector tracks tool call targets and text responses to detect LLM loops.
type loopDetector struct {
	lastReadTarget string    // normalized target of the most recent read-only call
	lastReadTime   time.Time // when the last read occurred
	textWindow     []string  // recent text-only responses (content, trimmed)
	hintCount      int       // total hints injected for this task
	phraseDet      *phraseDetector // real-time phrase repetition tracker
	phraseFired    bool      // phrase loop detected during current streaming response
}

// newLoopDetector creates a new loopDetector.
func newLoopDetector() *loopDetector {
	return &loopDetector{
		textWindow: make([]string, 0, 8),
		phraseDet:  newPhraseDetector(),
	}
}

// reset clears all tracked state (called on user message or after hint injection).
func (d *loopDetector) reset() {
	d.lastReadTarget = ""
	d.lastReadTime = time.Time{}
	d.textWindow = d.textWindow[:0]
	d.phraseDet.reset()
	d.phraseFired = false
}

// resetText clears only the text window (called after a tool call breaks text-only streak).
func (d *loopDetector) resetText() {
	d.textWindow = d.textWindow[:0]
}

// recordTurn records an LLM turn and returns the loop type if detected.
// toolCalls: the tool calls made this turn (nil or empty for text-only).
// assistantText: the text content of the response.
func (d *loopDetector) recordTurn(toolCalls []types.ChatToolCall, assistantText string) LoopType {
	hasToolCalls := len(toolCalls) > 0 && toolCalls[0].Function.Name != ""

	if hasToolCalls {
		d.resetText() // tool call breaks text-only streak
		d.phraseDet.reset() // tool call resets phrase tracker

		// Check each tool call
		for _, tc := range toolCalls {
			if tc.Function.Name == "" {
				continue
			}
			target := extractTarget(tc.Function.Name, tc.Function.Arguments)
			if target == "" {
				continue
			}

			if isReadOnlyCall(tc.Function.Name, tc.Function.Arguments) {
				// Read-only call: check if it repeats the last read target
				if d.lastReadTarget != "" && target == d.lastReadTarget {
					return LoopTool
				}
				d.lastReadTarget = target
				d.lastReadTime = time.Now()
			} else {
				// State-changing call: clear the read tracker
				d.lastReadTarget = ""
				d.lastReadTime = time.Time{}
			}
		}
	} else if strings.TrimSpace(assistantText) != "" {
		// Text-only response: add to window and check for repetition
		trimmed := strings.TrimSpace(assistantText)
		d.textWindow = append(d.textWindow, trimmed)
		if len(d.textWindow) >= 2 {
			if d.detectTextLoop() {
				return LoopText
			}
		}
	}

	return LoopNone
}

// checkStreamingText feeds accumulated streaming text into the phrase detector.
// Sets phraseFired flag and returns LoopPhrase if exact phrase repetition is detected.
func (d *loopDetector) checkStreamingText(text string) LoopType {
	if d.phraseDet.check(text) {
		d.phraseFired = true
		return LoopPhrase
	}
	return LoopNone
}

// phraseDetected returns true if phrase repetition was detected during the current stream.
func (d *loopDetector) phraseDetected() bool {
	return d.phraseFired
}

// extractTarget extracts the primary file/target from a tool call.
func extractTarget(toolName, rawArgs string) string {
	switch toolName {
	case "read_file", "write_file", "edit_file":
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(rawArgs), &args); err == nil {
			if p, ok := args["path"].(string); ok {
				return normalizeTarget(p)
			}
		}
	case "bash":
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(rawArgs), &args); err == nil {
			if cmd, ok := args["command"].(string); ok {
				return extractCommandTarget(cmd)
			}
		}
	}
	return ""
}

// extractCommandTarget extracts the primary file/target from a shell command.
// Handles: head file, wc file, grep pattern file, cat file, ls dir, tail file, diff f1 f2,
// sed file, python script.py, etc.
func extractCommandTarget(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}

	tokens := shellTokens(cmd)
	if len(tokens) == 0 {
		return ""
	}

	// Take only the first command (before any pipe)
	pipeIdx := -1
	for i, t := range tokens {
		if t == "|" {
			pipeIdx = i
			break
		}
	}
	if pipeIdx >= 0 {
		tokens = tokens[:pipeIdx]
	}

	if len(tokens) < 2 {
		return ""
	}

	verb := tokens[0]
	args := tokens[1:]

	// Filter out flags (tokens starting with -)
	nonFlags := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && a != ">" && a != ">>" && a != "2>&1" && a != "&&" {
			nonFlags = append(nonFlags, a)
		}
	}

	switch verb {
	case "head", "tail", "cat", "wc", "stat", "file", "diff":
		if len(nonFlags) > 0 {
			return normalizeTarget(nonFlags[0])
		}
	case "grep":
		if len(nonFlags) >= 2 {
			return normalizeTarget(nonFlags[len(nonFlags)-1])
		} else if len(nonFlags) == 1 {
			return normalizeTarget(nonFlags[0])
		}
	case "ls":
		if len(nonFlags) > 0 {
			return normalizeTarget(nonFlags[0])
		}
		return normalizeTarget(".")
	case "sed":
		if len(nonFlags) >= 2 {
			return normalizeTarget(nonFlags[len(nonFlags)-1])
		}
	case "python", "python3":
		if len(nonFlags) > 0 {
			return normalizeTarget(nonFlags[0])
		}
	case "echo", "printf":
		for i, t := range tokens {
			if (t == ">" || t == ">>") && i+1 < len(tokens) {
				return normalizeTarget(tokens[i+1])
			}
		}
	case "mv", "cp":
		if len(nonFlags) >= 2 {
			return normalizeTarget(nonFlags[len(nonFlags)-1])
		}
	case "git":
		if len(args) > 0 {
			subcmd := args[0]
			switch subcmd {
			case "add", "rm":
				if slices.Contains(args, "-A") {
					return normalizeTarget(".")
				}
				if len(nonFlags) > 0 {
					return normalizeTarget(nonFlags[0])
				}
			case "commit", "diff", "status", "log":
				return normalizeTarget(".")
			}
		}
	}

	return ""
}

// shellTokens splits a shell command into tokens, handling pipes and redirects.
func shellTokens(cmd string) []string {
	var tokens []string
	current := strings.Builder{}
	inSingleQuote, inDoubleQuote := false, false
	for _, r := range cmd {
		switch r {
		case '\'':
			inSingleQuote = !inSingleQuote
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			} else {
				current.WriteRune(r)
			}
		case ' ', '\t':
			if !inSingleQuote && !inDoubleQuote {
				if current.Len() > 0 {
					tokens = append(tokens, current.String())
					current.Reset()
				}
			} else {
				current.WriteRune(r)
			}
		case '|', '&':
			if !inSingleQuote && !inDoubleQuote {
				if current.Len() > 0 {
					tokens = append(tokens, current.String())
					current.Reset()
				}
				if r == '|' {
					tokens = append(tokens, "|")
				} else {
					tokens = append(tokens, "&&")
				}
			} else {
				current.WriteRune(r)
			}
		case '>':
			if !inSingleQuote && !inDoubleQuote {
				if current.Len() > 0 {
					tokens = append(tokens, current.String())
					current.Reset()
				}
				tokens = append(tokens, ">")
			} else {
				current.WriteRune(r)
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// normalizeTarget normalizes a file path for comparison.
func normalizeTarget(p string) string {
	p = strings.TrimSpace(p)
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return p
}

// isReadOnlyCall classifies a tool call as read-only (no state changes) or state-changing.
func isReadOnlyCall(toolName, rawArgs string) bool {
	switch toolName {
	case "read_file":
		return true
	case "write_file", "edit_file":
		return false
	case "task_complete":
		return false
	case "bash":
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(rawArgs), &args); err == nil {
			if cmd, ok := args["command"].(string); ok {
				return isReadOnlyCommand(cmd)
			}
		}
		return true // default to read-only for unknown commands (safer)
	}
	return true
}

// readOnlyCommands are commands that only read data without modifying files.
var readOnlyCommands = []string{
	"head", "tail", "cat", "wc", "ls", "stat", "file", "diff",
	"grep", "find", "echo", "printf", "pwd", "whoami", "uname",
	"date", "which", "type", "command", "test", "du", "df",
	"tree", "xxd", "od", "hexdump", "strings", "tr", "cut",
	"sort", "uniq", "comm", "fold", "join", "paste", "split",
	"basename", "dirname", "realpath", "readlink", "mktemp",
}

// isReadOnlyCommand checks if a shell command is read-only.
func isReadOnlyCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	tokens := shellTokens(cmd)
	if len(tokens) == 0 {
		return true
	}

	for i, t := range tokens {
		if t == "|" {
			tokens = tokens[:i]
			break
		}
	}

	verb := tokens[0]

	for _, t := range tokens {
		if t == ">" || t == ">>" {
			return false
		}
	}

	if strings.Contains(cmd, "&&") {
		parts := strings.Split(cmd, "&&")
		for _, part := range parts[1:] {
			if !isReadOnlyCommand(strings.TrimSpace(part)) {
				return false
			}
		}
	}

	for _, ro := range readOnlyCommands {
		if verb == ro {
			return true
		}
	}

	stateChanging := []string{
		"python", "python3", "node", "npm", "pip", "cargo", "go",
		"make", "gcc", "g++", "rustc", "javac", "ruby", "perl",
		"sed", "awk", "vim", "nano", "emacs", "touch",
		"mv", "cp", "rm", "mkdir", "rmdir", "chmod", "chown",
		"install", "curl", "wget", "apt", "yum", "dnf", "brew",
		"git", "docker", "kubectl", "terraform", "ansible",
	}
	for _, sc := range stateChanging {
		if verb == sc {
			return false
		}
	}

	return true
}

// buildHint generates a targeted system message based on loop type.
func buildHint(loopType LoopType, target string) string {
	switch loopType {
	case LoopTool:
		return fmt.Sprintf("You've read/checked `%s` twice in a row without making any changes in between. You already have the contents of this file. Summarize what you know and decide on the next action \u2014 edit the file, run a command, or mark the task complete.", target)
	case LoopText:
		return "You've produced several similar responses without calling any tools. Take action \u2014 call a tool to make progress, or use `task_complete` if the task is done."
	case LoopPhrase:
		return "You repeated yourself in your last response. Continue with a new action or tool call."
	default:
		return ""
	}
}

// executeTask runs the AI conversation loop.
func (te *TaskExecutor) executeTask(ctx context.Context, task *types.Task, projectID, projectName, worktreePath, serverURL, modelName string, b *stream.Broadcaster, userMsgChan chan stream.UserMessage, sandboxEnabled bool, networkIsolation bool, baseImage, sandboxDockerfilePath string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[panic] task=%s recovered from panic: %v", task.ID, r)
			te.sendAndPersistError(b, task.ID, fmt.Sprintf("Internal error: %v", r), "panic")
		}
		te.taskMu.Lock()
		delete(te.tasks, task.ID)
		te.taskMu.Unlock()
		// Don't remove the stream here — the broadcaster lives as long as clients are connected.
		// It's removed only when the task is deleted (via RemoveStream in cleanupTask).
		// Sandbox container is also kept alive until task deletion.
	}()

	// Create sandbox if enabled
	var execFn execFunc
	var readFn readFunc
	var writeFn writeFunc
	if sandboxEnabled {
		if te.sandboxMgr == nil || !te.sandboxMgr.IsAvailable() {
			task.StatusState = "failed"
			task.Output = "Sandbox enabled but gVisor (runsc) is not available. Install gVisor: sudo bash scripts/install-gvisor.sh"
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(b, task.ID, "gVisor not available", "sandbox_unavailable")
			return
		}
		// Determine image to use: Dockerfile build takes precedence over pre-built image
		imageToUse := ""
		if sandboxDockerfilePath != "" {
			imageToUse = sandbox.ProjectImageName(projectName)
			if !te.sandboxMgr.ImageReadyForProject(projectName) {
				b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "building_image", "activity": "Building sandbox image from Dockerfile"}})
				if err := te.sandboxMgr.BuildImage(projectName, sandboxDockerfilePath); err != nil {
					task.StatusState = "failed"
					task.Output = fmt.Sprintf("Failed to build sandbox image: %v", err)
					te.store.UpdateTask(task)
					te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(b, task.ID, fmt.Sprintf("Image build failed: %v", err), "image_build_failed")
					return
				}
			}
		} else {
			// Pull pre-built image if not available locally
			imageToUse = baseImage
			if imageToUse == "" {
				imageToUse = sandbox.BaseImage
			}
			if !te.sandboxMgr.ImageReady(imageToUse) {
				b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "pulling_image", "activity": "Pulling " + imageToUse}})
				if err := te.sandboxMgr.PullImage(imageToUse); err != nil {
					task.StatusState = "failed"
					task.Output = fmt.Sprintf("Failed to pull sandbox image: %v", err)
					te.store.UpdateTask(task)
					te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(b, task.ID, fmt.Sprintf("Image pull failed: %v", err), "image_pull_failed")
					return
				}
			}
		}
		if err := te.sandboxMgr.Create(task.ID, worktreePath, imageToUse, networkIsolation); err != nil {
			task.StatusState = "failed"
			task.Output = fmt.Sprintf("Failed to create sandbox: %v", err)
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(b, task.ID, fmt.Sprintf("Sandbox creation failed: %v", err), "sandbox_create_failed")
			return
		}
		log.Printf("[sandbox] task=%s sandbox created with image %s", task.ID, imageToUse)

		// Build sandbox exec function
		execFn = func(command string) (string, error) {
			return te.sandboxMgr.Exec(task.ID, command, nil)
		}
		// Build sandbox read/write functions
		readFn = func(path string) ([]byte, error) {
			out, err := te.sandboxMgr.Exec(task.ID, "cat "+shellQuote(path), nil)
			if err != nil {
				return nil, fmt.Errorf("%v: %s", err, out)
			}
			return []byte(out), nil
		}
		writeFn = func(path, content string) error {
			_, err := te.sandboxMgr.ExecWithInput(task.ID, "mkdir -p "+shellQuote(filepath.Dir(path))+" && cat > "+shellQuote(path), []byte(content), nil)
			if err != nil {
				return fmt.Errorf("write failed: %w", err)
			}
			return nil
		}
	}

	mc := &modelConfig{serverURL: serverURL, modelName: modelName}
	providerName := te.configStore.GetProviderNameByURL(serverURL)
	reasoningEffort := task.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = te.configStore.GetModelReasoningEffort(modelName)
	}
	apiKey := te.configStore.GetProviderAPIKey(serverURL)
	client := llama.NewLlamaClient(serverURL, modelName, reasoningEffort, apiKey)
	// In plan mode, restrict tools to read-only + create_task + task_complete
	var tools []types.ChatTool
	if task.PlanMode {
		tools = llama.GetPlanModeToolDefinitions()
	} else {
		tools = llama.GetToolDefinitions()
	}
	// In sandbox mode, use the container mount path so the LLM sees
	// the actual working directory inside the sandbox.
	promptPath := worktreePath
	if sandboxEnabled {
		promptPath = sandbox.SandboxMountPath
	}
	var systemPrompt string
	if task.PlanMode {
		systemPrompt = llama.BuildPlanModeSystemPrompt(promptPath)
	} else {
		systemPrompt = llama.BuildSystemPrompt(promptPath)
	}

	taskMsgs, _ := te.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	messages := []types.ChatMessage{{Role: "system", Content: systemPrompt}}
	for _, m := range taskMsgs {
		switch m.Role {
		case "user":
			// Check for image attachments and build multi-modal content
			if m.Attachments != "" {
				var atts []struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				}
				if err := json.Unmarshal([]byte(m.Attachments), &atts); err == nil {
					parts := []types.ContentPart{{Type: "text", Text: m.Content}}
					for _, att := range atts {
						if att.Data != "" {
							mediaType := att.MimeType
							if mediaType == "" {
								mediaType = "image/png"
							}
							base64Data := att.Data
							// Strip data: prefix if present
							if idx := strings.Index(base64Data, ","); idx >= 0 {
								base64Data = base64Data[idx+1:]
							}
							parts = append(parts, types.ContentPart{
								Type: "image",
								ImageSource: &struct {
									Type      string `json:"type"`
									MediaType string `json:"media_type"`
									Data      string `json:"data"`
								}{Type: "base64", MediaType: mediaType, Data: base64Data},
							})
						}
					}
					messages = append(messages, types.ChatMessage{Role: m.Role, ContentParts: parts})
					continue
				}
			}
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: m.Content})
		case "assistant":
			// Merge reasoning_content into content so all models see the full assistant message
			// (reasoning_content field in requests is undefined behavior - works for Qwen, not gemma)
			content := m.Content
			if m.ReasoningContent != "" {
				content = m.ReasoningContent + "\n" + m.Content
			}
			messages = append(messages, types.ChatMessage{Role: m.Role, Content: content})
		case "tool_call":
			if m.ToolResult != "" {
				var resultMap map[string]interface{}
				if err := json.Unmarshal([]byte(m.ToolResult), &resultMap); err == nil {
					output := resultMap["output"].(string)
					toolID := ""
					if m.ToolCalls != "" {
						var tcMap map[string]interface{}
						if err2 := json.Unmarshal([]byte(m.ToolCalls), &tcMap); err2 == nil {
							if tid, ok := tcMap["tool_id"].(string); ok {
								toolID = tid
							}
						}
					}
					messages = append(messages, types.ChatMessage{Role: "tool", Content: output, ToolCallID: toolID})
				}
			}
		}
	}

	consecutiveFailures := 0
	maxFailures := task.MaxConsecutiveFailedTurns
	if maxFailures == 0 {
		maxFailures = 3
	}

	// Context tracking: load context window from provider config, discover if unknown
	var contextWindow *int
	if cw := te.configStore.GetModelContextWindow(modelName, serverURL); cw != nil {
		contextWindow = cw
	} else if discovered := te.configStore.DiscoverContextWindow(modelName, serverURL); discovered != nil {
		contextWindow = discovered
		te.configStore.SaveModelContextWindow(modelName, serverURL, *discovered)
		log.Printf("[context] discovered context window for %s: %d", modelName, *discovered)
	}
	lastCompressIdx := -1 // index in messages slice up to which content was already compressed

	var assistantContent strings.Builder
	var reasoningContent strings.Builder
	var pendingToolCalls []types.ChatToolCall
	detector := newLoopDetector()
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(b, task.ID, "Task stopped", "stopped")
			return
		default:
		}

		// Check for model/provider changes before each LLM call.
		if updatedTask, err := te.store.GetTaskByTaskID(task.ID); err == nil {
			newURL := updatedTask.LlamaServerURL
			newModel := updatedTask.ModelName
			curURL, curModel := mc.get()
			if newURL != "" && newURL != curURL || newModel != "" && newModel != curModel {
				if newURL == "" {
					newURL = curURL
				}
				if newModel == "" {
					newModel = curModel
				}
				mc.set(newURL, newModel)
				newReasoningEffort := updatedTask.ReasoningEffort
				if newReasoningEffort == "" {
					newReasoningEffort = te.configStore.GetModelReasoningEffort(newModel)
				}
				newAPIKey := te.configStore.GetProviderAPIKey(newURL)
				client = llama.NewLlamaClient(newURL, newModel, newReasoningEffort, newAPIKey)
				providerName = te.configStore.GetProviderNameByURL(newURL)
				contextWindow = te.configStore.GetModelContextWindow(newModel, newURL)
				if contextWindow == nil {
					if discovered := te.configStore.DiscoverContextWindow(newModel, newURL); discovered != nil {
						contextWindow = discovered
						te.configStore.SaveModelContextWindow(newModel, newURL, *discovered)
					}
				}
				log.Printf("[model-switch] task=%s url=%s model=%s context_window=%v", task.ID, newURL, newModel, contextWindow)
			}
		}

		// Check for user messages that arrived during tool execution.
		select {
		case um := <-userMsgChan:
			log.Printf("[user-msg] task=%s received message before LLM call", task.ID)
			detector.reset() // user input breaks any loop pattern
			messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
			if um.ModelName != "" {
				mc.set(um.ServerURL, um.ModelName)
				newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
				newAPIKey := te.configStore.GetProviderAPIKey(um.ServerURL)
				client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort, newAPIKey)
				providerName = te.configStore.GetProviderNameByURL(um.ServerURL)
			}
		default:
		}

		// Acquire concurrency gate slot before LLM call.
		serverURL, activeModel := mc.get()
		concurrency := te.configStore.GetProviderConcurrency(serverURL)

		// Queue status polling — runs while waiting for a gate slot.
		queueDone := make(chan struct{})
		go func() {
			tick := time.NewTicker(1 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-queueDone:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					pos := te.gate.Position(serverURL)
					est := te.gate.EstimateWait(serverURL)
					b.Send(types.StreamEvent{Type: types.WSTypeQueue, Data: map[string]interface{}{
						"position":             pos,
						"estimated_wait_seconds": est,
					}})
				}
			}
		}()

		_, err := te.gate.Acquire(ctx, serverURL, concurrency)
		if err != nil {
			close(queueDone)
			// Context cancelled while waiting for slot
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(b, task.ID, "Task stopped while waiting for LLM slot", "stopped")
			return
		}
		close(queueDone)

		// Per-call context — can be cancelled independently to abort the LLM stream.
		callCtx, callCancel := context.WithCancel(ctx)

		streamDone := make(chan struct{})
		var usage *types.ChatUsage
		var streamErr error

		go func() {
			usage, streamErr = client.ChatStream(callCtx, messages, tools, func(choice types.ChatChoice) {
				if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
					reasoningContent.WriteString(*choice.Delta.ReasoningContent)
					b.Send(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
				}
				if choice.Delta.Content != nil && *choice.Delta.Content != "" {
					assistantContent.WriteString(*choice.Delta.Content)
					b.Send(types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
					// Real-time phrase repetition detection
					if loopType := detector.checkStreamingText(*choice.Delta.Content); loopType == LoopPhrase {
						log.Printf("[loop] task=%s phrase repetition detected during streaming", task.ID)
						b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Repetitive response detected"}})
					}
				}
				if len(choice.Delta.ToolCalls) > 0 {
					pendingToolCalls = llama.MergeToolCalls(pendingToolCalls, choice.Delta.ToolCalls)
				}
			})

			// Retry on transient errors (skip if cancelled by user message).
			if streamErr != nil && callCtx.Err() == nil {
				for i := 0; i < 3; i++ {
					select {
					case <-callCtx.Done():
						close(streamDone)
						return
					case <-time.After(time.Duration(i+1) * 2 * time.Second):
					}
					usage, streamErr = client.ChatStream(callCtx, messages, tools, func(choice types.ChatChoice) {
						if choice.Delta.ReasoningContent != nil {
							reasoningContent.WriteString(*choice.Delta.ReasoningContent)
							b.Send(types.StreamEvent{Type: types.WSTypeReasoning, Data: map[string]string{"text": *choice.Delta.ReasoningContent}})
						}
						if choice.Delta.Content != nil {
							b.Send(types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": *choice.Delta.Content}})
						}
					})
					if streamErr == nil {
						break
					}
				}
			}

			close(streamDone)
		}()

		// Wait for: stream complete, new user message, or task stop.
		select {
		case <-ctx.Done():
			callCancel()
			te.gate.Release(serverURL) // Release gate immediately so other tasks can proceed
			<-streamDone // Then wait for goroutine to finish
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			return
		case <-streamDone:
			// Stream completed normally (or all retries exhausted)
		case um := <-userMsgChan:
			// New user message — cancel current LLM call.
			log.Printf("[cancel] task=%s user message received during streaming, cancelling", task.ID)
			detector.reset() // user input breaks any loop pattern
			callCancel()
			// Wait for goroutine to finish (with timeout in case HTTP stream is slow to abort).
			select {
			case <-streamDone:
			case <-time.After(2 * time.Second):
				log.Printf("[cancel] task=%s streamDone timeout, forcing continue", task.ID)
			}
			te.gate.Release(serverURL)

			// Drain all queued messages from channel (non-blocking).
			var queuedMsgs []stream.UserMessage
			queuedMsgs = append(queuedMsgs, um)
			for i := 0; i < 16; i++ {
				select {
				case q := <-userMsgChan:
					queuedMsgs = append(queuedMsgs, q)
				default:
					goto drainDone
				}
			}
		drainDone:

			// Clear partial assistant state.
			assistantContent.Reset()
			reasoningContent.Reset()
			pendingToolCalls = nil

			// Add queued messages to conversation history.
			for _, qm := range queuedMsgs {
				// Check for image attachments
				if qm.Attachments != "" {
					var atts []struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					}
					if err := json.Unmarshal([]byte(qm.Attachments), &atts); err == nil {
						parts := []types.ContentPart{{Type: "text", Text: qm.Content}}
						for _, att := range atts {
							if att.Data != "" {
								mediaType := att.MimeType
								if mediaType == "" {
									mediaType = "image/png"
								}
								base64Data := att.Data
								if idx := strings.Index(base64Data, ","); idx >= 0 {
									base64Data = base64Data[idx+1:]
								}
								parts = append(parts, types.ContentPart{
									Type: "image",
									ImageSource: &struct {
										Type      string `json:"type"`
										MediaType string `json:"media_type"`
										Data      string `json:"data"`
									}{Type: "base64", MediaType: mediaType, Data: base64Data},
								})
							}
						}
						messages = append(messages, types.ChatMessage{Role: "user", ContentParts: parts})
						continue
					}
				}
				messages = append(messages, types.ChatMessage{Role: "user", Content: qm.Content})
				if qm.ModelName != "" {
					mc.set(qm.ServerURL, qm.ModelName)
					newReasoningEffort := te.configStore.GetModelReasoningEffort(qm.ModelName)
					newAPIKey := te.configStore.GetProviderAPIKey(qm.ServerURL)
					client = llama.NewLlamaClient(qm.ServerURL, qm.ModelName, newReasoningEffort, newAPIKey)
					providerName = te.configStore.GetProviderNameByURL(qm.ServerURL)
				}
			}

			// Notify frontend that response was cancelled.
			b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "cancelled", "activity": "Response cancelled, processing new message"}})

			continue
		}
		callCancel()

		if streamErr != nil {
			task.StatusState = "failed"
			task.Output = fmt.Sprintf("Connection failed: %v", streamErr)
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(b, task.ID, streamErr.Error(), "connection_failed")
			return
		}

		// Release gate slot after LLM call (before tool execution).
		te.gate.Release(serverURL)

		assistantText := assistantContent.String()

		type toolResult struct {
			toolCallID string
			output     string
		}
		var toolResults []toolResult

		if len(pendingToolCalls) > 0 {
			// Persist assistant message BEFORE tool calls so DB ordering matches LLM output order
			msgCountBefore := len(messages)
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(usage, messages[:msgCountBefore], assistantText, reasoningContent.String(), startTime)
			assistantCreatedAt := time.Now().UTC()
			assistantMsgID := uuid.New().String()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: assistantMsgID, TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: reasoningContent.String(),
				ModelName: activeModel, ProviderName: providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: time.Since(startTime).Milliseconds(), CreatedAt: assistantCreatedAt,
			})

			for _, tc := range pendingToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					log.Printf("[tool] skipping incomplete tool call (id=%q, name=%q)", tc.ID, tc.Function.Name)
					continue
				}
				args := parseToolArgs(tc.Function.Arguments)
				toolStart := time.Now()
				te.taskMu.Lock()
				state := te.tasks[task.ID]
				te.taskMu.Unlock()
				result, success := te.executeTool(tc.Function.Name, args, worktreePath, task, state, execFn, readFn, writeFn)
				toolDuration := time.Since(toolStart).Milliseconds()
				isSandboxed := sandboxEnabled && isSandboxableTool(tc.Function.Name)
				b.Send(types.StreamEvent{Type: types.WSTypeToolCall, Data: map[string]interface{}{"tool_id": tc.ID, "tool_name": tc.Function.Name, "tool_display_name": llama.GetToolDisplayName(tc.Function.Name), "args": args, "raw_args": tc.Function.Arguments, "sandbox": isSandboxed}})
				b.Send(types.StreamEvent{Type: types.WSTypeToolResult, Data: map[string]interface{}{"tool_id": tc.ID, "success": success, "output": result, "duration": toolDuration}})
				toolResults = append(toolResults, toolResult{toolCallID: tc.ID, output: result})

				tcJSON, _ := json.Marshal(map[string]interface{}{
					"tool_id":           tc.ID,
					"tool_name":         tc.Function.Name,
					"tool_display_name": llama.GetToolDisplayName(tc.Function.Name),
					"args":              args,
					"raw_args":          tc.Function.Arguments,
					"sandbox":           sandboxEnabled && isSandboxableTool(tc.Function.Name),
				})
				resultJSON, _ := json.Marshal(map[string]interface{}{
					"output":   result,
					"success":  success,
					"duration": toolDuration,
				})
				_ = te.store.CreateTaskMessage(&types.TaskMessage{
					ID:         uuid.New().String(),
					TaskID:     task.ID,
					ThreadID:   task.CurrentThreadID,
					Role:       "tool_call",
					Content:    "",
					ToolCalls:  string(tcJSON),
					ToolResult: string(resultJSON),
					CreatedAt:  time.Now().UTC(),
				})

				if !success {
					consecutiveFailures++
					if consecutiveFailures >= maxFailures {
						task.StatusState = "failed"
						task.Output = fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures)
						te.store.UpdateTask(task)
						te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
						te.sendAndPersistError(b, task.ID, fmt.Sprintf("Max consecutive failed turns (%d) reached", maxFailures), "max_failures")
						return
					}
				} else {
					consecutiveFailures = 0
				}

				if tc.Function.Name == "task_complete" {
					// Extract summary from tool args
					if summary, ok := args["summary"].(string); ok && summary != "" {
						task.ChangeSummary = summary
					}
					modelName := activeModel
					if modelName == "" {
						modelName = "unknown model"
					}
					if task.AutoCommit {
						summary := task.ChangeSummary
						if summary == "" {
							// Generate a descriptive label from conversation + diff
							diffStat, err := te.worktreeMgr.GitRunInWorktreeOutput(task.ID, "diff", "--stat", "HEAD")
							if err != nil {
								diffStat = "no diff available"
							}
							summary = te.GenerateCommitLabel(task.ID, diffStat, messages)
						}
						commitMsg := fmt.Sprintf("%s, generated by %s\n\n%s", task.Title, modelName, summary)
						te.gitRun(worktreePath, "add", "-A")
						te.gitRun(worktreePath, "commit", "-m", commitMsg)
						task.StatusState = "completed"
						task.Status = "done"
					} else {
						task.StatusState = "awaiting_review"
						task.Status = "review"
					}
					te.store.UpdateTask(task)
					b.Send(types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]interface{}{
						"state": task.StatusState,
						"assistant_message_id": assistantMsgID,
						"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
						"cache_tokens": cacheTokens,
						"prompt_ms": promptMs, "predicted_ms": predictedMs,
						"model_name": activeModel, "duration": time.Since(startTime).Milliseconds(),
					}})
					te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
					// Broadcast diff so the frontend updates the diff panel
					baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, "")
					if diffResult, err := te.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil {
						untrackedMaps := make([]map[string]interface{}, len(diffResult.Untracked))
						for i, u := range diffResult.Untracked {
							untrackedMaps[i] = map[string]interface{}{"path": u.Path, "file_size": u.FileSize, "diff": u.Diff}
						}
						te.boardStreamMgr.BroadcastDiff(projectID, task.ID, diffResult.Diff, diffResult.ShortStat, len(diffResult.Untracked), untrackedMaps)
					}
					// If this is a subtask, auto-start the next sibling
					if task.ParentTaskID != "" {
						_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
					}
					return
				}
			}
			// Build assistant message with merged reasoning + content + tool calls.
			// Including tool_calls in the message history is critical for models like gemma
			// to understand what tool was called and match it to the result.
			assistantContentFull := assistantText
			if reasoningContent.Len() > 0 {
				assistantContentFull = reasoningContent.String() + "\n" + assistantText
			}
			var assistantToolCalls []types.ToolCallMsg
			for _, tc := range pendingToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					assistantToolCalls = append(assistantToolCalls, types.ToolCallMsg{
						ID:   tc.ID,
						Type: "function",
					})
					assistantToolCalls[len(assistantToolCalls)-1].Function.Name = tc.Function.Name
					assistantToolCalls[len(assistantToolCalls)-1].Function.Arguments = tc.Function.Arguments
				}
			}
			messages = append(messages, types.ChatMessage{
				Role:      "assistant",
				Content:   assistantContentFull,
				ToolCalls: assistantToolCalls,
			})
			for _, tr := range toolResults {
				messages = append(messages, types.ChatMessage{Role: "tool", Content: tr.output, ToolCallID: tr.toolCallID})
			}

			// Loop detection: record this turn and check for repetition
			loopType := detector.recordTurn(pendingToolCalls, assistantText)
			if loopType != LoopNone {
				if detector.hintCount >= maxLoopHints {
					task.StatusState = "failed"
					task.Output = "Model stuck in loop (max hints exhausted)"
					te.store.UpdateTask(task)
					te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(b, task.ID, "Model stuck in loop (max hints exhausted)", "loop_detected")
					return
				}
				// Extract target for hint message
				target := ""
				if loopType == LoopTool && len(pendingToolCalls) > 0 && pendingToolCalls[0].Function.Name != "" {
					target = extractTarget(pendingToolCalls[0].Function.Name, pendingToolCalls[0].Function.Arguments)
				}
				hint := buildHint(loopType, target)
				log.Printf("[loop] task=%s detected loop type=%d (hint %d/%d), target=%s", task.ID, loopType, detector.hintCount+1, maxLoopHints, target)
				messages = append(messages, types.ChatMessage{Role: "system", Content: hint})
				detector.hintCount++
				detector.reset()
				// Notify frontend
				b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Loop detected, steering agent"}})
			}

			pendingToolCalls = nil
			assistantContent.Reset()
			reasoningContent.Reset()

			// Auto-commit file changes as a checkpoint
			te.createCheckpoint(task.ID, worktreePath, task.CurrentThreadID, assistantMsgID, messages, b, sandboxEnabled)

			// Broadcast updated diff stats to board viewers
			thread, _ := te.store.GetThread(task.CurrentThreadID)
			forkBranch := ""
			if thread != nil {
				forkBranch = thread.GitBranch
			}
			baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, forkBranch)
			if diffResult, err := te.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0) {
				untrackedMaps := make([]map[string]interface{}, len(diffResult.Untracked))
				for i, u := range diffResult.Untracked {
					untrackedMaps[i] = map[string]interface{}{"path": u.Path, "file_size": u.FileSize, "diff": u.Diff}
				}
				te.boardStreamMgr.BroadcastDiff(projectID, task.ID, diffResult.Diff, diffResult.ShortStat, len(diffResult.Untracked), untrackedMaps)
			}
			// Continue to call LLM again with tool results — don't fall through to blocking select
			continue
		} else if assistantText != "" {
			// Merge reasoning into content so the model sees its own thinking
			assistantContentFull := assistantText
			if reasoningContent.Len() > 0 {
				assistantContentFull = reasoningContent.String() + "\n" + assistantText
			}
			messages = append(messages, types.ChatMessage{Role: "assistant", Content: assistantContentFull})
			promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs := computeStats(usage, messages[:len(messages)-1], assistantText, reasoningContent.String(), startTime)
			duration := time.Since(startTime).Milliseconds()
			te.store.CreateTaskMessage(&types.TaskMessage{
				ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID, Role: "assistant",
				Content: assistantText, ReasoningContent: reasoningContent.String(),
				ModelName: activeModel, ProviderName: providerName,
				PromptTokens: promptTokens, PredictedTokens: predictedTokens, CacheTokens: cacheTokens,
				PromptMs: promptMs, PredictedMs: predictedMs,
				Duration: duration, CreatedAt: time.Now().UTC(),
			})
			assistantContent.Reset()
			reasoningContent.Reset()

			// Loop detection for text-only responses
			loopType := detector.recordTurn(nil, assistantText)
			// Also check for phrase repetition detected during streaming
			if loopType == LoopNone && detector.phraseDetected() {
				loopType = LoopPhrase
			}
			if loopType != LoopNone {
				if detector.hintCount >= maxLoopHints {
					task.StatusState = "failed"
					task.Output = "Model stuck in loop (max hints exhausted)"
					te.store.UpdateTask(task)
					te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
					te.sendAndPersistError(b, task.ID, "Model stuck in loop (max hints exhausted)", "loop_detected")
					return
				}
				hint := buildHint(loopType, "")
				log.Printf("[loop] task=%s detected loop type=%d (hint %d/%d)", task.ID, loopType, detector.hintCount+1, maxLoopHints)
				messages = append(messages, types.ChatMessage{Role: "system", Content: hint})
				detector.hintCount++
				detector.reset()
				task.StatusState = "running"
				te.store.UpdateTask(task)
				b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Loop detected, steering agent"}})
				continue
			}

			// Check for queued user messages before sending complete.
			select {
			case um := <-userMsgChan:
				log.Printf("[user-msg] task=%s received message after response, continuing", task.ID)
				detector.reset() // user input breaks any loop pattern
				messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
				if um.ModelName != "" {
					mc.set(um.ServerURL, um.ModelName)
					newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
					newAPIKey := te.configStore.GetProviderAPIKey(um.ServerURL)
					client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort, newAPIKey)
					providerName = te.configStore.GetProviderNameByURL(um.ServerURL)
				}
				task.StatusState = "running"
				te.store.UpdateTask(task)
				b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
				continue
			default:
			}
			task.Status = "review"
			task.StatusState = "idle"
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			// Broadcast diff so the frontend updates the diff panel
			baseRef, _ := te.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, "")
			if diffResult, err := te.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil {
				untrackedMaps := make([]map[string]interface{}, len(diffResult.Untracked))
				for i, u := range diffResult.Untracked {
					untrackedMaps[i] = map[string]interface{}{"path": u.Path, "file_size": u.FileSize, "diff": u.Diff}
				}
				te.boardStreamMgr.BroadcastDiff(projectID, task.ID, diffResult.Diff, diffResult.ShortStat, len(diffResult.Untracked), untrackedMaps)
			}
			b.Send(types.StreamEvent{Type: types.WSTypeComplete, Data: map[string]interface{}{
				"state": "idle", "summary": "Agent finished responding",
				"prompt_tokens": promptTokens, "predicted_tokens": predictedTokens,
				"cache_tokens": cacheTokens,
				"prompt_ms": promptMs, "predicted_ms": predictedMs,
				"model_name": activeModel, "duration": duration,
			}})
			// If this is a subtask, auto-start the next sibling
			if task.ParentTaskID != "" {
				_ = te.AutoStartNextSibling(projectID, task.ID, task.ParentTaskID, task.Status)
			}
		}

		// Context compression: check if we're approaching the context window limit
		if contextWindow != nil && lastCompressIdx < len(messages)-1 {
			if shouldCompress(messages, *contextWindow) {
				compressResult, err := te.compressContext(messages, lastCompressIdx, systemPrompt, serverURL, activeModel)
				if err != nil {
					log.Printf("[compress] task=%s compression failed: %v", task.ID, err)
				} else {
					messages = compressResult.newMessages
					lastCompressIdx = compressResult.newLastCompressIdx
					// Persist compression checkpoint message
					_ = te.store.CreateTaskMessage(&types.TaskMessage{
						ID:       uuid.New().String(),
						TaskID:   task.ID,
						ThreadID: task.CurrentThreadID,
						Role:     "compression",
						Content:  compressResult.summary,
						CreatedAt: time.Now().UTC(),
					})
					b.Send(types.StreamEvent{Type: "compression", Data: map[string]string{"summary": compressResult.summary}})
					log.Printf("[compress] task=%s compressed context: %d messages -> %d messages", task.ID, compressResult.oldLen, len(messages))
				}
			}
		}

		// Block waiting for a user message before the next LLM call.
		// Without this, the executor would immediately loop and call the LLM again
		// with the same conversation (text-only response already added to history).
		select {
		case <-ctx.Done():
			task.StatusState = "stopped"
			task.Output = "Stopped by user"
			te.store.UpdateTask(task)
			te.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			te.sendAndPersistError(b, task.ID, "Task stopped", "stopped")
			return
		case um := <-userMsgChan:
			log.Printf("[user-msg] task=%s received message while idle, continuing", task.ID)
			detector.reset()
			messages = append(messages, types.ChatMessage{Role: "user", Content: um.Content})
			if um.ModelName != "" {
				mc.set(um.ServerURL, um.ModelName)
				newReasoningEffort := te.configStore.GetModelReasoningEffort(um.ModelName)
				newAPIKey := te.configStore.GetProviderAPIKey(um.ServerURL)
				client = llama.NewLlamaClient(um.ServerURL, um.ModelName, newReasoningEffort, newAPIKey)
				providerName = te.configStore.GetProviderNameByURL(um.ServerURL)
			}
			task.StatusState = "running"
			te.store.UpdateTask(task)
			b.Send(types.StreamEvent{Type: types.WSTypeStatus, Data: map[string]string{"state": "running", "activity": "Processing new message"}})
		}
	}
}

// sendAndPersistError sends an error event via WebSocket and persists it to the database.
func (te *TaskExecutor) sendAndPersistError(b *stream.Broadcaster, taskID, message, code string) {
	// Get the task to find its current thread
		task, _ := te.store.GetTaskByTaskID(taskID)
		errMsg := &types.TaskMessage{
			ID: uuid.New().String(), TaskID: taskID, Role: "error",
			Content: fmt.Sprintf("[%s] %s", code, message), CreatedAt: time.Now(),
		}
		if task != nil {
			errMsg.ThreadID = task.CurrentThreadID
		}
	if err := te.store.CreateTaskMessage(errMsg); err != nil {
		log.Printf("[error-persist] task=%s failed to persist error: %v", taskID, err)
	}
	b.Send(types.StreamEvent{Type: types.WSTypeError, Data: map[string]string{"message": message, "code": code}})
}

func (te *TaskExecutor) gitRun(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	_, err := c.CombinedOutput()
	return err
}

func computeStats(usage *types.ChatUsage, messages []types.ChatMessage, assistantText, reasoningContent string, startTime time.Time) (promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs int) {
	if usage != nil {
		return usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, int(usage.PromptMs), int(usage.PredictedMs)
	}
	durationMs := int(time.Since(startTime).Milliseconds())
	promptTokens = estimatePromptTokens(messages)
	predictedTokens = estimateTotalTokens(assistantText, reasoningContent)
	cacheTokens = 0
	promptMs = durationMs * 7 / 10
	predictedMs = durationMs * 3 / 10
	return promptTokens, predictedTokens, cacheTokens, promptMs, predictedMs
}

// compressResult holds the result of a context compression operation.
type compressResult struct {
	newMessages       []types.ChatMessage
	newLastCompressIdx int
	summary           string
	oldLen            int
}

// shouldCompress checks if the current context (estimated from messages) is approaching the context window limit.
// Uses the same token estimation as estimatePromptTokens for consistency.
func shouldCompress(messages []types.ChatMessage, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	estimatedTokens := estimatePromptTokens(messages)
	return estimatedTokens >= contextWindow*8/10
}

// compressContext summarizes the conversation history to reduce context size.
// It sends a non-streaming chat completion with a summarization prompt,
// then returns a new messages array: [system, summary] + last N recent messages.
func (te *TaskExecutor) compressContext(messages []types.ChatMessage, lastCompressIdx int, systemPrompt, serverURL, modelName string) (compressResult, error) {
	oldLen := len(messages)

	// Build the summarization request: system prompt + all messages since last compression
	compressFrom := 0
	if lastCompressIdx > 0 {
		compressFrom = lastCompressIdx
	}

	compressMessages := []types.ChatMessage{
		{Role: "system", Content: "Summarize the key events, decisions, and lessons learned in this conversation. " +
			"Preserve technical details, file paths, code decisions, and any important context. " +
			"This summary will be used to restore context in a truncated conversation. " +
			"Be concise but thorough. Include: what was done, what files were changed, what commands were run, " +
			"what errors were encountered, and what the current state of the task is."},
	}
	// Include messages from after last compression (skip system prompt at index 0)
	for i := compressFrom; i < len(messages); i++ {
		compressMessages = append(compressMessages, messages[i])
	}

	// Send non-streaming summarization request (concurrency handled by ChatCompletion)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	_, summary, err := te.ChatCompletion(ctx, serverURL, modelName, compressMessages)
	cancel()
	if err != nil {
		return compressResult{}, fmt.Errorf("summarization call failed: %w", err)
	}

	// Keep last N messages for continuity (system + last ~10 messages = ~5 turns)
	keepRecent := 10
	var recent []types.ChatMessage
	if len(messages) > keepRecent+1 {
		recent = messages[len(messages)-keepRecent:]
	} else {
		recent = messages[1:] // skip system
	}

	// Build new messages: system + summary + recent
	newMessages := []types.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "assistant", Content: "[Context summary: " + summary + "]"},
	}
	newMessages = append(newMessages, recent...)

	// New lastCompressIdx: points to the summary message (index 1)
	newLastCompressIdx := 1

	return compressResult{
		newMessages:        newMessages,
		newLastCompressIdx: newLastCompressIdx,
		summary:            summary,
		oldLen:             oldLen,
	}, nil
}

// shellQuote safely quotes a string for use in a shell command.
// Uses single quotes and escapes any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// isSandboxableTool returns true if the tool can run inside a sandbox.
func isSandboxableTool(name string) bool {
	switch name {
	case "read_file", "write_file", "edit_file", "bash":
		return true
	default:
		return false
	}
}

// createCheckpoint checks for file changes after tool calls and creates a git stash snapshot.
// Uses `git stash create` + `git stash store` to record a checkpoint WITHOUT removing changes
// from the working tree — files stay visible in the filesystem.
// Runs the LLM labeler in a goroutine so it does not block the main execution loop.
func (te *TaskExecutor) createCheckpoint(taskID, worktreePath, threadID, assistantMsgID string, messages []types.ChatMessage, b *stream.Broadcaster, sandboxEnabled bool) {
	// Check for file changes
	statusOut, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "status", "--porcelain")
	if err != nil || len(strings.TrimSpace(statusOut)) == 0 {
		return // no changes
	}

	// Send pending event to frontend
	b.Send(types.StreamEvent{Type: types.WSTypeCheckpointPending, Data: map[string]string{"message_id": assistantMsgID}})

	// Run stash + LLM labeler in background (non-blocking)
	go func() {
		// Get diff stat for the LLM labeler
		diffStat, err := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat", "HEAD")
		if err != nil {
			diffStat = "file changes"
		}

		// Generate commit label via LLM (include branch prefix for stash list filtering)
		taskForLabel, _ := te.store.GetTaskByTaskID(taskID)
		branchPrefix := ""
		if taskForLabel != nil && taskForLabel.Branch != "" {
			branchPrefix = "On " + taskForLabel.Branch + ": "
		}
		label := branchPrefix + te.GenerateCommitLabel(taskID, diffStat, messages)

		// Serialize stash operations per project (stash is repo-wide)
		task, _ := te.store.GetTaskByTaskID(taskID)
		projectID := ""
		if task != nil {
			projectID = task.ProjectID
		}
		var stashHash string
		var stashErr error
		if projectID != "" {
			mu := te.worktreeMgr.GetProjectStashMu(projectID)
			mu.Lock()
			// Stage changes, create stash commit (doesn't remove from working tree), store in stash list
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
			stashCreateOut, createErr := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "stash", "create")
			if createErr == nil {
				stashCreateHash := strings.TrimSpace(stashCreateOut)
				_ = te.worktreeMgr.GitRunInWorktree(taskID, "stash", "store", "-m", label, stashCreateHash)
				stashHash = stashCreateHash
			} else {
				stashErr = createErr
			}
			mu.Unlock()
		} else {
			// Fallback without locking (shouldn't happen)
			_ = te.worktreeMgr.GitRunInWorktree(taskID, "add", "-A")
			stashCreateOut, createErr := te.worktreeMgr.GitRunInWorktreeOutput(taskID, "stash", "create")
			if createErr == nil {
				stashCreateHash := strings.TrimSpace(stashCreateOut)
				_ = te.worktreeMgr.GitRunInWorktree(taskID, "stash", "store", "-m", label, stashCreateHash)
				stashHash = stashCreateHash
			} else {
				stashErr = createErr
			}
		}

		if stashErr != nil {
			log.Printf("[checkpoint] task=%s stash failed: %v", taskID, stashErr)
			b.Send(types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
				"message_id":  assistantMsgID,
				"commit_hash": "",
				"label":       label,
			}})
			return
		}

		// Store stash hash on the assistant message (reuses git_commit_hash field)
		if stashHash != "" {
			_ = te.store.UpdateTaskMessageCommitHash(assistantMsgID, stashHash)
		}

		// Send complete event to frontend
		b.Send(types.StreamEvent{Type: types.WSTypeCheckpointComplete, Data: map[string]string{
			"message_id":  assistantMsgID,
			"commit_hash": stashHash,
			"label":       label,
		}})
	}()
}

// GenerateCommitLabel uses the LLM to generate a descriptive commit label from git diff stat
// and the last 3 conversation turns. Falls back to "{title}, generated by {model}" on any error.
func (te *TaskExecutor) GenerateCommitLabel(taskID, diffStat string, conversation []types.ChatMessage) string {
	task, err := te.store.GetTaskByTaskID(taskID)
	if err != nil || task.LlamaServerURL == "" || task.ModelName == "" {
		return "file changes"
	}
	fallback := fmt.Sprintf("%s, generated by %s", task.Title, task.ModelName)

	// Extract last 3 turns (user + assistant pairs) from conversation
	turns := extractLastTurns(conversation, 3)
	turnText := formatConversationTurns(turns)

	labelMessages := []types.ChatMessage{
		{Role: "system", Content: "You are a commit message generator. Given a git diff --stat output and the conversation that led to the changes, generate a short, descriptive commit label (one line, max 80 characters, imperative mood, no period at end). Return only the label, nothing else."},
		{Role: "user", Content: fmt.Sprintf("Conversation context:\n\n%s\n\nGit diff --stat:\n\n%s", turnText, diffStat)},
	}

	ctx := context.Background()
	_, label, err := te.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, labelMessages)
	if err != nil {
		log.Printf("[checkpoint-label] task=%s LLM call failed: %v", taskID, err)
		return fallback
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fallback
	}
	log.Printf("[checkpoint-label] task=%s generated: %q", taskID, label)
	return label
}

// extractLastTurns extracts the last N turns (user + assistant pairs, including tool results) from a conversation.
func extractLastTurns(messages []types.ChatMessage, n int) []types.ChatMessage {
	// Group messages into turns: each turn starts with a user message and includes
	// all following assistant/tool_call messages until the next user message.
	var turns [][]types.ChatMessage
	var current []types.ChatMessage

	for _, m := range messages {
		if m.Role == "user" && len(current) > 0 {
			turns = append(turns, current)
			current = nil
		}
		// Skip system messages for turn extraction
		if m.Role == "system" {
			continue
		}
		current = append(current, m)
	}
	if len(current) > 0 {
		turns = append(turns, current)
	}

	// Take last N turns
	if len(turns) > n {
		turns = turns[len(turns)-n:]
	}

	// Flatten back into a single slice
	var result []types.ChatMessage
	for _, turn := range turns {
		result = append(result, turn...)
	}
	return result
}

// formatConversationTurns formats a slice of chat messages into readable text.
func formatConversationTurns(messages []types.ChatMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "user":
			sb.WriteString("User: ")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("Assistant: ")
			if m.Content != "" {
				sb.WriteString(m.Content)
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					sb.WriteString(fmt.Sprintf("[tool: %s %s]\n", tc.Function.Name, tc.Function.Arguments))
				}
			}
			sb.WriteString("\n\n")
		case "tool":
			// Tool result — include truncated output
			if m.Content != "" {
				sb.WriteString("[Tool result]")
				if len(m.Content) > 500 {
					sb.WriteString(m.Content[:500] + "...")
				} else {
					sb.WriteString(m.Content)
				}
				sb.WriteString("\n\n")
			}
		}
	}
	return sb.String()
}
