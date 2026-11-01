package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// A click that ends by re-reading its own row must answer a refusal when the row was deleted while the
// request ran. Answering from a nil row either writes "null" with a 200 — which the board reads as a
// task that exists and holds nothing — or dereferences it.
func TestRespondingToAVanishedRowRefusesInsteadOfEmptying(t *testing.T) {
	h, _, _ := newTestTaskHandler(t)

	rec := httptest.NewRecorder()
	h.respondWithTask(rec, "p1", "t0000000000-000000")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a row that is gone (body %q)", rec.Code, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); body == "null" || body == "" {
		t.Errorf("body = %q, want MIKE's own sentence about the missing row", body)
	}
}

// A title taken from a long prompt is cut to fit, and the cut must land between characters: half a
// character reaches the board as the replacement glyph, in the person's own title.
func TestTitleFromALongUnasciiPromptKeepsItsCharacters(t *testing.T) {
	prompt := strings.Repeat("構", 120) + "\nthe rest of the prompt is never the title"

	title := autoGenerateTitle(prompt)

	if strings.ContainsRune(title, utf8.RuneError) {
		t.Errorf("title holds the replacement character: %q", title)
	}
	if !utf8.ValidString(title) {
		t.Errorf("title is not valid UTF-8: %q", title)
	}
	if n := utf8.RuneCountInString(title); n > 80 {
		t.Errorf("title is %d characters, want at most 80: %q", n, title)
	}
	if !strings.HasSuffix(title, "...") {
		t.Errorf("title %q carries no notice that it was cut", title)
	}
	if strings.Contains(title, "rest of the prompt") {
		t.Errorf("title reached past the first line: %q", title)
	}
}

// isShortTitle reads the same first line, so it answers for the same text.
func TestShortTitleJudgesTheFirstLineOnly(t *testing.T) {
	if !isShortTitle("ship it\n" + strings.Repeat("many words ", 20)) {
		t.Error("a two-word first line is short whatever follows it")
	}
	if isShortTitle(strings.Repeat("word ", 9)) {
		t.Error("nine words on one line is not a short title")
	}
	if isShortTitle("   \nsecond line") {
		t.Error("an empty first line is no title at all")
	}
}
