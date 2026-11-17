package exec

import "testing"

// TestCountEditLines counts what an edit_file call added and removed for the loop hint's wording. The
// partial-JSON row is the reason it is tolerant: a streamed call is scored before its array is complete,
// and a parse failure must count nothing rather than guess.
func TestCountEditLines(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		args        string
		wantAdded   int
		wantRemoved int
	}{
		{
			name:        "one edit that adds a line",
			args:        `{"path":"foo.go","edits":[{"oldText":"line1\nline2","newText":"line1\nline2\nline3"}]}`,
			wantAdded:   2,
			wantRemoved: 1,
		},
		{
			name: "every edit in the array counts",
			args: `{"path":"foo.go","edits":[
				{"oldText":"a\nb\nc","newText":"a\nb"},
				{"oldText":"x\ny","newText":"x\ny\nz\nw"}
			]}`,
			wantAdded:   4,
			wantRemoved: 3,
		},
		{name: "no edits", args: `{"path":"foo.go","edits":[]}`},
		{name: "an array still being streamed", args: `{"path":"foo.go","edits":[{"oldText":"line1`},
		{name: "a one-line replacement changes no line count", args: `{"path":"foo.go","edits":[{"oldText":"single","newText":"replacement"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			added, removed := countEditLines(tc.args)
			if added != tc.wantAdded || removed != tc.wantRemoved {
				t.Errorf("countEditLines = (%d, %d), want (%d, %d)", added, removed, tc.wantAdded, tc.wantRemoved)
			}
		})
	}
}

func TestPhraseDetector_Basic(t *testing.T) {
	t.Parallel()
	det := newPhraseDetector()
	phrase := "This is a long enough phrase to be tracked by the detector. "

	// Should not fire until maxPhraseRepeats (10)
	for i := 0; i < 9; i++ {
		if det.check(phrase) {
			t.Fatalf("phrase detector fired too early at iteration %d", i)
		}
	}

	if !det.check(phrase) {
		t.Error("phrase detector should have fired on 10th repetition")
	}
}

func TestBlockDetector_Basic(t *testing.T) {
	t.Parallel()
	det := newBlockDetector()
	block := "This is a repeating block of text. "

	det.check(block)
	// 2nd occurrence
	if det.check(block) {
		t.Error("block detector fired on 2nd repetition, want 3rd")
	}
	// 3rd occurrence
	if !det.check(block) {
		t.Error("block detector should have fired on 3rd repetition")
	}
}

func TestBlockDetector_MinLength(t *testing.T) {
	t.Parallel()
	det := newBlockDetector()
	shortBlock := "Short. "

	for i := 0; i < 10; i++ {
		if det.check(shortBlock) {
			t.Fatalf("block detector fired for short block at iteration %d", i)
		}
	}
}

func TestLoopDetector_CheckStreamingText(t *testing.T) {
	t.Parallel()
	ld := newLoopDetector()

	t.Run("PhraseLoop", func(t *testing.T) {
		ld.reset()
		phrase := "This is a long enough phrase to be tracked by the detector. "
		for i := 0; i < 9; i++ {
			ld.checkStreamingText(phrase)
		}
		if ld.checkStreamingText(phrase) != LoopPhrase {
			t.Error("expected LoopPhrase for repeated phrase")
		}
	})

	t.Run("BlockLoop", func(t *testing.T) {
		ld.reset()
		block := "Repeat me three times. "
		ld.checkStreamingText(block)
		ld.checkStreamingText(block)
		if ld.checkStreamingText(block) != LoopPhrase {
			t.Error("expected LoopPhrase for repeated block")
		}
	})
}

func TestAsksInLastParagraph(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"QuestionInSingleParagraph", "Should I use React or Vue for this project?", true},
		{"QuestionMidParagraph", "What framework would you recommend? Let me know your preference.", true},
		{"StatementOnly", "I have analyzed the code and updated the tests.", false},
		{"QuestionInEarlierParagraphOnly", "Should I use React or Vue?\n\nI'll go with React and start now.", false},
		{"QuestionInFinalParagraphAfterProse", "I analyzed the code and updated the tests.\n\nShall I merge it into main?", true},
		{"TrailingBlankLines", "Work is done.\n\nWant me to push it?\n\n\n", true},
		{"TrailingWhitespace", "Do you want the long version?   \t", true},
		{"TrailingStatementAfterQuestionList", "Options:\n- React\n- Vue\n\nI'll pick React.", false},
		{"CRLFLineEndings", "I finished the change.\r\n\r\nShould I commit it?\r\n", true},
		{"Empty", "", false},
		{"OnlyWhitespace", "\n\n   \n\t\n", false},
		{"FullwidthQuestionMark", "これを先に実装しますか。それとも確認しますか？", true},
		{"ArabicQuestionMark", "هل تريد المتابعة؟", true},
		{"SmallPresentationForm", "Should we keep this marker\uFE56", true},
		{"QuestionMarkInsideCodeFence", "The check is:\n\n```go\nif ok {\n\tfmt.Println(\"done?\")\n}\n```", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := asksInLastParagraph(tc.text); got != tc.want {
				t.Errorf("asksInLastParagraph(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
