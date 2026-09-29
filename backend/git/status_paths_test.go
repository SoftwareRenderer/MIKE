package git

import "testing"

// TestParseStatusPaths pins the porcelain v1 reading every "what changed in this worktree"
// caller relies on: modified, staged, untracked, renames (destination wins) and paths git
// chose to quote.
func TestParseStatusPaths(t *testing.T) {
	t.Parallel()
	out := "M  main.go\n?? broken.go\n M touched.go\nR  old.go -> renamed.go\n?? \"quoted name.go\"\nA  dir/new.go\n"
	got := ParseStatusPaths(out)
	want := map[string]bool{
		"main.go": true, "broken.go": true, "touched.go": true,
		"renamed.go": true, "quoted name.go": true, "dir/new.go": true,
	}
	if len(got) != len(want) {
		t.Fatalf("ParseStatusPaths(%q) = %v", out, got)
	}
	for _, f := range got {
		if !want[f] {
			t.Fatalf("unexpected entry %q in %v", f, got)
		}
	}
	if len(ParseStatusPaths("")) != 0 || len(ParseStatusPaths("\n\n")) != 0 {
		t.Fatal("empty porcelain output must yield no paths")
	}
}
