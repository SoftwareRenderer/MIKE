package types

import (
	"bytes"
	"os"
	"regexp"
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

// TestIDGenerationUsesCryptoRand guards the entropy source: task and thread IDs
// surface in URLs, git branch names and worktree paths, so they must not come
// from the clock-seeded math/rand.
func TestIDGenerationUsesCryptoRand(t *testing.T) {
	src, err := os.ReadFile("idgen.go")
	if err != nil {
		t.Fatalf("read idgen.go: %v", err)
	}
	if bytes.Contains(src, []byte(`"math/rand"`)) {
		t.Error(`idgen.go imports math/rand; word selection must use crypto/rand`)
	}
	if !bytes.Contains(src, []byte(`"crypto/rand"`)) {
		t.Error("idgen.go does not import crypto/rand")
	}
}

func TestGenerateTaskIDFormat(t *testing.T) {
	taskIDPattern := regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z]+$`)
	threadIDPattern := regexp.MustCompile(`^[a-z]+-[a-z]+$`)

	for i := 0; i < 500; i++ {
		id := GenerateTaskID()
		if !taskIDPattern.MatchString(id) {
			t.Fatalf("task ID %q does not match ^[a-z]+-[a-z]+-[a-z]+$", id)
		}
		for _, part := range strings.Split(id, "-") {
			if !contains(taskWords, part) {
				t.Fatalf("task ID %q uses the word %q, which is not in the dictionary", id, part)
			}
		}

		thread := GenerateThreadID()
		if !threadIDPattern.MatchString(thread) {
			t.Fatalf("thread ID %q does not match ^[a-z]+-[a-z]+$", thread)
		}
		for _, part := range strings.Split(thread, "-") {
			if !contains(taskWords, part) {
				t.Fatalf("thread ID %q uses the word %q, which is not in the dictionary", thread, part)
			}
		}
	}
}

// TestGenerateTaskIDUniqueness draws many IDs and requires them to be (almost
// all) distinct. 256^3 = 16.7M combinations make a birthday collision possible
// (≈0.8 expected over 5000 draws), but mass duplication can only come from a
// weak or repeatedly re-seeded source.
func TestGenerateTaskIDUniqueness(t *testing.T) {
	const draws = 5000
	seen := make(map[string]bool, draws)
	firstWords := make(map[string]bool, len(taskWords))
	dup := 0
	for i := 0; i < draws; i++ {
		id := GenerateTaskID()
		if seen[id] {
			dup++
		}
		seen[id] = true
		firstWords[strings.SplitN(id, "-", 2)[0]] = true
	}
	if dup > draws/200 {
		t.Errorf("%d duplicate task IDs over %d draws", dup, draws)
	}
	// A uniform draw over 256 words misses a word with probability ~e^-19.5.
	if len(firstWords) < 240 {
		t.Errorf("only %d distinct first words over %d draws; the source looks low-entropy", len(firstWords), draws)
	}
}

func TestGenerateThreadIDUniqueness(t *testing.T) {
	const draws = 2000
	seen := make(map[string]bool, draws)
	dup := 0
	for i := 0; i < draws; i++ {
		id := GenerateThreadID()
		if seen[id] {
			dup++
		}
		seen[id] = true
	}
	// 256^2 = 65536 combinations: a handful of birthday collisions is expected,
	// mass duplication is not.
	if dup > draws/20 {
		t.Errorf("%d duplicate thread IDs over %d draws", dup, draws)
	}
}
