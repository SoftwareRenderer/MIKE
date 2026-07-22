package types

import (
	"strings"
	"testing"
)

func TestTaskWordsCount(t *testing.T) {
	expected := 256
	if len(taskWords) != expected {
		t.Errorf("expected %d words, got %d", expected, len(taskWords))
	}
}

func TestTaskWordsNoDuplicates(t *testing.T) {
	seen := make(map[string]int, len(taskWords))
	for _, w := range taskWords {
		seen[w]++
	}

	var dupes []string
	for w, count := range seen {
		if count > 1 {
			dupes = append(dupes, w)
		}
	}
	if len(dupes) > 0 {
		t.Errorf("duplicate words: %v", dupes)
	}
}

func TestTaskWordsAllLowercase(t *testing.T) {
	for _, w := range taskWords {
		if w != strings.ToLower(w) {
			t.Errorf("word not lowercase: %q", w)
		}
	}
}

func TestTaskWordsAllNonEmpty(t *testing.T) {
	for _, w := range taskWords {
		if w == "" {
			t.Error("found empty string in taskWords")
		}
	}
}

func TestGenerateTaskID(t *testing.T) {
	id := GenerateTaskID()
	parts := splitID(t, id, 3)
	for _, part := range parts {
		if !contains(taskWords, part) {
			t.Errorf("task ID part %q not in taskWords", part)
		}
	}
}

func TestGenerateThreadID(t *testing.T) {
	id := GenerateThreadID()
	parts := splitID(t, id, 2)
	for _, part := range parts {
		if !contains(taskWords, part) {
			t.Errorf("thread ID part %q not in taskWords", part)
		}
	}
}

func splitID(t *testing.T, id string, expectedParts int) []string {
	t.Helper()
	parts := split(id, "-")
	if len(parts) != expectedParts {
		t.Fatalf("expected %d parts in %q, got %d", expectedParts, id, len(parts))
	}
	return parts
}

func split(s, sep string) []string {
	// simple split to avoid importing strings in helpers
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep[0] {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
