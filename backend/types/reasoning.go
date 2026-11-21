package types

// ShouldHideReasoning is the one rule that decides whether a reader is shown a message's reasoning text.
// Its two callers — the message window's redaction pass and the executor's reasoning stream — must agree,
// or the same row reads differently depending on which path it arrived by.
//
// Reasoning is the turn's expensive text, so what buys it a place on screen is being the answer itself:
// reasoning with no message text. Anything else collapses behind the "Show reasoning" toggle whose read is
// `?full=1`. The bound is measured in bytes of stored text, which is what the wire costs.
func ShouldHideReasoning(reasoningContent, content string, hasToolCalls bool) bool {
	if reasoningContent == "" {
		return true
	}
	if content == "" {
		// Short enough to read at a glance, or the only thing this call said besides its tool calls.
		if len(reasoningContent) <= 200 || hasToolCalls {
			return false
		}
	}
	return true
}
