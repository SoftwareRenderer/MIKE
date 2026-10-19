package exec

import "testing"

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
