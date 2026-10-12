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
// Tries regex, then parsed JSON, then double-encoded JSON.
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
// Does NOT clear hintIndices.
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

// recordHintIndex tracks the position of an injected loop hint.
func (d *loopDetector) recordHintIndex(idx int) {
	d.hintIndices = append(d.hintIndices, idx)
}

// removeHints removes tracked hint messages from the conversation and clears the tracker.
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

// asksInLastParagraph reports whether a question mark appears in the final paragraph.
// Only the closing paragraph can be waiting for the user.
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

// isQuestionMark covers question-mark code points for multiple scripts.
func isQuestionMark(r rune) bool {
	switch r {
	case '?', '\u00BF', '\u037E', '\u055E', '\u061F', '\u109E',
		'\u2047', '\u2048', '\u2049', '\uFE16', '\uFE56', '\uFF1F':
		return true
	}
	return false
}

// recordTurn records an LLM turn and returns the loop type if detected.
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

// checkStreamingText feeds accumulated streaming text into detectors.
// Returns LoopPhrase if repetition is detected.
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

// The separator tokens shellTokens emits between words. A segment separator starts a new command; ">" does
// not, it stays inside its segment where it says that segment writes a file.
const (
	sepPipe    = "|"
	sepAnd     = "&&"
	sepSemi    = ";"
	redirToken = ">"
)

// shellTokens splits a shell command into words and separator tokens.
// Handles quotes and escaped characters.
func shellTokens(cmd string) []string {
	rs := []rune(cmd)
	var tokens []string
	current := strings.Builder{}
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	inSingle, inDouble := false, false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && !inSingle && i+1 < len(rs) {
			i++
			current.WriteRune(rs[i])
			continue
		}
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				current.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else {
				current.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
		case r == '"':
			inDouble = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		case r == '|' || r == '&' || r == ';' || r == '>':
			flush()
			for i+1 < len(rs) && rs[i+1] == r {
				i++
			}
			tokens = append(tokens, separatorToken(r))
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// separatorToken names the token for a run of one shell operator rune.
func separatorToken(r rune) string {
	switch r {
	case '|':
		return sepPipe
	case '&':
		return sepAnd
	case ';':
		return sepSemi
	default:
		return redirToken
	}
}

// isReadOnlyCall classifies a tool call as read-only or state-changing.
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

// readOnlyCommands and stateChangingCommands are the two known vocabularies of verbs.
// Verbs in neither count as read-only.
var readOnlyCommands = []string{
	"head", "tail", "cat", "wc", "ls", "stat", "file", "diff",
	"grep", "find", "echo", "printf", "pwd", "whoami", "uname",
	"date", "which", "type", "command", "test", "du", "df",
	"tree", "xxd", "od", "hexdump", "strings", "tr", "cut",
	"sort", "uniq", "comm", "fold", "join", "paste", "split",
	"basename", "dirname", "realpath", "readlink", "mktemp",
}

var stateChangingCommands = []string{
	"python", "python3", "node", "npm", "pip", "cargo", "go",
	"make", "gcc", "g++", "rustc", "javac", "ruby", "perl",
	"sed", "awk", "vim", "nano", "emacs", "touch",
	"mv", "cp", "rm", "mkdir", "rmdir", "chmod", "chown",
	"install", "curl", "wget", "apt", "yum", "dnf", "brew",
	"git", "docker", "kubectl", "terraform", "ansible",
}

// isReadOnlyCommand reports whether every command on a shell line only reads.
// Each segment (separated by |, &&, ;) is judged individually.
func isReadOnlyCommand(cmd string) bool {
	tokens := shellTokens(cmd)
	for start := 0; ; {
		end := start
		for end < len(tokens) && !endsSegment(tokens[end]) {
			end++
		}
		if !readOnlySegment(tokens[start:end]) {
			return false
		}
		if end == len(tokens) {
			return true
		}
		start = end + 1
	}
}

// endsSegment reports whether tok starts a new command.
func endsSegment(tok string) bool {
	return tok == sepPipe || tok == sepAnd || tok == sepSemi
}

// readOnlySegment judges one command: a redirect writes a file, otherwise the first word decides.
func readOnlySegment(segment []string) bool {
	for _, t := range segment {
		if t == redirToken {
			return false
		}
	}
	if len(segment) == 0 {
		return true
	}
	verb := segment[0]
	if slices.Contains(readOnlyCommands, verb) {
		return true
	}
	return !slices.Contains(stateChangingCommands, verb)
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
