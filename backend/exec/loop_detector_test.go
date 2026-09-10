package exec

import "testing"

func TestCountEditLines_CompleteJSON(t *testing.T) {
	args := `{"path":"foo.go","edits":[{"oldText":"line1\nline2","newText":"line1\nline2\nline3"}]}`
	added, removed := countEditLines(args)
	if added != 2 {
		t.Errorf("added = %d, want 2", added)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
}

func TestCountEditLines_MultipleEdits(t *testing.T) {
	args := `{"path":"foo.go","edits":[
		{"oldText":"a\nb\nc","newText":"a\nb"},
		{"oldText":"x\ny","newText":"x\ny\nz\nw"}
	]}`
	added, removed := countEditLines(args)
	// edit1: newText "a\nb" has 1 newline; edit2: newText "x\ny\nz\nw" has 3 newlines => 4 total
	// edit1: oldText "a\nb\nc" has 2 newlines; edit2: oldText "x\ny" has 1 newline => 3 total
	if added != 4 {
		t.Errorf("added = %d, want 4", added)
	}
	if removed != 3 {
		t.Errorf("removed = %d, want 3", removed)
	}
}

func TestCountEditLines_EmptyEdits(t *testing.T) {
	args := `{"path":"foo.go","edits":[]}`
	added, removed := countEditLines(args)
	if added != 0 || removed != 0 {
		t.Errorf("added = %d, removed = %d, want 0, 0", added, removed)
	}
}

func TestCountEditLines_NoEditsKey(t *testing.T) {
	args := `{"path":"foo.go"}`
	added, removed := countEditLines(args)
	if added != 0 || removed != 0 {
		t.Errorf("added = %d, removed = %d, want 0, 0", added, removed)
	}
}

func TestCountEditLines_PartialJSON(t *testing.T) {
	// Partial JSON — edits array not yet complete, parseToolArgs returns nil
	args := `{"path":"foo.go","edits":[{"oldText":"line1`
	added, removed := countEditLines(args)
	if added != 0 || removed != 0 {
		t.Errorf("added = %d, removed = %d, want 0, 0 (partial JSON)", added, removed)
	}
}

func TestCountEditLines_SingleLineNoNewline(t *testing.T) {
	args := `{"path":"foo.go","edits":[{"oldText":"single","newText":"replacement"}]}`
	added, removed := countEditLines(args)
	if added != 0 {
		t.Errorf("added = %d, want 0 (no newlines in single line)", added)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (no newlines in single line)", removed)
	}
}

func TestPhraseDetector_Basic(t *testing.T) {
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

func TestPhraseDetector_MinWords(t *testing.T) {
	det := newPhraseDetector()
	shortPhrase := "Too short. "

	for i := 0; i < 20; i++ {
		if det.check(shortPhrase) {
			t.Fatalf("phrase detector fired for short phrase at iteration %d", i)
		}
	}
}

func TestBlockDetector_Basic(t *testing.T) {
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
	det := newBlockDetector()
	shortBlock := "Short. "

	for i := 0; i < 10; i++ {
		if det.check(shortBlock) {
			t.Fatalf("block detector fired for short block at iteration %d", i)
		}
	}
}

func TestLoopDetector_CheckStreamingText(t *testing.T) {
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
