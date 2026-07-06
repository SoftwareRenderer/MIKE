package stream

import (
	"testing"
)

// TestIsGitPath verifies the isGitPath helper correctly identifies .git paths.
func TestIsGitPath(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		// Normal files — should NOT be filtered
		{"main.go", false},
		{"src/main.go", false},
		{"README.md", false},
		{"lib/utils.ts", false},
		{".env", false},
		{".gitignore", false},
		{".gitattributes", false},
		{"docs/.gitkeep", false}, // .gitkeep is not .git dir
		{"src/.github/config.yml", false}, // .github is not .git

		// .git directory — should be filtered
		{".git", true},
		{".git/config", true},
		{".git/HEAD", true},
		{".git/objects/pack/abc.pack", true},
		{".git/refs/heads/main", true},
		{".git/index", true},
		{".git/logs/HEAD", true},
		{".git/worktrees/task-abc/gitdir", true},
		{".git/FETCH_HEAD", true},

		// Nested .git directories (submodules, worktrees)
		{"vendor/lib/.git", true},
		{"vendor/lib/.git/modules/sub", true},
		{"worktrees/feature/.git/config", true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := isGitPath(tc.path)
			if got != tc.expected {
				t.Errorf("isGitPath(%q) = %v, want %v", tc.path, got, tc.expected)
			}
		})
	}
}

// TestIsInternalFile verifies that internal files (.git + flight recorder) are detected.
func TestIsInternalFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		// Normal files — should NOT be filtered
		{"main.go", false},
		{"src/main.go", false},
		{"README.md", false},
		{"data/kanban.db", false},
		{"kanban.trace.bak", false}, // not the exact name
		{"trace/kanban.trace", true}, // basename is kanban.trace, matches

		// .git paths — should be filtered (via isGitPath)
		{".git/index", true},
		{".git/HEAD", true},
		{"vendor/lib/.git", true},

		// Flight recorder files — should be filtered
		{"kanban.trace", true},
		{"kanban.trace.old", true},
		{"data/kanban.trace", true},
		{"data/kanban.trace.old", true},
		{"tmp/kanban.trace", true},
		{"tmp/kanban.trace.old", true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := isInternalFile(tc.path)
			if got != tc.expected {
				t.Errorf("isInternalFile(%q) = %v, want %v", tc.path, got, tc.expected)
			}
		})
	}
}

// TestFilterInternalPathsFromHashes verifies that internal paths are removed from file hash maps.
func TestFilterInternalPathsFromHashes(t *testing.T) {
	hashes := map[string]string{
		"main.go":              "abc123",
		".git/index":           "git123",
		"src/utils.ts":         "def456",
		".git/objects/pack.xz": "git456",
		"vendor/lib/.git":      "git789",
		"README.md":            "ghi789",
		"kanban.trace":         "trace123",
		"kanban.trace.old":     "trace456",
		"data/kanban.trace":    "trace789",
	}

	filtered := filterInternalPathsFromHashes(hashes)

	// Should keep normal files
	if _, ok := filtered["main.go"]; !ok {
		t.Error("expected main.go in filtered hashes")
	}
	if _, ok := filtered["src/utils.ts"]; !ok {
		t.Error("expected src/utils.ts in filtered hashes")
	}
	if _, ok := filtered["README.md"]; !ok {
		t.Error("expected README.md in filtered hashes")
	}

	// Should NOT contain .git paths
	if _, ok := filtered[".git/index"]; ok {
		t.Error("did not expect .git/index in filtered hashes")
	}
	if _, ok := filtered[".git/objects/pack.xz"]; ok {
		t.Error("did not expect .git/objects/pack.xz in filtered hashes")
	}
	if _, ok := filtered["vendor/lib/.git"]; ok {
		t.Error("did not expect vendor/lib/.git in filtered hashes")
	}

	// Should NOT contain flight recorder files
	if _, ok := filtered["kanban.trace"]; ok {
		t.Error("did not expect kanban.trace in filtered hashes")
	}
	if _, ok := filtered["kanban.trace.old"]; ok {
		t.Error("did not expect kanban.trace.old in filtered hashes")
	}
	if _, ok := filtered["data/kanban.trace"]; ok {
		t.Error("did not expect data/kanban.trace in filtered hashes")
	}

	if len(filtered) != 3 {
		t.Errorf("expected 3 entries, got %d: %v", len(filtered), filtered)
	}
}

// TestFilterInternalPathsFromList verifies that internal paths are removed from string slices.
func TestFilterInternalPathsFromList(t *testing.T) {
	paths := []string{
		"main.go",
		".git/index",
		"src/utils.ts",
		".git/refs/heads/main",
		"vendor/lib/.git",
		"README.md",
		"kanban.trace",
		"kanban.trace.old",
		"data/kanban.trace",
	}

	filtered := filterInternalPathsFromList(paths)

	expected := []string{"main.go", "src/utils.ts", "README.md"}
	if len(filtered) != len(expected) {
		t.Fatalf("expected %d entries, got %d: %v", len(expected), len(filtered), filtered)
	}
	for i, p := range expected {
		if filtered[i] != p {
			t.Errorf("filtered[%d] = %q, want %q", i, filtered[i], p)
		}
	}
}

// TestFilterInternalPathsFromList_empty verifies empty/nil input handling.
func TestFilterInternalPathsFromList_empty(t *testing.T) {
	// nil input
	result := filterInternalPathsFromList(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}

	// empty input
	result = filterInternalPathsFromList([]string{})
	if len(result) != 0 {
		t.Errorf("expected empty for empty input, got %v", result)
	}

	// all internal paths
	allInternal := []string{".git/index", ".git/HEAD", "sub/.git", "kanban.trace", "kanban.trace.old"}
	result = filterInternalPathsFromList(allInternal)
	if len(result) != 0 {
		t.Errorf("expected empty when all paths are internal, got %v", result)
	}
}

// TestFilterInternalPathsFromHashes_empty verifies empty/nil input handling.
func TestFilterInternalPathsFromHashes_empty(t *testing.T) {
	// nil input
	result := filterInternalPathsFromHashes(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map for nil input, got %v", result)
	}

	// empty input
	result = filterInternalPathsFromHashes(map[string]string{})
	if len(result) != 0 {
		t.Errorf("expected empty map for empty input, got %v", result)
	}
}
