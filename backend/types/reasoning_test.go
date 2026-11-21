package types

import (
	"strings"
	"testing"
)

// TestShouldHideReasoning: the shapes that keep reasoning on screen are the ones with no message text.
// The stream and the message read both ask this, so a change moves both paths.
func TestShouldHideReasoning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		reasoning string
		content   string
		toolCalls bool
		wantHide  bool
	}{
		{name: "no reasoning to show", wantHide: true},
		{name: "a chain of thought behind an answer", reasoning: strings.Repeat("x", 300), content: "Here is the answer.", wantHide: true},
		{name: "reasoning that is the whole answer", reasoning: "Brief thought", wantHide: false},
		{name: "reasoning that carried a tool call", reasoning: strings.Repeat("z", 400), toolCalls: true, wantHide: false},
		{name: "long reasoning carried by a tool call and an answer", reasoning: strings.Repeat("z", 400), content: "Done.", toolCalls: true, wantHide: true},
		{name: "reasoning at the bound with nothing else", reasoning: strings.Repeat("y", 200), wantHide: false},
		{name: "reasoning past the bound with nothing else", reasoning: strings.Repeat("y", 201), wantHide: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ShouldHideReasoning(tc.reasoning, tc.content, tc.toolCalls); got != tc.wantHide {
				t.Errorf("ShouldHideReasoning(%q, %q, toolCalls=%v) = %v, want %v",
					tc.reasoning, tc.content, tc.toolCalls, got, tc.wantHide)
			}
		})
	}
}
