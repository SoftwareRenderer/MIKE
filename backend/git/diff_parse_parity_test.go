package git

// Ported from frontend/src/lib/utils/__tests__/diff-parser.unit.ts: those cases are the contract of the
// parser being deleted, and a parser cannot be deleted until the surviving one provably covers what it
// handled. Case for case here - including the two behaviours the Go parser did NOT have when the port
// began (see the binary-notice and quoted-path tests).

import (
	"strings"
	"testing"
)

// parseForTest is the parity entry point: one file per diff header, in path order.
func parseForTest(t *testing.T, patch string) []TrackedDiffFile {
	t.Helper()
	return ParseUnifiedDiff(patch)
}

// rowVariants renders the rows as a compact string so a failure names the shape, not just a count.
func rowVariants(f TrackedDiffFile) string {
	parts := make([]string, 0, len(f.Rows))
	for _, r := range f.Rows {
		parts = append(parts, r.Variant)
	}
	return strings.Join(parts, ",")
}

func TestParseReturnsNothingForEmptyInput(t *testing.T) {
	for _, in := range []string{"", "   ", "\n"} {
		if got := ParseUnifiedDiff(in); len(got) != 0 {
			t.Errorf("ParseUnifiedDiff(%q) = %d files, want 0", in, len(got))
		}
	}
}

func TestParseSimpleSingleFileDiff(t *testing.T) {
	patch := `diff --git a/src/main.ts b/src/main.ts
index abc1234..def5678 100644
--- a/src/main.ts
+++ b/src/main.ts
@@ -1,3 +1,4 @@
 const x = 1;
-const y = 2;
+const y = 3;
+const z = 4;
 const w = 5;`

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	f := files[0]
	if f.Path != "src/main.ts" {
		t.Errorf("path = %q, want src/main.ts", f.Path)
	}
	if f.Added != 2 || f.Removed != 1 {
		t.Errorf("added/removed = %d/%d, want 2/1", f.Added, f.Removed)
	}
	if len(f.Rows) != 5 {
		t.Fatalf("rows = %d (%s), want 5", len(f.Rows), rowVariants(f))
	}
	want := []string{"context", "removed", "added", "added", "context"}
	for i, w := range want {
		if f.Rows[i].Variant != w {
			t.Errorf("row %d variant = %q, want %q (all: %s)", i, f.Rows[i].Variant, w, rowVariants(f))
		}
	}
}

func TestParseMultiFileDiff(t *testing.T) {
	patch := `diff --git a/file1.ts b/file1.ts
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
+new`

	files := parseForTest(t, patch)
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
}

func TestParseDetectsNewFiles(t *testing.T) {
	patch := `diff --git a/newfile.ts b/newfile.ts
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ a/newfile.ts
@@ -0,0 +1,2 @@
+line1
+line2`

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if !files[0].IsNew {
		t.Error("IsNew = false, want true")
	}
	if files[0].Added != 2 {
		t.Errorf("added = %d, want 2", files[0].Added)
	}
}

func TestParseDetectsDeletedFiles(t *testing.T) {
	patch := `diff --git a/deleted.ts b/deleted.ts
deleted file mode 100644
index abc1234..0000000
--- a/deleted.ts
+++ /dev/null
@@ -1,2 +0,0 @@
-line1
-line2`

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if !files[0].IsDeleted {
		t.Error("IsDeleted = false, want true")
	}
	if files[0].Removed != 2 {
		t.Errorf("removed = %d, want 2", files[0].Removed)
	}
}

// TestParseCarriesTheBinaryNotice is one of the two behaviours the Go parser lacked at port time.
// Without an annotation (which only the task-diff pipeline produces — a commit-to-commit diff has
// none), a binary file produced zero rows and no size, so the panel rendered an empty file that looks
// like a bug rather than like a file git refuses to diff.
func TestParseCarriesTheBinaryNotice(t *testing.T) {
	patch := `diff --git a/image.png b/image.png
Binary files a/image.png and b/image.png differ`

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	f := files[0]
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
}

// TestParseHonoursBinaryAndLargeFileAnnotations covers the task-diff pipeline's own markers, which
// replace the hunks of files too big or binary to ship.
func TestParseHonoursBinaryAndLargeFileAnnotations(t *testing.T) {
	bin := parseForTest(t, `diff --git a/large.bin b/large.bin
# binary-file: 5242880
Binary files a/large.bin and b/large.bin differ`)
	if len(bin) != 1 {
		t.Fatalf("got %d files, want 1", len(bin))
	}
	if bin[0].FileSize == nil || *bin[0].FileSize != 5242880 {
		t.Errorf("fileSize = %v, want 5242880", bin[0].FileSize)
	}
	if !bin[0].IsBinary {
		t.Error("isBinary = false for a # binary-file annotation")
	}

	large := parseForTest(t, `diff --git a/big.txt b/big.txt
# large-file: 1048576`)
	if len(large) != 1 {
		t.Fatalf("got %d files, want 1", len(large))
	}
	if large[0].FileSize == nil || *large[0].FileSize != 1048576 {
		t.Errorf("fileSize = %v, want 1048576", large[0].FileSize)
	}
	if large[0].IsBinary {
		t.Error("isBinary = true for a # large-file annotation; a big text file is not binary")
	}
}

func TestParseSkipsNoNewlineMarkers(t *testing.T) {
	patch := `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-old line
\ No newline at end of file
+new line
\ No newline at end of file`

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if len(files[0].Rows) != 2 {
		t.Fatalf("rows = %d (%s), want 2 — the marker lines must not become rows", len(files[0].Rows), rowVariants(files[0]))
	}
	if files[0].Rows[0].Variant != "removed" || files[0].Rows[1].Variant != "added" {
		t.Errorf("variants = %s, want removed,added", rowVariants(files[0]))
	}
}

func TestParseLineNumbersTrackThroughHunks(t *testing.T) {
	patch := `diff --git a/file.ts b/file.ts
--- a/file.ts
+++ b/file.ts
@@ -5,3 +5,4 @@
 context1
-removed1
+added1
+added2
 context2`

	files := parseForTest(t, patch)
	if len(files) != 1 || len(files[0].Rows) != 5 {
		t.Fatalf("want 1 file with 5 rows, got %d/%d", len(files), len(files[0].Rows))
	}
	want := []int{5, 6, 6, 7, 8} // a removed row carries the OLD line number
	for i, w := range want {
		got := files[0].Rows[i].LineNumber
		if got == nil {
			t.Fatalf("row %d has no line number", i)
		}
		if *got != w {
			t.Errorf("row %d lineNumber = %d, want %d", i, *got, w)
		}
	}
}

func TestParseKeepsContextOnlyFiles(t *testing.T) {
	patch := `diff --git a/nochange.ts b/nochange.ts
index abc..abc 100644
--- a/nochange.ts
+++ b/nochange.ts
@@ -1 +1 @@
 same line`

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1 — a file with rows but no +/- is still a file", len(files))
	}
	if files[0].Added != 0 || files[0].Removed != 0 {
		t.Errorf("added/removed = %d/%d, want 0/0", files[0].Added, files[0].Removed)
	}
}

// TestParseQuotedPathIsUnquoted is the second behaviour the Go parser lacked. git C-quotes a path
// containing a double quote, so the header arrives as `diff --git "a/quo\"te.ts" "b/quo\"te.ts"`.
// The old path extractor required a literal `a/` prefix, returned "" for it, and every such file in a
// diff collapsed into a single entry keyed by the empty string — a wrong path is bad enough, but a
// shared key silently merges two files' rows.
func TestParseQuotedPathIsUnquoted(t *testing.T) {
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

// TestParseSortsByPath pins the wire contract: parseUnifiedDiffToFiles answers a map, and a handler
// that serialised it straight out would reorder the diff panel's file list on every refresh.
func TestParseSortsByPath(t *testing.T) {
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
}

// TestParseDetectsNewFileFromDevNullOnly covers a diff without the `new file mode` line: git omits it
// when the header is abbreviated, and the frontend parser used /dev/null as a signal. A brand-new file
// shown as modified is a badge lie, not a cosmetic miss.
func TestParseDetectsNewFileFromDevNullOnly(t *testing.T) {
	patch := `diff --git a/blank.ts b/blank.ts
--- /dev/null
+++ b/blank.ts
@@ -0,0 +1 @@
+first`

	files := parseForTest(t, patch)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if !files[0].IsNew {
		t.Error("IsNew = false; `--- /dev/null` means the file did not exist before")
	}
}
