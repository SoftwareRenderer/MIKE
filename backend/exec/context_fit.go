package exec

import (
	"fmt"
	"strings"

	"mike/backend/types"
	"mike/pkg/logging"
)

// This file decides whether a request fits the model's context window, and what to cut when it does not;
// whether the conversation has grown too long is summarize.go's decision.

// promptBudget is the largest prompt the loop may send. A window nobody reported answers 0, which every
// caller reads as no gate.
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
	// causeHistory: the conversation alone is past the budget, and only a summary moves it — everything
	// older than the summary stops being sent.
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
			total += estimateTokensFromText(m.Content)
		}
		return total
	}
	return 0
}

// requestSplit returns what a request costs, and what the same request costs without its newest run of tool
// results. Both come from one pass, because a long conversation is measured on every LLM call.
func requestSplit(messages []types.ChatMessage) (int, int) {
	total := estimatePromptTokens(messages)
	return total, total - toolBatchTokens(messages)
}

// conversationTokens is what a request would cost without its newest tool batch, which is also what the
// usage trigger measures.
func conversationTokens(messages []types.ChatMessage) int {
	_, history := requestSplit(messages)
	return history
}

// reportedConversation is what the provider counted for the conversation at its last call, plus the answer it
// gave and the results appended since. A word count only reads message text, so it cannot price the system
// prompt, the tool definitions or the provider's tokenizer.
func reportedConversation(lc *loopContext, messages []types.ChatMessage) int {
	if lc.usage == nil || lc.usage.PromptTokens <= 0 {
		return 0
	}
	last := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			last = i
			break
		}
	}
	if last < 0 {
		return 0
	}
	for _, m := range messages[last+1:] {
		if m.Role == "summarization" {
			// The summary replaced the conversation this count describes.
			return 0
		}
	}
	appended := estimatePromptTokens(messages[last:])
	return lc.usage.PromptTokens + appended - toolBatchTokens(messages)
}

// fitRequestToWindow returns the messages one LLM call is sent, made to fit the model's window: a
// conversation already past the budget is summarized, otherwise the tool results that do not fit are trimmed.
// Trimming lands on this copy and never on lc.messages, so every send cuts again from the full conversation.
func (te *TaskExecutor) fitRequestToWindow(setup *taskExecSetup, lc *loopContext, activeModel string) []types.ChatMessage {
	llmMessages := buildLLMContext(lc.messages)
	budget := 0
	if lc.contextWindow != nil {
		budget = promptBudget(*lc.contextWindow, te.configStore.AutoSummarizeThreshold())
	}
	estimated, history := requestSplit(llmMessages)
	// extra is the part of the conversation the provider counted and the estimate did not.
	extra := reportedConversation(lc, lc.messages) - history
	if extra < 0 {
		extra = 0
	}
	cause := classifyOverflow(estimated+extra, history+extra, budget)
	if cause == causeFits {
		return llmMessages
	}
	if cause == causeHistory {
		te.summarize.summarizeNow(setup, lc, activeModel, true)
		llmMessages = buildLLMContext(lc.messages)
		extra = 0
	}
	// trimToFit measures the request itself, so it is charged the same shortfall. A batch cause leaves the
	// conversation inside the budget, so this stays positive.
	trimmed, reports := trimToFit(llmMessages, budget-extra)
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

// trimToFit shortens tool results until the request takes limit tokens or fewer, oldest first, cutting each
// result once and never removing one — its assistant tool_call still has to be answered. The messages handed
// in are not modified, and once every result sits at minKeptTokens the request comes back as it is.
func trimToFit(messages []types.ChatMessage, limit int) ([]types.ChatMessage, []trimReport) {
	kept := make([]types.ChatMessage, len(messages))
	copy(kept, messages)
	names := toolNamesByID(kept)
	cut := make(map[int]bool)
	var reports []trimReport
	for over := estimatePromptTokens(kept) - limit; over > 0; {
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

// trimMarker tells the model how much it missed and how to ask for the rest: a trimmed read is re-read with
// offset and limit, a trimmed command is re-run narrowed.
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
