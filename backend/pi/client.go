package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"kanban/backend/types"
)

// Client manages a `pi --mode rpc` subprocess and communicates via JSONL protocol.
// Each task gets its own Client instance.
type Client struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stdoutBuf *bufio.Scanner

	mu          sync.Mutex
	closed      bool
	eventChan   chan PiEvent
	done        chan struct{}
	cwd         string
	env         []string
	sessionName string
}

// PiEvent represents an event from the Pi RPC protocol.
type PiEvent struct {
	Type              string                 `json:"type"`                  // agent_start, agent_end, message_update, tool_execution_*, etc.
	AssistantEventType string                 `json:"assistantMessageEvent"` // nested event type for message_update
	TextDelta         string                 `json:"-"`                     // extracted text delta
	ThinkingDelta     string                 `json:"-"`                     // extracted thinking delta
	ToolName          string                 `json:"toolName"`              // from tool_execution_*
	ToolCallID        string                 `json:"toolCallId"`            // from tool_execution_*
	ToolArgs          map[string]interface{} `json:"args"`                  // from tool_execution_start
	ToolResult        string                 `json:"-"`                     // extracted tool result text
	ToolIsError       bool                   `json:"isError"`               // from tool_execution_end
	Messages          []map[string]interface{} `json:"messages"`            // from agent_end
	StopReason        string                 `json:"-"`                     // extracted from last assistant message
	Error             string                 `json:"error"`                 // from response errors
	Success           bool                   `json:"success"`               // from response
	Command           string                 `json:"command"`               // from response
	ResponseID        string                 `json:"id"`                    // from response (correlation id)
	// Extension UI
	ExtMethod string `json:"method"` // from extension_ui_request
	ExtID     string `json:"-"`      // from extension_ui_request (id field)
}

// NewClient creates a new Pi RPC client. Spawns `pi --mode rpc` in the given working directory.
// sessionName sets the display name for the Pi session (--name flag).
// env sets additional environment variables (e.g., PI_CODING_AGENT_DIR).
func NewClient(cwd string, sessionName string, env []string) (*Client, error) {
	c := &Client{
		cwd:         cwd,
		env:         env,
		sessionName: sessionName,
		eventChan:   make(chan PiEvent, 64),
		done:        make(chan struct{}),
	}

	// Build command arguments
	args := []string{"--mode", "rpc"}
	if sessionName != "" {
		args = append(args, "--name", sessionName)
	}

	c.cmd = exec.Command("pi", args...)
	c.cmd.Dir = cwd

	// Set environment: inherit current env + overrides
	c.cmd.Env = append(os.Environ(), c.env...)

	// Setup pipes
	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	c.stdout, err = c.cmd.StdoutPipe()
	if err != nil {
		c.stdin.Close()
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	// Merge stderr into logs
	c.cmd.Stderr = nil // stderr goes to parent's stderr (or we could capture it)

	if err := c.cmd.Start(); err != nil {
		c.stdin.Close()
		c.stdout.Close()
		return nil, fmt.Errorf("start pi process: %w", err)
	}

	c.stdoutBuf = bufio.NewScanner(c.stdout)
	buf := make([]byte, 0, 64*1024)
	c.stdoutBuf.Buffer(buf, 1024*1024)

	// Start stdout reader goroutine
	go c.readEvents()

	log.Printf("[pi] client started (pid=%d, cwd=%s, session=%s)", c.cmd.Process.Pid, cwd, sessionName)
	return c, nil
}

// EventChan returns the channel for receiving Pi events.
func (c *Client) EventChan() <-chan PiEvent {
	return c.eventChan
}

// PID returns the subprocess PID.
func (c *Client) PID() int {
	if c.cmd.Process == nil {
		return -1
	}
	return c.cmd.Process.Pid
}

// Prompt sends a user prompt to Pi.
// Returns immediately after the prompt is accepted. Events stream via EventChan().
func (c *Client) Prompt(ctx context.Context, message string) error {
	cmd := map[string]interface{}{
		"type":    "prompt",
		"message": message,
		"id":      fmt.Sprintf("prompt-%d", time.Now().UnixNano()),
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshal prompt: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("pi client closed")
	}

	_, err = c.stdin.Write(append(data, '\n'))
	if err != nil {
		return fmt.Errorf("write prompt: %w", err)
	}

	log.Printf("[pi] prompt sent (len=%d)", len(message))
	return nil
}

// Abort sends an abort command to Pi and waits for it to stop.
func (c *Client) Abort(ctx context.Context) error {
	cmd := map[string]interface{}{
		"type": "abort",
	}
	data, _ := json.Marshal(cmd)

	c.mu.Lock()
	if !c.closed {
		c.stdin.Write(append(data, '\n')) // best-effort
	}
	c.mu.Unlock()

	// Wait briefly for graceful shutdown
	done := make(chan struct{})
	go func() {
		c.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		c.kill()
		<-done
	case <-time.After(5 * time.Second):
		c.kill()
		<-done
	}
	return nil
}

// Close gracefully shuts down the Pi subprocess.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true

	// Close stdin to signal EOF
	c.stdin.Close()

	// Wait for process to exit (with timeout)
	done := make(chan error, 1)
	go func() {
		done <- c.cmd.Wait()
	}()

	select {
	case err := <-done:
		log.Printf("[pi] client closed (pid=%d, err=%v)", c.PID(), err)
	case <-time.After(3 * time.Second):
		log.Printf("[pi] close timeout, killing pid=%d", c.PID())
		c.cmd.Process.Kill()
		<-done
	}

	close(c.eventChan)
	close(c.done)
}

// kill forcefully terminates the process.
func (c *Client) kill() {
	if c.cmd.Process != nil {
		c.cmd.Process.Signal(syscall.SIGKILL)
		c.cmd.Wait()
	}
}

// sendExtensionUIResponse sends a response to an extension UI request (e.g., cancel a dialog).
func (c *Client) sendExtensionUIResponse(extID string, cancelled bool) {
	resp := map[string]interface{}{
		"type":     "extension_ui_response",
		"id":       extID,
		"cancelled": cancelled,
	}
	data, _ := json.Marshal(resp)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.stdin.Write(append(data, '\n'))
	}
}

// readEvents reads JSONL lines from stdout and dispatches events.
func (c *Client) readEvents() {
	defer func() {
		c.stdout.Close()
	}()

	for c.stdoutBuf.Scan() {
		line := strings.TrimSpace(c.stdoutBuf.Text())
		if line == "" {
			continue
		}

		// Strip trailing \r for cross-platform compatibility
		line = strings.TrimRight(line, "\r")

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			log.Printf("[pi] parse error: %v (line=%q)", err, line[:min(len(line), 200)])
			continue
		}

		eventType, _ := raw["type"].(string)
		if eventType == "" {
			continue
		}

		event := PiEvent{
			Type: eventType,
		}

		// Extract common fields
		if id, ok := raw["id"].(string); ok {
			event.ResponseID = id
		}

		switch eventType {
		case "response":
			event.Success = false
			if success, ok := raw["success"].(bool); ok {
				event.Success = success
			}
			if command, ok := raw["command"].(string); ok {
				event.Command = command
			}
			if err, ok := raw["error"].(string); ok {
				event.Error = err
			}

		case "message_update":
			// Extract assistantMessageEvent
			if ame, ok := raw["assistantMessageEvent"].(map[string]interface{}); ok {
				if etype, ok := ame["type"].(string); ok {
					event.AssistantEventType = etype
					switch etype {
					case "text_delta":
						if delta, ok := ame["delta"].(string); ok {
							event.TextDelta = delta
						}
					case "thinking_delta":
						if delta, ok := ame["delta"].(string); ok {
							event.ThinkingDelta = delta
						}
					}
				}
			}

		case "tool_execution_start":
			if toolName, ok := raw["toolName"].(string); ok {
				event.ToolName = toolName
			}
			if toolCallID, ok := raw["toolCallId"].(string); ok {
				event.ToolCallID = toolCallID
			}
			if args, ok := raw["args"].(map[string]interface{}); ok {
				event.ToolArgs = args
			}

		case "tool_execution_end":
			if toolName, ok := raw["toolName"].(string); ok {
				event.ToolName = toolName
			}
			if toolCallID, ok := raw["toolCallId"].(string); ok {
				event.ToolCallID = toolCallID
			}
			if isError, ok := raw["isError"].(bool); ok {
				event.ToolIsError = isError
			}
			if result, ok := raw["result"].(map[string]interface{}); ok {
				// Extract text content from result
				if content, ok := result["content"].([]interface{}); ok {
					var texts []string
					for _, item := range content {
						if cm, ok := item.(map[string]interface{}); ok {
							if cm["type"] == "text" {
								if text, ok := cm["text"].(string); ok {
									texts = append(texts, text)
								}
							}
						}
					}
					event.ToolResult = strings.Join(texts, "\n")
				}
			}

		case "agent_end":
			if messages, ok := raw["messages"].([]interface{}); ok {
				event.Messages = make([]map[string]interface{}, 0, len(messages))
				for _, m := range messages {
					if mm, ok := m.(map[string]interface{}); ok {
						event.Messages = append(event.Messages, mm)
					}
				}
				// Extract stop reason from last assistant message
				if len(event.Messages) > 0 {
					lastMsg := event.Messages[len(event.Messages)-1]
					if role, ok := lastMsg["role"].(string); ok && role == "assistant" {
						if stopReason, ok := lastMsg["stopReason"].(string); ok {
							event.StopReason = stopReason
						}
					}
				}
			}

		case "extension_ui_request":
			if method, ok := raw["method"].(string); ok {
				event.ExtMethod = method
			}
			if extID, ok := raw["id"].(string); ok {
				event.ExtID = extID
			}

		default:
			// Pass through other events (agent_start, turn_start, etc.)
			break
		}

		// Auto-cancel extension UI dialogs (Pi asking user questions)
		// This causes Pi to stop and move to Review
		if eventType == "extension_ui_request" && event.ExtID != "" {
			log.Printf("[pi] extension_ui_request method=%s, auto-cancelling", event.ExtMethod)
			c.sendExtensionUIResponse(event.ExtID, true)
		}

		// Send to event channel (non-blocking to avoid blocking the reader)
		select {
		case c.eventChan <- event:
		default:
			log.Printf("[pi] event channel full, dropping event type=%s", eventType)
		}
	}

	// Scanner ended (EOF or error)
	if err := c.stdoutBuf.Err(); err != nil && err != io.EOF {
		log.Printf("[pi] stdout read error: %v", err)
	}
}

// ToStreamEvent converts a PiEvent to a Kanban StreamEvent for WebSocket broadcasting.
func ToStreamEvent(event PiEvent) types.StreamEvent {
	switch event.Type {
	case "message_update":
		switch event.AssistantEventType {
		case "text_delta":
			return types.StreamEvent{
				Type: types.WSTypeToken,
				Data: map[string]string{"text": event.TextDelta},
			}
		case "thinking_delta":
			return types.StreamEvent{
				Type: types.WSTypeReasoning,
				Data: map[string]string{"text": event.ThinkingDelta},
			}
		}

	case "tool_execution_start":
		return types.StreamEvent{
			Type: types.WSTypeToolCall,
			Data: map[string]interface{}{
				"tool_id":   event.ToolCallID,
				"tool_name": event.ToolName,
				"args":      event.ToolArgs,
				"sandbox":   false, // Pi tools run in-process, not sandboxed separately
			},
		}

	case "tool_execution_end":
		return types.StreamEvent{
			Type: types.WSTypeToolResult,
			Data: map[string]interface{}{
				"tool_id": event.ToolCallID,
				"success": !event.ToolIsError,
				"output":  event.ToolResult,
			},
		}

	case "agent_start":
		return types.StreamEvent{
			Type: types.WSTypeStatus,
			Data: map[string]string{"state": "running", "activity": "Pi agent started"},
		}

	case "agent_end":
		state := "idle"
		activity := "Pi agent finished"
		if event.StopReason == "toolUse" {
			// Pi is waiting for tool results — shouldn't happen since Pi runs its own tools
			state = "running"
			activity = "Pi agent continuing"
		}
		return types.StreamEvent{
			Type: types.WSTypeStatus,
			Data: map[string]string{"state": state, "activity": activity},
		}

	case "extension_ui_request":
		return types.StreamEvent{
			Type: types.WSTypeStatus,
			Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Pi requested: %s", event.ExtMethod)},
		}

	case "response":
		if !event.Success && event.Error != "" {
			return types.StreamEvent{
				Type: types.WSTypeError,
				Data: map[string]string{"message": event.Error, "code": "pi_rpc_error"},
			}
		}

	default:
		// Pass through unknown events as status
		return types.StreamEvent{
			Type: types.WSTypeStatus,
			Data: map[string]string{"state": "running", "activity": fmt.Sprintf("Pi: %s", event.Type)},
		}
	}

	return types.StreamEvent{}
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
