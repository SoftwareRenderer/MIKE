package fsutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// IsText is the one rule behind three routes: a working-tree read, a committed read and one archive member.
func TestIsTextRefusesWhatNoEditorCanShow(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{name: "text", in: []byte("package main\n"), want: true},
		{name: "text with a combining mark and an emoji", in: []byte("café — 🎨\n"), want: true},
		// Latin-1: no NUL, so only the UTF-8 half can refuse it.
		{name: "text in another encoding", in: []byte{0x63, 0x61, 0x66, 0xE9, 0x0A}, want: false},
		{name: "a run cut mid-rune", in: []byte{0x61, 0x62, 0xC3}, want: false}, // promised a continuation
		{name: "a NUL with nothing else wrong", in: []byte{0x61, 0x00, 0x62}, want: false},
		{name: "the bytes of a PNG", in: []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, want: false},
		{name: "no bytes at all", in: nil, want: true}, // a file the editor must still open
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsText(tc.in); got != tc.want {
				t.Errorf("IsText(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The refusal reaches a person, so it is MIKE's own words and carries no path.
func TestErrNotTextNamesNoPath(t *testing.T) {
	if got := ErrNotText.Error(); got != "not a text file" {
		t.Errorf("ErrNotText = %q, want the sentence the routes answer with", got)
	}
}

// ToText is what a read-only surface (a diff row, a patch, a status line) answers foreign bytes with. Its
// two invariants are what the callers depend on: the text becomes encodable without losing its lines, and a
// NUL survives, because the same bytes are often asked "is this binary?" afterwards.
func TestToTextRepairsWithoutRewritingTheShape(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "text is handed back untouched", in: "two\nlines\n", want: "two\nlines\n"},
		{name: "one undecodable run is one replacement character", in: "caf\xE9", want: "caf\ufffd"},
		{name: "a truncated rune is repaired at its own edge", in: "ab\xC3", want: "ab\ufffd"},
		{name: "a NUL is text as far as UTF-8 is concerned", in: "a\x00b", want: "a\x00b"},
		{name: "nothing at all stays nothing", in: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ToText(tc.in)
			if got != tc.want {
				t.Errorf("ToText(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("ToText left %q unencodable", got)
			}
			if strings.Count(got, "\n") != strings.Count(tc.in, "\n") {
				t.Errorf("ToText changed the line count of %q: %q", tc.in, got)
			}
		})
	}
}
