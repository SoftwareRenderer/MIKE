package git

// truncateDiffLines budgets a patch by change lines before it is parsed, so the panel's payload stays
// bounded while the counts it shows stay the pre-truncation ones. The budget is spent hunk by hunk,
// and a file over the limit never costs the files listed after it their diff.

import (
	"fmt"
	"strings"
	"testing"
)

func TestTruncateDiffLinesCutsByChangeLimit(t *testing.T) {
	t.Run("a diff under the limit is returned unchanged", func(t *testing.T) {
		diff := `diff --git a/file.txt b/file.txt
index 1234567..abcdef 100644
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,4 @@
 line1
+added1
 line2
 line3
+added2`
		result, truncated := truncateDiffLines(diff, 60)
		if len(truncated) != 0 {
			t.Errorf("expected no truncated files, got %v", truncated)
		}
		if result != diff {
			t.Error("expected diff to be unchanged")
		}
	})

	t.Run("the budget is spent hunk by hunk", func(t *testing.T) {
		diff := `diff --git a/small.txt b/small.txt
--- a/small.txt
+++ b/small.txt
@@ -1,2 +1,4 @@
 line1
+added1
+added2
 line2
+added3
+added4
diff --git a/large.txt b/large.txt
--- a/large.txt
+++ b/large.txt
@@ -1,2 +1,4 @@
 line1
+h1-added1
+h1-added2
 line2
+h1-added3
@@ -10,2 +10,4 @@
 line10
+h2-added1
+h2-added2
 line11
+h2-added3
@@ -20,2 +20,4 @@
 line20
+h3-added1
+h3-added2
 line21
+h3-added3`
		result, truncated := truncateDiffLines(diff, 6)

		if len(truncated) != 1 || truncated[0] != "large.txt" {
			t.Errorf("expected [large.txt], got %v", truncated)
		}
		if !strings.Contains(result, "added4") {
			t.Error("expected small.txt changes to be fully present")
		}
		// large.txt: first hunk always kept (3 changes), second fits (3+3=6), third dropped.
		if !strings.Contains(result, "h1-added3") {
			t.Error("expected first hunk of large.txt")
		}
		if !strings.Contains(result, "h2-added3") {
			t.Error("expected second hunk of large.txt (fits within limit)")
		}
		if strings.Contains(result, "h3-added") {
			t.Error("expected the whole third hunk of large.txt to be truncated")
		}
		// small.txt(1) + large.txt(2) hunks, two @@ markers each.
		if n := strings.Count(result, "@@"); n != 6 {
			t.Errorf("expected 6 @@ markers (3 hunks), got %d", n)
		}
	})

	t.Run("one hunk over the limit keeps the first changes", func(t *testing.T) {
		var lines []string
		lines = append(lines, "diff --git a/file.txt b/file.txt", "--- a/file.txt", "+++ b/file.txt", "@@ -1,2 +1,12 @@", " line1")
		for i := 1; i <= 10; i++ {
			lines = append(lines, "+line"+string(rune('0'+i)))
		}
		lines = append(lines, " line2")
		result, truncated := truncateDiffLines("\n"+strings.Join(lines, "\n"), 6)

		if len(truncated) != 1 || truncated[0] != "file.txt" {
			t.Errorf("expected [file.txt], got %v", truncated)
		}
		if !strings.Contains(result, "@@") {
			t.Error("expected a hunk header")
		}
		for _, kept := range []string{"+line1", "+line6"} {
			if !strings.Contains(result, kept) {
				t.Errorf("expected %q inside the budget", kept)
			}
		}
		for _, cut := range []string{"+line:", "+line;", "+line<", "+line="} {
			if strings.Contains(result, cut) {
				t.Errorf("expected the changes past the limit to be gone, found %q", cut)
			}
		}
	})

	t.Run("a first file over the limit costs only itself", func(t *testing.T) {
		var lines []string
		lines = append(lines, "diff --git a/large.txt b/large.txt", "--- a/large.txt", "+++ b/large.txt", "@@ -1,2 +1,12 @@", " line1")
		for i := 1; i <= 10; i++ {
			lines = append(lines, "+large-line-"+string(rune('0'+i)))
		}
		lines = append(lines, " line2")
		lines = append(lines, "diff --git a/small.txt b/small.txt", "--- a/small.txt", "+++ b/small.txt", "@@ -1,2 +1,4 @@",
			" lineA", "+small-1", "+small-2", " lineB", "+small-3")
		lines = append(lines, "diff --git a/tiny.txt b/tiny.txt", "--- a/tiny.txt", "+++ b/tiny.txt", "@@ -1 +1,2 @@",
			" lineX", "+tiny-1", "+tiny-2")
		result, truncated := truncateDiffLines("\n"+strings.Join(lines, "\n"), 6)

		if len(truncated) != 1 || truncated[0] != "large.txt" {
			t.Errorf("expected [large.txt], got %v", truncated)
		}
		// A file over the limit must not drop the files listed after it.
		for _, header := range []string{"diff --git a/large.txt", "diff --git a/small.txt", "diff --git a/tiny.txt"} {
			if !strings.Contains(result, header) {
				t.Errorf("expected %q in result (an early return once dropped the later files)", header)
			}
		}
		for _, kept := range []string{"+large-line-1", "+large-line-6", "+small-1", "+small-3", "+tiny-1", "+tiny-2"} {
			if !strings.Contains(result, kept) {
				t.Errorf("expected %q present", kept)
			}
		}
		for _, cut := range []string{"+large-line-7", "+large-line-8", "+large-line-9", "+large-line:"} {
			if strings.Contains(result, cut) {
				t.Errorf("expected %q truncated", cut)
			}
		}
	})

	t.Run("an empty diff stays empty", func(t *testing.T) {
		result, truncated := truncateDiffLines("", 60)
		if result != "" {
			t.Errorf("expected empty result, got %q", result)
		}
		if len(truncated) != 0 {
			t.Errorf("expected no truncated files, got %v", truncated)
		}
	})
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
