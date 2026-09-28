package exec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"mike/backend/types"
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

// countEditLines counts lines added and removed from edit_file tool args.
// Parses the (possibly partial) JSON to extract oldText/newText from edits array.
// Returns (added, removed) line counts.
func countEditLines(args string) (added, removed int) {
	// Try parsed JSON first (works when full edits array is available)
	parsed := parseToolArgs(args)
	if parsed != nil {
		if edits, ok := parsed["edits"].([]any); ok {
			for _, e := range edits {
				if editMap, ok := e.(map[string]any); ok {
					if oldText, ok := editMap["oldText"].(string); ok {
						removed += strings.Count(oldText, "\n")
					}
					if newText, ok := editMap["newText"].(string); ok {
						added += strings.Count(newText, "\n")
					}
				}
			}
		}
	}
	return added, removed
}

// LoopType indicates the type of loop detected.
type LoopType int

const (
	LoopNone          LoopType = iota
	LoopTool                   // repeated read of same target
	LoopText                   // repeated similar text-only responses
	LoopPhrase                 // exact phrase repetition within a response
	LoopReasoningOnly          // reasoning-only response with no content or tool calls
	LoopStatement              // text-only response without ask_question or request_review
)

const maxLoopHints = 3

// loopDetector tracks tool call targets and text responses to detect LLM loops.
type loopDetector struct {
	lastReadToolName string          // tool name of the most recent read-only call
	lastReadArgs     string          // raw args JSON of the most recent read-only call
	textWindow       []string        // recent text-only responses (content, trimmed)
	loopStreak       int             // consecutive loop detections (reset to 0 on valid turn)
	phraseDet        *phraseDetector // real-time phrase repetition tracker
	blockDet         *blockDetector  // real-time block repetition tracker
	phraseFired      bool            // phrase loop detected during current streaming response
	hintIndices      []int           // indices in messages slice of injected loop hints
}

// newLoopDetector creates a new loopDetector.
func newLoopDetector() *loopDetector {
	return &loopDetector{
		textWindow: make([]string, 0, 8),
		phraseDet:  newPhraseDetector(),
		blockDet:   newBlockDetector(),
	}
}

// reset clears all tracked state (called on user message or after hint injection).
// Does NOT clear hintIndices — those are preserved until hints are removed from context.
func (d *loopDetector) reset() {
	d.lastReadToolName = ""
	d.lastReadArgs = ""
	d.textWindow = d.textWindow[:0]
	d.loopStreak = 0
	d.phraseDet.reset()
	d.blockDet.reset()
	d.phraseFired = false
}

// clearHints removes tracked hint indices (called on user message / context change).
func (d *loopDetector) clearHints() {
	d.hintIndices = d.hintIndices[:0]
}

// recordHintIndex tracks the position of an injected loop hint in the messages slice.
func (d *loopDetector) recordHintIndex(idx int) {
	d.hintIndices = append(d.hintIndices, idx)
}

// removeHints removes tracked hint messages from the conversation and clears the tracker.
// Returns the cleaned messages slice.
func (d *loopDetector) removeHints(messages []types.ChatMessage) []types.ChatMessage {
	if len(d.hintIndices) == 0 {
		return messages
	}
	// Build a set of indices to remove for O(1) lookup
	remove := make(map[int]bool, len(d.hintIndices))
	for _, i := range d.hintIndices {
		remove[i] = true
	}
	// Filter out hint messages, preserving order
	cleaned := make([]types.ChatMessage, 0, len(messages)-len(remove))
	for i, m := range messages {
		if !remove[i] {
			cleaned = append(cleaned, m)
		}
	}
	d.hintIndices = d.hintIndices[:0]
	return cleaned
}

// resetText clears only the text window (called after a tool call breaks text-only streak).
func (d *loopDetector) resetText() {
	d.textWindow = d.textWindow[:0]
}

// asksInLastParagraph reports whether a question mark appears in the final paragraph of a
// text-only response, paragraphs being the blank-line-separated blocks of markdown text.
// Only the closing paragraph can be waiting for the user: a response that asked something
// two paragraphs ago and ends in a statement is a statement. A fenced code block is text
// like any other.
func asksInLastParagraph(text string) bool {
	lines := strings.Split(text, "\n")
	inParagraph := false
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], " \t\r")
		if line == "" {
			if inParagraph {
				break // the blank line above the paragraph is the separator
			}
			continue // the response's trailing whitespace
		}
		inParagraph = true
		for _, r := range line {
			if isQuestionMark(r) {
				return true
			}
		}
	}
	return false
}

// isQuestionMark covers the question-mark code points rather than ASCII alone: a response
// written in another script asks with its own mark.
func isQuestionMark(r rune) bool {
	switch r {
	case '?', '\u00BF', '\u037E', '\u055E', '\u061F', '\u109E',
		'\u2047', '\u2048', '\u2049', '\uFE16', '\uFE56', '\uFF1F':
		return true
	}
	return false
}

// recordTurn records an LLM turn and returns the loop type if detected.
// toolCalls: the tool calls made this turn (nil or empty for text-only).
// assistantText: the text content of the response.
// finishReason: the LLM's finish_reason ("stop", "tool_calls", etc.).
// Returns (loopType, rawArgs) where rawArgs is the argument text of the repeated call.
func (d *loopDetector) recordTurn(toolCalls []types.ChatToolCall, assistantText, finishReason string) (LoopType, string) {
	hasToolCalls := len(toolCalls) > 0 && toolCalls[0].Function.Name != ""

	if hasToolCalls {
		d.resetText()       // tool call breaks text-only streak
		d.phraseDet.reset() // tool call resets phrase tracker
		d.blockDet.reset()  // tool call resets block tracker

		// Check each tool call
		for _, tc := range toolCalls {
			if tc.Function.Name == "" {
				continue
			}

			if isReadOnlyCall(tc.Function.Name, tc.Function.Arguments) {
				// Read-only call: check if it repeats the last read (same tool + same args)
				if d.lastReadToolName != "" && tc.Function.Name == d.lastReadToolName && tc.Function.Arguments == d.lastReadArgs {
					return LoopTool, tc.Function.Arguments
				}
				d.lastReadToolName = tc.Function.Name
				d.lastReadArgs = tc.Function.Arguments
			} else {
				// State-changing call: clear the read tracker
				d.lastReadToolName = ""
				d.lastReadArgs = ""
			}
		}
	} else if strings.TrimSpace(assistantText) != "" {
		// Text-only response: only trigger LoopStatement when the LLM is
		// explicitly done (finish_reason="stop"). If the stream ended for
		// another reason (e.g., tool_calls with no valid calls), don't penalize.
		if finishReason != "stop" {
			d.loopStreak = 0
			return LoopNone, ""
		}
		if asksInLastParagraph(assistantText) {
			d.loopStreak = 0
			return LoopNone, ""
		}
		return LoopStatement, ""
	}

	// No loop detected: reset consecutive loop counter
	d.loopStreak = 0

	return LoopNone, ""
}

// checkStreamingText feeds accumulated streaming text into the phrase and block detectors.
// Sets phraseFired flag and returns LoopPhrase if exact phrase repetition is detected.
func (d *loopDetector) checkStreamingText(text string) LoopType {
	if d.phraseDet.check(text) {
		d.phraseFired = true
		return LoopPhrase
	}
	if d.blockDet.check(text) {
		d.phraseFired = true // Treat block loop as phrase loop for reporting/hints
		return LoopPhrase
	}
	return LoopNone
}

// phraseDetected returns true if phrase repetition was detected during the current stream.
func (d *loopDetector) phraseDetected() bool {
	return d.phraseFired
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

// isReadOnlyCall classifies a tool call as read-only (no state changes) or state-changing.
// This is not used in a security context, only used to infer if the model is either reading
// or writing in order to provide hints.
func isReadOnlyCall(toolName, rawArgs string) bool {
	switch toolName {
	case types.ToolReadFile:
		return true
	case types.ToolLSPReferences, types.ToolLSPDefinition, types.ToolLSPHover:
		return true
	case types.ToolWriteFile, types.ToolEditFile, types.ToolLSPRename:
		return false
	case types.ToolAskQuestion, types.ToolRequestReview:
		return false
	case types.ToolBash:
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
		return "You've produced several similar responses without calling any tools. Take action \u2014 call a tool to make progress, or use `request_review` if the task is done."
	case LoopStatement:
		return "Is the task complete? Use `ask_question` if clarification/approval is needed, or `request_review` if the task is complete. Otherwise use `read_file`, `write_file`, `edit_file`, or `bash` to make progress."
	case LoopPhrase:
		return "You repeated yourself in your last response. Continue with a new action or tool call."
	case LoopReasoningOnly:
		return "Your last response contained only internal reasoning with no visible text or tool calls. You must respond with actual content (a text message) or call a tool to take action. Available tools: read_file, write_file, edit_file, bash, ask_question, request_review. Use the function calling format to invoke tools — do not describe tool calls as plain text."
	default:
		return ""
	}
}
