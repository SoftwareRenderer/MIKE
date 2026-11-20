package git

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A patch carries the file's own bytes, so a file written in another encoding puts them into the rows a panel
// is handed. They arrive repaired: an unencodable string would leave the request with a status and no body.
func TestParseUnifiedDiffRepairsBytesNoEncoderWillTake(t *testing.T) {
	// "café old" / "café new" in Latin-1: no NUL, so only the UTF-8 rule sees it.
	f := oneFile(t, "diff --git a/notes.txt b/notes.txt\n"+
		"--- a/notes.txt\n+++ b/notes.txt\n@@ -1 +1 @@\n-caf\xE9 old\n+caf\xE9 new\n")

	if f.Path != "notes.txt" {
		t.Errorf("path = %q, want notes.txt", f.Path)
	}
	if len(f.Rows) != 2 {
		t.Fatalf("rows = %d, want the two changed lines: %+v", len(f.Rows), f.Rows)
	}
	for _, row := range f.Rows {
		if !utf8.ValidString(row.Text) {
			t.Errorf("row %q is not valid UTF-8, so nothing can marshal it", row.Text)
		}
	}
	changed := f.Rows[1].Text
	if !strings.HasPrefix(changed, "caf") || !strings.Contains(changed, "new") {
		t.Errorf("the repair lost the readable part of the line: %q", changed)
	}
	if !strings.Contains(changed, "\ufffd") {
		t.Errorf("the undecodable byte should be visible as %q, got %q", "\ufffd", changed)
	}
}

// The repair changes what a byte is, not what a line is: rows keep their count, and a NUL survives so the
// file still reads as binary to whoever asks next.
func TestParseUnifiedDiffKeepsTheRowsItParsedFromForeignBytes(t *testing.T) {
	f := oneFile(t, "diff --git a/data.bin b/data.bin\n"+
		"--- a/data.bin\n+++ b/data.bin\n@@ -1,2 +1,2 @@\n-a\x00b\n+c\xFF\xFFd\n")

	if len(f.Rows) != 2 {
		t.Fatalf("rows = %d, want the two changed lines: %+v", len(f.Rows), f.Rows)
	}
	if !strings.Contains(f.Rows[0].Text, "\x00") {
		t.Errorf("the NUL vanished from %q: that byte is what says a file is binary", f.Rows[0].Text)
	}
	if !utf8.ValidString(f.Rows[1].Text) {
		t.Errorf("row %q is not valid UTF-8", f.Rows[1].Text)
	}
}
