package git

// Rows carry the line numbers the panel prints and the flag that offers to show the rest.

import (
	"fmt"
	"strings"
	"testing"
)

func rowNumbers(f TrackedDiffFile) string {
	parts := make([]string, 0, len(f.Rows))
	for _, r := range f.Rows {
		n := "-"
		if r.LineNumber != nil {
			n = fmt.Sprint(*r.LineNumber)
		}
		parts = append(parts, r.Variant+"@"+n+":"+r.Text)
	}
	return strings.Join(parts, " | ")
}

// An unchanged empty line is sometimes written as nothing rather than " "; dropping that row shifts every
// later line of the hunk.
func TestParseReadsABlankContextLineAsContext(t *testing.T) {
	patch := "diff --git a/x.txt b/x.txt\n" +
		"index abc1234..def5678 100644\n" +
		"--- a/x.txt\n+++ b/x.txt\n" +
		"@@ -1,5 +1,5 @@\n" +
		" one\n" + "\n" + " three\n" + "-four\n+FOUR\n five\n"

	files := ParseUnifiedDiff(patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	got := rowNumbers(files[0])
	want := "context@1:one | context@2: | context@3:three | removed@4:four | added@4:FOUR | context@5:five"
	if got != want {
		t.Errorf("rows =\n  %s\nwant\n  %s", got, want)
	}
}

// Carrying the previous hunk's counters forward would print rows at line numbers the file does not have.
func TestParseStopsNumberingAtAnUnreadableHunkHeader(t *testing.T) {
	patch := "diff --git a/x.txt b/x.txt\n" +
		"index abc1234..def5678 100644\n" +
		"--- a/x.txt\n+++ b/x.txt\n" +
		"@@ -1,2 +1,2 @@\n one\n-TWO\n+two\n" +
		"@@ garbage not a header\n" +
		"+NOT-NUMBERED\n"

	files := ParseUnifiedDiff(patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	for _, r := range files[0].Rows {
		if r.Text == "NOT-NUMBERED" {
			t.Errorf("a row after an unreadable header was numbered %v: %s", *r.LineNumber, rowNumbers(files[0]))
		}
	}
}

// A C-quoted header must name the block the parser keys by, or no file is told it was cut.
func TestTruncateNamesAQuotedPathAndKeepsItsCounts(t *testing.T) {
	const name = `qu"ote.ts`
	var body []string
	body = append(body,
		`diff --git "a/qu\"ote.ts" "b/qu\"ote.ts"`,
		"index abc1234..def5678 100644",
		`--- "a/qu\"ote.ts"`,
		`+++ "b/qu\"ote.ts"`,
		"@@ -1 +1,70 @@",
	)
	for i := 1; i <= 70; i++ {
		body = append(body, fmt.Sprintf("+NEW-%d", i))
	}
	patch := strings.Join(body, "\n") + "\n"

	origStats := countFileChanges(patch)
	cut, truncatedFiles := truncateDiffLines(patch, 60)
	files := parseUnifiedDiffToFiles(cut, truncatedFiles, origStats, nil)

	entry, ok := files[name]
	if !ok {
		t.Fatalf("the parsed diff is keyed by %q, not the file: %v", name, keysOf(files))
	}
	if !entry.Truncated {
		t.Errorf("a file cut from 70 changes to 60 is not marked truncated, so the panel offers no way to "+
			"see the rest (truncatedFiles=%v)", truncatedFiles)
	}
	if entry.OrigAdded != 70 {
		t.Errorf("OrigAdded = %d, want the pre-truncation 70", entry.OrigAdded)
	}
}
