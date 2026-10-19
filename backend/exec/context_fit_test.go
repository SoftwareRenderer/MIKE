package exec

import (
	"strings"
	"testing"

	"mike/backend/types"
)

// lastReportedPrompt walks back to the newest assistant row the provider answered.
func TestLastReportedPrompt(t *testing.T) {
	t.Parallel()
	rows := []types.TaskMessage{
		{Role: "assistant", PromptTokens: 100},
		{Role: "tool_call"},
		{Role: "assistant", PromptTokens: 5000},
		{Role: "tool_call"},
		{Role: "user"},
	}
	if got := lastReportedPrompt(rows); got != 5000 {
		t.Errorf("lastReportedPrompt = %d, want the newest answered turn at 5000", got)
	}
	if got := lastReportedPrompt([]types.TaskMessage{{Role: "assistant"}, {Role: "user"}}); got != 0 {
		t.Errorf("lastReportedPrompt = %d, want 0 when no row holds a count", got)
	}
}

// A tool_call is sent with its arguments, which is how a long command reaches the model.
func TestEstimatePromptTokensCountsToolCallArguments(t *testing.T) {
	t.Parallel()
	withCall := types.ChatMessage{Role: "assistant"}
	withCall.ToolCalls = []types.ToolCallMsg{{ID: "c1", Type: "function"}}
	withCall.ToolCalls[0].Function.Name = types.ToolBash
	withCall.ToolCalls[0].Function.Arguments = filler(2000, "aa")

	bare := estimatePromptTokens([]types.ChatMessage{{Role: "assistant"}})
	args := estimatePromptTokens([]types.ChatMessage{withCall})
	if got := args - bare; got < 1900 || got > 2100 {
		t.Errorf("a 2000 token command counted %d tokens", got)
	}
}

// A megabyte of base64 has a word per line at most, so only its bytes tell the gate what it costs.
func TestEstimateTokensPricesDenseTextByItsBytes(t *testing.T) {
	t.Parallel()
	blob := denseBlob(1<<20, 76)
	if got, want := estimateTokensFromText(blob), len(blob)/minBytesPerToken; got < want {
		t.Errorf("a %d byte blob estimated %d tokens, want at least its byte count over %d", len(blob), got, minBytesPerToken)
	}
	spaced := strings.Repeat("a ", 40)
	if got, want := estimateTokensFromText(spaced), int(float64(len(strings.Fields(spaced)))*1.3); got != want {
		t.Errorf("text the word count already over-prices came to %d tokens, want %d", got, want)
	}
}

// Both gates must cross at the same token count, or a run summarizes at one number and gets refused at
// another.
func TestPromptBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		window    int
		threshold int
		want      int
	}{
		{name: "window and threshold", window: 8192, threshold: 90, want: 7372},
		{name: "custom threshold", window: 1000, threshold: 50, want: 500},
		{name: "unset threshold uses the default", window: 100, threshold: 0, want: defaultSummarizeThreshold},
		{name: "negative threshold uses the default", window: 100, threshold: -10, want: defaultSummarizeThreshold},
		{name: "threshold above 100 uses the default", window: 100, threshold: 150, want: defaultSummarizeThreshold},
		{name: "no window reported", window: 0, threshold: 90, want: 0},
		{name: "negative window", window: -1, threshold: 90, want: 0},
	}
	for _, tc := range cases {
		if got := promptBudget(tc.window, tc.threshold); got != tc.want {
			t.Errorf("%s: promptBudget(%d, %d) = %d, want %d", tc.name, tc.window, tc.threshold, got, tc.want)
		}
	}
}

// The summarize trigger fires at exactly promptBudget.
func TestPromptBudgetIsWhereSummarizeFires(t *testing.T) {
	t.Parallel()
	for _, window := range []int{4096, 8192, 32768} {
		budget := promptBudget(window, 90)
		if shouldSummarize(budget-1, window, 90) {
			t.Errorf("window %d: summarize fired below its budget of %d", window, budget)
		}
		if !shouldSummarize(budget, window, 90) {
			t.Errorf("window %d: summarize did not fire at its budget of %d", window, budget)
		}
	}
	if shouldSummarize(1000, 0, 90) {
		t.Error("an unreported window must gate nothing")
	}
}
