package types

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestTaskWordsAreUsableAsIDParts covers both dictionary rules at once. Every word has to survive
// being embedded in a URL, a git branch name and a worktree path, so a word that is empty or carries
// anything outside [a-z] is a bug in the dictionary rather than in the generator; and a word listed
// twice shrinks the space an ID is drawn from, so two tasks can be named alike more often than the
// word count promises.
func TestTaskWordsAreUsableAsIDParts(t *testing.T) {
	seen := make(map[string]int, len(taskWords))
	for _, w := range taskWords {
		seen[w]++
		if w == "" {
			t.Error("found empty string in taskWords")
		}
		if w != strings.ToLower(w) {
			t.Errorf("word not lowercase: %q", w)
		}
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

// TestGenerateTaskIDFormat is the shape check for both generators: word count, the
// [a-z]+-(...)+ pattern, and dictionary membership — 500 draws each rather than the
// single draw the removed per-generator tests used, which could only ever pass.
func TestGenerateTaskIDFormat(t *testing.T) {
	taskIDPattern := regexp.MustCompile(`^[a-z]+-[a-z]+-[a-z]+$`)
	threadIDPattern := regexp.MustCompile(`^[a-z]+-[a-z]+$`)

	for i := 0; i < 500; i++ {
		id := GenerateTaskID()
		if !taskIDPattern.MatchString(id) {
			t.Fatalf("task ID %q does not match ^[a-z]+-[a-z]+-[a-z]+$", id)
		}
		for _, part := range strings.Split(id, "-") {
			if !slices.Contains(taskWords, part) {
				t.Fatalf("task ID %q uses the word %q, which is not in the dictionary", id, part)
			}
		}

		thread := GenerateThreadID()
		if !threadIDPattern.MatchString(thread) {
			t.Fatalf("thread ID %q does not match ^[a-z]+-[a-z]+$", thread)
		}
		for _, part := range strings.Split(thread, "-") {
			if !slices.Contains(taskWords, part) {
				t.Fatalf("thread ID %q uses the word %q, which is not in the dictionary", thread, part)
			}
		}
	}
}

// TestGeneratedIDsAreNotDuplicated draws many IDs from each generator and requires them to be (almost
// all) distinct: mass duplication can only come from a weak or repeatedly re-seeded source, which no
// shape check can see. The task draw also counts the distinct first words, because a source stuck on a
// few of them produces well-formed IDs out of a corner of the space.
func TestGeneratedIDsAreNotDuplicated(t *testing.T) {
	t.Run("task", func(t *testing.T) {
		const draws = 5000
		// 256^3 = 16.7M combinations make a birthday collision possible (≈0.8 expected over 5000
		// draws), but nothing near the threshold below.
		const allowedDupes = draws / 200
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
		if dup > allowedDupes {
			t.Errorf("%d duplicate task IDs over %d draws", dup, draws)
		}
		// A uniform draw over 256 words misses a word with probability ~e^-19.5.
		if len(firstWords) < 240 {
			t.Errorf("only %d distinct first words over %d draws; the source looks low-entropy", len(firstWords), draws)
		}
	})

	t.Run("thread", func(t *testing.T) {
		const draws = 2000
		// 256^2 = 65536 combinations: a handful of birthday collisions is expected, mass duplication
		// is not.
		const allowedDupes = draws / 20
		seen := make(map[string]bool, draws)
		dup := 0
		for i := 0; i < draws; i++ {
			id := GenerateThreadID()
			if seen[id] {
				dup++
			}
			seen[id] = true
		}
		if dup > allowedDupes {
			t.Errorf("%d duplicate thread IDs over %d draws", dup, draws)
		}
	})
}
