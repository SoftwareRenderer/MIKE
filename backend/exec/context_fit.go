package exec

import (
	"fmt"
	"strings"

	"mike/backend/types"
	"mike/pkg/logging"
)

// This file decides whether a request fits the model's context window, and what to cut when it does not.
// Whether the conversation is getting long is summarize.go's.

// promptBudget is the largest prompt the loop may send: thresholdPct of the model's window, the rest left
// for the answer. A window nobody reported answers 0, which every caller reads as no gate.
func promptBudget(contextWindow, thresholdPct int) int {
	if contextWindow <= 0 {
		return 0
	}
	if thresholdPct <= 0 || thresholdPct > 100 {
		thresholdPct = defaultSummarizeThreshold
	}
	return contextWindow * thresholdPct / 100
}

// overflowCause is why a request is too large, which decides what to do about it.
type overflowCause int

const (
	causeFits overflowCause = iota
	// causeHistory: the conversation alone is past the budget. Everything older than a summary stops
	// being sent, so only a summary moves it.
	causeHistory
	// causeToolOutput: the conversation fits and the newest tool results do not, so the results are cut.
	causeToolOutput
)

func classifyOverflow(requestTokens, historyTokens, budget int) overflowCause {
	switch {
	case budget <= 0, requestTokens <= budget:
		return causeFits
	case historyTokens > budget:
		return causeHistory
	default:
		return causeToolOutput
	}
}

// toolBatchTokens estimates the newest run of tool results: the messages after the last assistant message
// that asked for tools, which the model has not been offered yet.
func toolBatchTokens(messages []types.ChatMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if len(messages[i].ToolCalls) == 0 {
			continue
		}
		total := 0
		for _, m := range messages[i+1:] {
			if m.Role != "tool" {
				break
			}
			total += estimatePromptTokens([]types.ChatMessage{m})
		}
		return total
	}
	return 0
}

// conversationTokens is what a request would cost without its newest tool batch, which is also what the
// usage trigger measures.
func conversationTokens(messages []types.ChatMessage) int {
	return estimatePromptTokens(messages) - toolBatchTokens(messages)
}

// fitRequestToWindow returns the messages one LLM call is sent, made to fit the model's window: a
// conversation already past the budget is summarized, otherwise the tool results that do not fit are
// trimmed. Trimming lands on this copy and never on lc.messages, so every send re-derives the cut.
func (te *TaskExecutor) fitRequestToWindow(setup *taskExecSetup, lc *loopContext, activeModel string) []types.ChatMessage {
	llmMessages := buildLLMContext(lc.messages)
	budget := 0
	if lc.contextWindow != nil {
		budget = promptBudget(*lc.contextWindow, te.configStore.AutoSummarizeThreshold())
	}
	request := estimatePromptTokens(llmMessages)
	cause := classifyOverflow(request, conversationTokens(llmMessages), budget)
	if cause == causeFits {
		return llmMessages
	}
	if cause == causeHistory {
		te.summarize.summarizeNow(setup, lc, activeModel, true)
		llmMessages = buildLLMContext(lc.messages)
	}
	trimmed, reports := trimToFit(llmMessages, budget)
	for _, r := range reports {
		logging.For(logging.Exec).With(logging.TaskID, setup.task.ID).Warnf(
			"trimmed %s result to %d tokens, dropping %d, to fit a %d token budget",
			r.toolName, r.keptTokens, r.droppedTokens, budget)
	}
	return trimmed
}

// minKeptTokens is the floor a trimmed tool result keeps, so the model still sees what the call returned.
const minKeptTokens = 512

// trimReport is one tool result that was shortened, for the operator's log.
type trimReport struct {
	toolName      string
	keptTokens    int
	droppedTokens int
}

// trimToFit shortens tool results until the request takes budget tokens or fewer, oldest first, cutting
// each result once and never removing one — its assistant tool_call still has to be answered. The messages
// handed in are not modified, and once every result sits at minKeptTokens the request comes back as it is.
func trimToFit(messages []types.ChatMessage, budget int) ([]types.ChatMessage, []trimReport) {
	kept := make([]types.ChatMessage, len(messages))
	copy(kept, messages)
	names := toolNamesByID(kept)
	cut := make(map[int]bool)
	var reports []trimReport
	for over := estimatePromptTokens(kept) - budget; over > 0; {
		i := -1
		for j := range kept {
			if kept[j].Role == "tool" && !cut[j] && estimateTokensFromText(kept[j].Content) > minKeptTokens {
				i = j
				break
			}
		}
		if i < 0 {
			return kept, reports
		}
		cut[i] = true
		m := kept[i]
		name := names[m.ToolCallID]
		before := estimateTokensFromText(m.Content)
		// The notice spends the room the cut frees, or a trimmed result would come out longer.
		cost := estimateTokensFromText(trimMarker(name, "", m.Content))
		room := before - over - cost
		if room < minKeptTokens {
			room = minKeptTokens
		}
		body := cutToTokens(m.Content, room)
		kept[i].Content = body + trimMarker(name, body, m.Content)
		after := estimateTokensFromText(kept[i].Content)
		reports = append(reports, trimReport{toolName: name, keptTokens: after, droppedTokens: before - after})
		over -= before - after
	}
	return kept, reports
}

// toolNamesByID maps each tool result to the tool that produced it, which lives in the assistant
// message's tool_calls rather than in the result.
func toolNamesByID(messages []types.ChatMessage) map[string]string {
	names := make(map[string]string)
	for _, m := range messages {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				names[tc.ID] = tc.Function.Name
			}
		}
	}
	return names
}

// trimMarker tells the model how much it did not get and how to ask for the rest: a read is re-read by
// line, a command is re-run narrowed.
func trimMarker(toolName, body, original string) string {
	switch toolName {
	case types.ToolReadFile:
		return fmt.Sprintf("\n… (trimmed to %d of %d lines to fit the context window; re-read with offset and limit for the rest)",
			strings.Count(body, "\n")+1, strings.Count(original, "\n")+1)
	case types.ToolBash:
		return fmt.Sprintf("\n… (trimmed to %d of %d bytes to fit the context window; re-run the command narrowed with head, sed -n or grep for the rest)",
			len(body), len(original))
	}
	return fmt.Sprintf("\n… (trimmed to %d of %d bytes to fit the context window)", len(body), len(original))
}

// cutToTokens keeps the longest prefix of text that estimates at target tokens or fewer, ending on a line
// boundary where one exists. A cut mid-statement reads as content, and the model finishes it.
func cutToTokens(text string, target int) string {
	if target <= 0 || estimateTokensFromText(text) <= target {
		return text
	}
	// The estimate counts words, so the token target becomes a word count.
	room := int(float64(target) / 1.3)
	if room <= 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	kept := 0
	for kept < len(lines) {
		n := len(strings.Fields(lines[kept]))
		if n > room {
			break
		}
		room -= n
		kept++
	}
	if kept == 0 {
		words := strings.Fields(lines[0])
		if len(words) > room {
			words = words[:room]
		}
		return strings.Join(words, " ")
	}
	return strings.Join(lines[:kept], "\n")
}
