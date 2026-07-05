package exec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"kanban/backend/types"
)

var pathRegex = regexp.MustCompile(`"path"\s*:\s*"([^"]+)"`)

// extractPathFromArgs extracts the "path" value from (possibly partial) JSON args.
// Tries regex on raw string, then parsed JSON, then double-encoded JSON.
func extractPathFromArgs(args string) string {
	// Try regex on raw string (works when "path" key has appeared in stream)
	matches := pathRegex.FindStringSubmatch(args)
	if len(matches) > 1 {
		return matches[1]
	}
	// Try parsed JSON (works when full JSON is available)
	if parsed := parseToolArgs(args); parsed != nil {
		if p, ok := parsed["path"].(string); ok && p != "" {
			return p
		}
	}
	// Try double-encoded JSON (some LLMs send escaped JSON)
	var inner string
	if err := json.Unmarshal([]byte(args), &inner); err == nil && inner != "" {
		matches2 := pathRegex.FindStringSubmatch(inner)
		if len(matches2) > 1 {
			return matches2[1]
		}
		if parsed := parseToolArgs(inner); parsed != nil {
			if p, ok := parsed["path"].(string); ok && p != "" {
				return p
			}
		}
	}
	return ""
}

// LoopType indicates the type of loop detected.
type LoopType int

const (
	LoopNone          LoopType = iota
	LoopTool                  // repeated read of same target
	LoopText                  // repeated similar text-only responses
	LoopPhrase                // exact phrase repetition within a response
	LoopReasoningOnly         // reasoning-only response with no content or tool calls
)

const maxLoopHints = 3

// loopDetector tracks tool call targets and text responses to detect LLM loops.
type loopDetector struct {
	lastReadToolName string        // tool name of the most recent read-only call
	lastReadArgs     string        // raw args JSON of the most recent read-only call
	lastReadTime     time.Time     // when the last read occurred
	textWindow       []string      // recent text-only responses (content, trimmed)
	hintCount        int           // total hints injected for this task
	phraseDet        *phraseDetector // real-time phrase repetition tracker
	phraseFired      bool          // phrase loop detected during current streaming response
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
	d.lastReadToolName = ""
	d.lastReadArgs = ""
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
// Returns (loopType, toolName, rawArgs) where toolName/rawArgs identify the repeated call.
func (d *loopDetector) recordTurn(toolCalls []types.ChatToolCall, assistantText string) (LoopType, string, string) {
	hasToolCalls := len(toolCalls) > 0 && toolCalls[0].Function.Name != ""

	if hasToolCalls {
		d.resetText()       // tool call breaks text-only streak
		d.phraseDet.reset() // tool call resets phrase tracker

		// Check each tool call
		for _, tc := range toolCalls {
			if tc.Function.Name == "" {
				continue
			}

			if isReadOnlyCall(tc.Function.Name, tc.Function.Arguments) {
				// Read-only call: check if it repeats the last read (same tool + same args)
				if d.lastReadToolName != "" && tc.Function.Name == d.lastReadToolName && tc.Function.Arguments == d.lastReadArgs {
					return LoopTool, tc.Function.Name, tc.Function.Arguments
				}
				d.lastReadToolName = tc.Function.Name
				d.lastReadArgs = tc.Function.Arguments
				d.lastReadTime = time.Now()
			} else {
				// State-changing call: clear the read tracker
				d.lastReadToolName = ""
				d.lastReadArgs = ""
				d.lastReadTime = time.Time{}
			}
		}
	} else if strings.TrimSpace(assistantText) != "" {
		// Text-only response: add to window and check for repetition
		trimmed := strings.TrimSpace(assistantText)
		d.textWindow = append(d.textWindow, trimmed)
		if len(d.textWindow) >= 2 {
			if d.detectTextLoop() {
				return LoopText, "", ""
			}
		}
	}

	return LoopNone, "", ""
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
		var args map[string]any
		if err := json.Unmarshal([]byte(rawArgs), &args); err == nil {
			if p, ok := args["path"].(string); ok {
				return normalizeTarget(p)
			}
		}
	case "bash":
		var args map[string]any
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
		var args map[string]any
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

	if slices.Contains(readOnlyCommands, verb) {
		return true
	}

	stateChanging := []string{
		"python", "python3", "node", "npm", "pip", "cargo", "go",
		"make", "gcc", "g++", "rustc", "javac", "ruby", "perl",
		"sed", "awk", "vim", "nano", "emacs", "touch",
		"mv", "cp", "rm", "mkdir", "rmdir", "chmod", "chown",
		"install", "curl", "wget", "apt", "yum", "dnf", "brew",
		"git", "docker", "kubectl", "terraform", "ansible",
	}
	return !slices.Contains(stateChanging, verb)
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
	case LoopReasoningOnly:
		return "Your last response contained only internal reasoning with no visible text or tool calls. You must respond with actual content (a text message) or call a tool to take action. Available tools: read_file, write_file, edit_file, bash, task_complete, create_task. Use the function calling format to invoke tools — do not describe tool calls as plain text."
	default:
		return ""
	}
}
