package git

// The one diff grammar. `git.ParseUnifiedDiff` is what every diff path — the panel, the stats frame,
// the editor gutter — reads, so these are the shapes it has to answer. The wire shape of the parsed
// rows is pinned beside the diff structs in worktree_test.go, and the route that serves them
// (`?parsed=1`) is owned by backend/api.

import (
	"strings"
	"testing"
)

// parseForTest is the grammar's entry point: one file per diff header, in path order.
func parseForTest(t *testing.T, patch string) []TrackedDiffFile {
	t.Helper()
	return ParseUnifiedDiff(patch)
}

// oneFile returns the single file a one-file patch parsed into, failing the test otherwise.
func oneFile(t *testing.T, patch string) TrackedDiffFile {
	t.Helper()
	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1:\n%s", len(files), patch)
	}
	return files[0]
}

// rowVariants renders the rows as a compact string so a failure names the shape, not just a count.
func rowVariants(f TrackedDiffFile) string {
	parts := make([]string, 0, len(f.Rows))
	for _, r := range f.Rows {
		parts = append(parts, r.Variant)
	}
	return strings.Join(parts, ",")
}

// rowWant is one expected row. line is the gutter number, or -1 for a row that must carry none.
type rowWant struct {
	variant string
	text    string
	line    int
}

// assertRows compares the parsed rows against the expected ones, element by element.
func assertRows(t *testing.T, f TrackedDiffFile, want []rowWant) {
	t.Helper()
	if len(f.Rows) != len(want) {
		t.Fatalf("rows = %d (%s), want %d:\n%+v", len(f.Rows), rowVariants(f), len(want), f.Rows)
	}
	for i, w := range want {
		r := f.Rows[i]
		if r.Variant != w.variant {
			t.Errorf("row %d: variant=%q, want %q (all: %s)", i, r.Variant, w.variant, rowVariants(f))
		}
		if r.Text != w.text {
			t.Errorf("row %d: text=%q, want %q", i, r.Text, w.text)
		}
		switch {
		case w.line < 0 && r.LineNumber != nil:
			t.Errorf("row %d (%s %q): lineNumber=%v, want none", i, r.Variant, r.Text, *r.LineNumber)
		case w.line >= 0 && r.LineNumber == nil:
			t.Errorf("row %d (%s %q): has no line number, want %d", i, r.Variant, r.Text, w.line)
		case w.line >= 0 && *r.LineNumber != w.line:
			t.Errorf("row %d (%s %q): lineNumber=%v, want %d", i, r.Variant, r.Text, *r.LineNumber, w.line)
		}
	}
}

func TestParseReturnsNothingForEmptyInput(t *testing.T) {
	for _, in := range []string{"", "   ", "\n"} {
		if got := ParseUnifiedDiff(in); len(got) != 0 {
			t.Errorf("ParseUnifiedDiff(%q) = %d files, want 0", in, len(got))
		}
	}
}

// A row's gutter number is not its identity: one column numbers a removed row from the old file and
// every other row from the new one, and the counters restart at each hunk header.
func TestParseUnifiedDiffRows(t *testing.T) {
	t.Run("one file's variants and counts", func(t *testing.T) {
		f := oneFile(t, `diff --git a/src/main.ts b/src/main.ts
index abc1234..def5678 100644
--- a/src/main.ts
+++ b/src/main.ts
@@ -1,3 +1,4 @@
 const x = 1;
-const y = 2;
+const y = 3;
+const z = 4;
 const w = 5;`)
		if f.Path != "src/main.ts" {
			t.Errorf("path = %q, want src/main.ts", f.Path)
		}
		if f.Added != 2 || f.Removed != 1 {
			t.Errorf("added/removed = %d/%d, want 2/1", f.Added, f.Removed)
		}
		assertRows(t, f, []rowWant{
			{"context", "const x = 1;", 1},
			{"removed", "const y = 2;", 2},
			{"added", "const y = 3;", 2},
			{"added", "const z = 4;", 3},
			{"context", "const w = 5;", 4},
		})
	})

	t.Run("a blank context line is still a row", func(t *testing.T) {
		// An unchanged empty line is sometimes written as nothing rather than " "; dropping that row
		// shifts every later line of the hunk.
		f := oneFile(t, "diff --git a/x.txt b/x.txt\n"+
			"index abc1234..def5678 100644\n"+
			"--- a/x.txt\n+++ b/x.txt\n"+
			"@@ -1,5 +1,5 @@\n"+
			" one\n"+"\n"+" three\n"+"-four\n+FOUR\n five\n")
		assertRows(t, f, []rowWant{
			{"context", "one", 1},
			{"context", "", 2},
			{"context", "three", 3},
			{"removed", "four", 4},
			{"added", "FOUR", 4},
			{"context", "five", 5},
		})
	})

	t.Run("removed rows are numbered from the old file", func(t *testing.T) {
		f := oneFile(t, `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -48,10 +48,7 @@ func hello() {
 line48
 line49
-removed50
-removed51
-removed52
 line53
 line54
 line55
+added56`)
		assertRows(t, f, []rowWant{
			{"context", "line48", 48},
			{"context", "line49", 49},
			{"removed", "removed50", 50},
			{"removed", "removed51", 51},
			{"removed", "removed52", 52},
			{"context", "line53", 50},
			{"context", "line54", 51},
			{"context", "line55", 52},
			{"added", "added56", 53},
		})
	})

	t.Run("the counters restart at each hunk", func(t *testing.T) {
		f := oneFile(t, `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,4 @@
 line1
+added2
 line2
 line3
@@ -20,5 +21,4 @@
 line20
-line21-old
 line22
 line23
-line24-old
 line25
+added25`)
		assertRows(t, f, []rowWant{
			{"context", "line1", 1},
			{"added", "added2", 2},
			{"context", "line2", 3},
			{"context", "line3", 4},
			{"context", "line20", 21},
			{"removed", "line21-old", 21},
			{"context", "line22", 22},
			{"context", "line23", 23},
			{"removed", "line24-old", 24},
			{"context", "line25", 24},
			{"added", "added25", 25},
		})
	})

	t.Run("an unreadable hunk header numbers nothing after it", func(t *testing.T) {
		// Carrying the previous hunk's counters forward would print rows at line numbers the file
		// does not have. The readable rows keep theirs; anything emitted after the garbage header
		// must carry no number at all.
		f := oneFile(t, "diff --git a/x.txt b/x.txt\n"+
			"index abc1234..def5678 100644\n"+
			"--- a/x.txt\n+++ b/x.txt\n"+
			"@@ -1,2 +1,2 @@\n one\n-TWO\n+two\n"+
			"@@ garbage not a header\n"+
			"+NOT-NUMBERED\n")
		assertRows(t, f, []rowWant{
			{"context", "one", 1},
			{"removed", "TWO", 2},
			{"added", "two", 2},
		})
		for _, r := range f.Rows {
			if r.Text == "NOT-NUMBERED" && r.LineNumber != nil {
				t.Errorf("a row after an unreadable header was numbered %v: %s", *r.LineNumber, rowVariants(f))
			}
		}
	})

	t.Run("the no-newline notice is not a row", func(t *testing.T) {
		f := oneFile(t, `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-old line
\ No newline at end of file
+new line
\ No newline at end of file`)
		assertRows(t, f, []rowWant{
			{"removed", "old line", 1},
			{"added", "new line", 1},
		})
	})

	t.Run("a file with only context is still a file", func(t *testing.T) {
		f := oneFile(t, `diff --git a/nochange.ts b/nochange.ts
index abc..abc 100644
--- a/nochange.ts
+++ b/nochange.ts
@@ -1 +1 @@
 same line`)
		if f.Added != 0 || f.Removed != 0 {
			t.Errorf("added/removed = %d/%d, want 0/0", f.Added, f.Removed)
		}
		assertRows(t, f, []rowWant{{"context", "same line", 1}})
	})
}

// A badge the grammar gets wrong is a lie in the panel: a brand-new file shown as modified, or a
// deletion shown as an edit. `new file mode` is the signal, and `--- /dev/null` is its stand-in when
// git abbreviates the header.
func TestParseUnifiedDiffBadges(t *testing.T) {
	t.Run("new file mode", func(t *testing.T) {
		f := oneFile(t, `diff --git a/newfile.ts b/newfile.ts
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ a/newfile.ts
@@ -0,0 +1,2 @@
+line1
+line2`)
		if !f.IsNew {
			t.Error("IsNew = false, want true")
		}
		if f.Added != 2 {
			t.Errorf("added = %d, want 2", f.Added)
		}
	})

	t.Run("/dev/null alone", func(t *testing.T) {
		f := oneFile(t, `diff --git a/blank.ts b/blank.ts
--- /dev/null
+++ b/blank.ts
@@ -0,0 +1 @@
+first`)
		if !f.IsNew {
			t.Error("IsNew = false; `--- /dev/null` means the file did not exist before")
		}
	})

	t.Run("a new file with nothing in it keeps its entry", func(t *testing.T) {
		// A diff header with no hunk at all: the entry must survive with zero rows.
		f := oneFile(t, `diff --git a/empty.txt b/empty.txt
new file mode 100644
index 0000000..e69de29`)
		if !f.IsNew {
			t.Error("expected IsNew to be true for new blank file")
		}
		if len(f.Rows) != 0 || f.Added != 0 {
			t.Errorf("blank file parsed to %d rows and %d added, want 0 and 0", len(f.Rows), f.Added)
		}
	})

	t.Run("deleted file mode", func(t *testing.T) {
		f := oneFile(t, `diff --git a/deleted.ts b/deleted.ts
deleted file mode 100644
index abc1234..0000000
--- a/deleted.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-line1
-line2`)
		if !f.IsDeleted {
			t.Error("IsDeleted = false, want true")
		}
		if f.Removed != 2 {
			t.Errorf("removed = %d, want 2", f.Removed)
		}
	})

	t.Run("a modified file is not new", func(t *testing.T) {
		f := oneFile(t, `diff --git a/existing.txt b/existing.txt
index abc1234..def5678 100644
--- a/existing.txt
+++ b/existing.txt
@@ -1,3 +1,4 @@
 line1
+new line
 line2
 line3`)
		if f.IsNew {
			t.Error("expected IsNew to be false for modified file")
		}
		if f.Added != 1 {
			t.Errorf("expected 1 added line, got %d", f.Added)
		}
	})
}

// A file git refuses to diff still arrives with a size and a reason. Without the notice the panel
// rendered an empty file that looks like a bug rather than like a binary.
func TestParseUnifiedDiffBinaryAndLargeFileAnnotations(t *testing.T) {
	t.Run("git's own binary notice", func(t *testing.T) {
		f := oneFile(t, `diff --git a/image.png b/image.png
Binary files a/image.png and b/image.png differ`)
		if f.Path != "image.png" {
			t.Errorf("path = %q, want image.png", f.Path)
		}
		if !f.IsBinary {
			t.Error("IsBinary = false; git said so in the header and the parser ignored it")
		}
		if len(f.Rows) != 1 {
			t.Fatalf("rows = %d, want 1 carrying the notice", len(f.Rows))
		}
		if f.Rows[0].Text != "Binary files a/image.png and b/image.png differ" {
			t.Errorf("notice row = %q", f.Rows[0].Text)
		}
	})

	t.Run("the task pipeline's binary annotation", func(t *testing.T) {
		f := oneFile(t, `diff --git a/large.bin b/large.bin
# binary-file: 5242880
Binary files a/large.bin and b/large.bin differ`)
		if f.FileSize == nil || *f.FileSize != 5242880 {
			t.Errorf("fileSize = %v, want 5242880", f.FileSize)
		}
		if !f.IsBinary {
			t.Error("isBinary = false for a # binary-file annotation")
		}
	})

	t.Run("the task pipeline's large-file annotation", func(t *testing.T) {
		f := oneFile(t, `diff --git a/big.txt b/big.txt
# large-file: 1048576`)
		if f.FileSize == nil || *f.FileSize != 1048576 {
			t.Errorf("fileSize = %v, want 1048576", f.FileSize)
		}
		if f.IsBinary {
			t.Error("isBinary = true for a # large-file annotation; a big text file is not binary")
		}
	})
}

// git C-quotes a path containing a double quote, so the header arrives as
// `diff --git "a/quo\"te.ts" "b/quo\"te.ts"`. An extractor that required a literal `a/` prefix
// returned "" for it, and every such file collapsed into one entry keyed by the empty string — a
// wrong path is bad enough, but a shared key silently merges two files' rows.
func TestParseUnifiedDiffQuotedPathIsUnquoted(t *testing.T) {
	patch := "diff --git \"a/quo\\\"te.ts\" \"b/quo\\\"te.ts\"\n" +
		"index 20a747d..c0c119b 100644\n" +
		"--- \"a/quo\\\"te.ts\"\n" +
		"+++ \"b/quo\\\"te.ts\"\n" +
		"@@ -1,3 +1,3 @@\n x\n-Y\n+Y2\n"

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1: %+v", len(files), files)
	}
	f := files[0]
	if f.Path == "" {
		t.Fatal("path is empty — the quoted header was not parsed, and every quoted file merges into this one")
	}
	if f.Path != `quo"te.ts` {
		t.Errorf("path = %q, want %s (the C-quotes must be undone)", f.Path, `quo"te.ts`)
	}
}

// The parser answers a map, so the order the panel lists files in comes from here: an unsorted answer
// reorders the file list on every refresh.
func TestParseUnifiedDiffListsFilesInPathOrder(t *testing.T) {
	t.Run("two files keep their paths and their own counts", func(t *testing.T) {
		files := parseForTest(t, `diff --git a/file1.ts b/file1.ts
index 111..222 100644
--- a/file1.ts
+++ b/file1.ts
@@ -1,2 +1,3 @@
 line1
+added1
 line2
diff --git a/file2.ts b/file2.ts
index 333..444 100644
--- a/file2.ts
+++ b/file2.ts
@@ -1,2 +1,2 @@
-old
+new`)
		if len(files) != 2 {
			t.Fatalf("got %d files, want 2", len(files))
		}
		if files[0].Path != "file1.ts" || files[1].Path != "file2.ts" {
			t.Errorf("paths = %q, %q — want file1.ts then file2.ts (sorted)", files[0].Path, files[1].Path)
		}
		if files[0].Added != 1 {
			t.Errorf("file1 added = %d, want 1", files[0].Added)
		}
		if files[1].Removed != 1 {
			t.Errorf("file2 removed = %d, want 1", files[1].Removed)
		}
	})

	t.Run("three files come back in path order, not patch order", func(t *testing.T) {
		var patch strings.Builder
		for _, p := range []string{"zulu.ts", "alpha.ts", "mike.ts"} {
			patch.WriteString("diff --git a/" + p + " b/" + p + "\n")
			patch.WriteString("--- a/" + p + "\n+++ b/" + p + "\n@@ -1 +1 @@\n-x\n+y\n")
		}
		files := parseForTest(t, patch.String())
		if len(files) != 3 {
			t.Fatalf("got %d files, want 3", len(files))
		}
		want := []string{"alpha.ts", "mike.ts", "zulu.ts"}
		for i, w := range want {
			if files[i].Path != w {
				t.Errorf("file %d = %q, want %q", i, files[i].Path, w)
			}
		}
	})
}
