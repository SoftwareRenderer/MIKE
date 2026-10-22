package exec

import (
	"strings"
	"unicode/utf8"

	"mike/backend/types"
)

// A summarization's dropped turns are reproduced as text, not kept as their rows. A resumed run reads its
// conversation from the newest summarization row, so rows before that marker would be sent live and lost there.

// The reproduction's caps, in estimated tokens.
const (
	recentTurnCount   = 3
	recentContextCap  = 1500
	recentThinkingCap = 400
	recentSpeechCap   = 400
	recentArgsCap     = 160
	recentQuoteCap    = 120
	recentUserCap     = 200
)

// The markers name the end that was dropped, so an abbreviated quote is not read as the whole of it.
const (
	droppedEarlier = "[… earlier dropped] "
	droppedLater   = " […]"
)

// keepHead keeps the start of text inside target tokens and marks where it cut.
func keepHead(text string, target int) string {
	// The marker spends part of the room the cut frees, or the line comes back over target.
	room := target - estimateTokensFromText(droppedLater)
	if room <= 0 || estimateTokensFromText(text) <= target {
		return text
	}
	return cutToTokens(text, room) + droppedLater
}

// keepTail keeps the end of text inside target tokens, because a reasoning block's last lines are the ones
// that name its next move.
func keepTail(text string, target int) string {
	room := target - estimateTokensFromText(droppedEarlier)
	if room <= 0 || estimateTokensFromText(text) <= target {
		return text
	}
	words := wordsIn(room)
	bytes := room * minBytesPerToken
	lines := strings.Split(text, "\n")
	start := len(lines)
	for start > 0 {
		n := len(strings.Fields(lines[start-1]))
		size := len(lines[start-1]) + 1
		if n > words || size > bytes {
			break
		}
		words -= n
		bytes -= size
		start--
	}
	if start < len(lines) {
		return droppedEarlier + strings.Join(lines[start:], "\n")
	}
	// The last line is over budget on its own: keep its last bytes, then charge the words inside them.
	line := lines[len(lines)-1]
	if words <= 0 {
		return droppedEarlier // no room left for even one word
	}
	begin := max(0, len(line)-bytes)
	for begin > 0 && !utf8.RuneStart(line[begin]) {
		begin++
	}
	cut := line[begin:]
	if len(strings.Fields(cut)) > words {
		if idx := wordRe.FindAllStringIndex(cut, -1); len(idx) > words {
			cut = cut[idx[len(idx)-words][0]:]
		}
	}
	return droppedEarlier + cut
}

// recentTurnWindow is the last recentTurnCount assistant turns and the message the oldest of them answers.
// It cuts on an assistant turn, so a reproduced result never arrives without the call that asked for it.
func recentTurnWindow(messages []types.ChatMessage) []types.ChatMessage {
	start, seen := 0, 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		seen++
		if seen == recentTurnCount {
			start = i
			break
		}
	}
	if seen < recentTurnCount {
		return messages
	}
	if start > 0 && messages[start-1].Role == "user" {
		start--
	}
	return messages[start:]
}

// turnGroups splits a window into the units the cap drops oldest-first: one user message, or one assistant
// turn with the rows that answered it.
func turnGroups(window []types.ChatMessage) [][]types.ChatMessage {
	var groups [][]types.ChatMessage
	for _, m := range window {
		if m.Role == "user" || m.Role == "assistant" || len(groups) == 0 {
			groups = append(groups, []types.ChatMessage{m})
			continue
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], m)
	}
	return groups
}

// renderTurn is one group as the model reads it: what it was thinking, what it said, each call it made and
// what that call answered. A row outside these roles renders nothing, which keeps the prompt out of it.
func renderTurn(group []types.ChatMessage) string {
	answered := map[string]bool{}
	for _, m := range group {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	var sb strings.Builder
	for _, m := range group {
		switch m.Role {
		case "user":
			sb.WriteString("User: " + keepHead(textOf(m), recentUserCap) + "\n")
		case "advisory":
			sb.WriteString("Note: " + keepHead(m.Content, recentQuoteCap) + "\n")
		case "tool":
			sb.WriteString("  Result: " + keepHead(m.Content, recentQuoteCap) + "\n")
		case "assistant":
			if m.ReasoningContent != "" {
				sb.WriteString("Thinking: " + keepTail(m.ReasoningContent, recentThinkingCap) + "\n")
			}
			if text := strings.TrimSpace(m.Content); text != "" {
				sb.WriteString("Assistant: " + keepHead(text, recentSpeechCap) + "\n")
			}
			for _, tc := range m.ToolCalls {
				sb.WriteString("  Called " + tc.Function.Name + ": " + keepHead(tc.Function.Arguments, recentArgsCap) + "\n")
				if !answered[tc.ID] {
					sb.WriteString("  Result: (none — the run was cut off here)\n")
				}
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// renderRecentContext reproduces the turns a summarization is about to drop, oldest first, inside
// recentContextCap estimated tokens. It reads the conversation as the model sees it.
func renderRecentContext(messages []types.ChatMessage) string {
	var blocks []string
	for _, g := range turnGroups(recentTurnWindow(messages)) {
		if b := renderTurn(g); b != "" {
			blocks = append(blocks, b)
		}
	}
	// The cap spends the oldest turn first; a turn over it alone is cut from its front.
	text := strings.Join(blocks, "\n\n")
	for len(blocks) > 1 && estimateTokensFromText(text) > recentContextCap {
		blocks = blocks[1:]
		text = strings.Join(blocks, "\n\n")
	}
	if estimateTokensFromText(text) > recentContextCap {
		text = keepTail(text, recentContextCap)
	}
	return strings.TrimSpace(text)
}

// composeContextCutNote is what the model reads where its conversation was cut.
func composeContextCutNote(tail string) string {
	if strings.TrimSpace(tail) == "" {
		return ""
	}
	return "Your conversation was compacted into the summary in your instructions because it reached the " +
		"model's context limit. Your work was interrupted mid-task, not finished: everything older than that " +
		"summary is gone except the turns below, reproduced as they happened.\n\n" +
		"Continue from the last thing you were doing. This reproduction is abbreviated, so re-read a file " +
		"before editing it rather than trusting what is quoted here.\n\n" +
		"<recent_turns>\n" + tail + "\n</recent_turns>"
}
