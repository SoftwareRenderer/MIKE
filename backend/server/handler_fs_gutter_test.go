package server

// The editor's gutter asks one question per working-tree line: did `git diff HEAD` touch it? The answer
// arrives as GET /api/fs/read's gutter_markers, and it is derived from git.ParseUnifiedDiff rather than a
// parser of this package's own. These cases are the shapes that parser used to get wrong on its own.

import (
	"testing"
)

// The lines every patch below repeats: git always writes them before the first hunk.
const diffHeader = `diff --git a/f.txt b/f.txt
index 1234567..89abcde 100644
--- a/f.txt
+++ b/f.txt
`

func TestGutterMarkers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		patch string
		want  map[int][]string
	}{
		{
			name:  "no diff has no markers",
			patch: "",
			want:  nil,
		},
		{
			name: "an added line marks itself",
			patch: diffHeader + `@@ -1,2 +1,3 @@
 a
+b
 c
`,
			want: map[int][]string{2: {"added"}},
		},
		{
			name: "a removed line marks the line it follows",
			patch: diffHeader + `@@ -1,3 +1,2 @@
 a
-b
 c
`,
			want: map[int][]string{1: {"removed"}},
		},
		{
			name: "a removal followed by an addition is one modification",
			patch: diffHeader + `@@ -1,3 +1,3 @@
 a
-b
+B
 c
`,
			want: map[int][]string{2: {"modified"}},
		},
		{
			// The parser this replaces marked line 2 "modified" *and* "removed" here: the second deletion
			// was never accounted for. A line that exists is not removed.
			name: "several deletions replaced by one addition mark one line modified",
			patch: diffHeader + `@@ -1,4 +1,3 @@
 a
-b
-c
+B
 d
`,
			want: map[int][]string{2: {"modified"}},
		},
		{
			name: "a deletion at the top of a hunk marks the line before the hunk",
			patch: diffHeader + `@@ -5,2 +5 @@
-b
 c
`,
			want: map[int][]string{4: {"removed"}},
		},
		{
			name: "trailing deletions mark the last line the hunk kept",
			patch: diffHeader + `@@ -1,3 +1,2 @@
 a
 b
-c
`,
			want: map[int][]string{2: {"removed"}},
		},
		{
			name: "a second hunk marks inside its own lines, not the first hunk's",
			patch: diffHeader + `@@ -1,2 +1,3 @@
 a
+new
 b
@@ -20,2 +21,2 @@
-old
+newer
`,
			want: map[int][]string{2: {"added"}, 21: {"modified"}},
		},
		{
			name: "the no-newline notice shifts nothing",
			patch: diffHeader + `@@ -1,2 +1,2 @@
 a
-b
+B
\ No newline at end of file
`,
			want: map[int][]string{2: {"modified"}},
		},
		{
			name: "a rename keeps marking the new file's lines",
			patch: `diff --git a/old.txt b/new.txt
similarity index 90%
rename from old.txt
rename to new.txt
index 1234567..89abcde 100644
--- a/new.txt
+++ b/new.txt
@@ -1,2 +1,2 @@
 a
-b
+B
`,
			want: map[int][]string{2: {"modified"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gutterMarkers(tt.patch)
			if len(got) != len(tt.want) {
				t.Fatalf("gutterMarkers() = %v, want %v", got, tt.want)
			}
			for line, kinds := range tt.want {
				if len(got[line]) != len(kinds) {
					t.Errorf("line %d = %v, want %v", line, got[line], kinds)
					continue
				}
				for i := range kinds {
					if got[line][i] != kinds[i] {
						t.Errorf("line %d = %v, want %v", line, got[line], kinds)
					}
				}
			}
		})
	}
}
