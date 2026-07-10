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
