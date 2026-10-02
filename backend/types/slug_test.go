package types

import (
	"strings"
	"testing"
)

func TestSlugify_NormalTitle(t *testing.T) {
	if got := Slugify("Fix login bug #123", 0); got != "fix-login-bug-123" {
		t.Errorf("Slugify = %q, want %q", got, "fix-login-bug-123")
	}
}

func TestSlugify_CollapsesRunsAndTrims(t *testing.T) {
	// Non-ASCII letters are dropped, not transliterated — they act as word
	// separators, which is why "café" becomes "caf".
	tests := map[string]string{
		"a  b":         "a-b",
		"a--b":         "a-b",
		"a__b":         "a-b",
		"a /b/ c":      "a-b-c",
		"/a/b/c":       "a-b-c", // leading separator dropped
		"foo/":         "foo",   // trailing separator dropped
		"Foo Bar":      "foo-bar",
		"Ünïcode!":     "n-code",
		"café-au-lait": "caf-au-lait",
		"":             "",
		"   ":          "",
	}
	for in, want := range tests {
		if got := Slugify(in, 0); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSlugify_UncappedWhenMaxLenNonPositive pins the project-ID usage: maxLen
// <= 0 must never truncate.
func TestSlugify_UncappedWhenMaxLenNonPositive(t *testing.T) {
	long := strings.Repeat("a", 300)
	for _, maxLen := range []int{0, -1} {
		if got := Slugify(long, maxLen); len(got) != 300 {
			t.Errorf("Slugify(long, %d) len = %d, want 300", maxLen, len(got))
		}
	}
}

func TestSlugify_CapsAtMaxLen(t *testing.T) {
	if got := Slugify("aaa bbb ccc", 7); got != "aaa-bbb" {
		t.Errorf("Slugify cap = %q, want %q", got, "aaa-bbb")
	}
}

func TestSlugifyBranchName_AllSpecialChars(t *testing.T) {
	if slug := SlugifyBranchName("###"); slug != "" {
		t.Errorf("expected empty string, got %q", slug)
	}
}

func TestSlugifyBranchName_AllHyphens(t *testing.T) {
	if slug := SlugifyBranchName("---"); slug != "" {
		t.Errorf("expected empty string, got %q", slug)
	}
}

func TestSlugifyBranchName_MaxLength(t *testing.T) {
	// Title that slugifies to more than BranchSlugMaxLen chars.
	longTitle := "Fix a very long bug description that exceeds the maximum allowed branch name length limit"
	slug := SlugifyBranchName(longTitle)
	if len(slug) > BranchSlugMaxLen {
		t.Errorf("slug len = %d (%q), want <= %d", len(slug), slug, BranchSlugMaxLen)
	}
	// The cap exists so the prefix still fits: this is what keeps a task branch
	// name inside MaxBranchName.
	if len(TaskBranchPrefix+slug) > MaxBranchName {
		t.Errorf("task/%s = %d chars, want <= %d", slug, len(TaskBranchPrefix+slug), MaxBranchName)
	}
}

func TestBranchSlugMaxLen_DerivedFromMaxBranchName(t *testing.T) {
	if BranchSlugMaxLen != MaxBranchName-len(TaskBranchPrefix) {
		t.Errorf("BranchSlugMaxLen = %d, want %d", BranchSlugMaxLen, MaxBranchName-len(TaskBranchPrefix))
	}
	if MaxBranchName != 50 {
		t.Errorf("MaxBranchName = %d, want 50 (established fact: task branches are capped at 50)", MaxBranchName)
	}
}

// TestSlugifyBranchName_NeverStartsWithHyphen verifies that a slug can never
// start with '-', preventing Git argument injection (see ValidateBranchName).
func TestSlugifyBranchName_NeverStartsWithHyphen(t *testing.T) {
	inputs := []string{
		"-fix login", // starts with hyphen
		"  -fix",     // whitespace then hyphen
		"---fix",     // multiple hyphens
		"-123",       // hyphen then digits
		"---",        // all hyphens
		"-",          // single hyphen
		"-a-b-c",     // hyphen-separated
		"Fix login",  // normal title
		"123 fix",    // starts with digit
		"!!!fix!!!",  // special chars
		"",           // empty
	}
	for _, input := range inputs {
		slug := SlugifyBranchName(input)
		if len(slug) > 0 && slug[0] == '-' {
			t.Errorf("SlugifyBranchName(%q) = %q starts with hyphen", input, slug)
		}
	}
}

// TestTaskBranchSlugPrefix pins the parser that matches a live branch back to its task's slug. It must
// be the shape the executor writes, or a GC that parses a different shape deletes live branches.
func TestTaskBranchSlugPrefix(t *testing.T) {
	tests := map[string]string{
		"task/count-hello-files-abc123":    "task/count-hello-files-",
		"task/fix-login-bug-xyz789":        "task/fix-login-bug-",
		"task/frost-amber-quartz-abc123":   "task/frost-amber-quartz-",
		"task/simple-abc123":               "task/simple-",
		"task/123-abc123":                  "task/123-",
		"master":                           "",
		"main":                             "",
		"task/nosuffix":                    "", // bare canonical branch
		"task/too-short-ab":                "", // suffix shorter than TaskBranchSuffixLen
		"task/suffix-too-long-abc1234":     "", // not a bare random tail
		"task/UPPER-CASE-SUFFIX-ABC123":    "", // suffix charset is lowercase alnum
		"feature/count-hello-files-abc123": "", // outside the task/ namespace
		"task/":                            "",
	}
	for branch, want := range tests {
		if got := TaskBranchSlugPrefix(branch); got != want {
			t.Errorf("TaskBranchSlugPrefix(%q) = %q, want %q", branch, got, want)
		}
	}
}

// TestTaskBranchSlugPrefix_RoundTripsWithSlugifier is the contract the GC and
// Main-thread recovery actually rely on: a branch name built the way the
// executor builds one parses back to exactly TaskBranchPrefix+slug+"-", the same
// string taskSlugPrefixSet records for the task.
func TestTaskBranchSlugPrefix_RoundTripsWithSlugifier(t *testing.T) {
	for _, title := range []string{
		"Fix login bug #123", "Count lines in hello.c", "WIP", "  spaces  everywhere  ",
		strings.Repeat("long ", 20),
	} {
		slug := SlugifyBranchName(title)
		if slug == "" {
			continue
		}
		branch := TaskBranchPrefix + slug + "-" + strings.Repeat("a", TaskBranchSuffixLen)
		if want := TaskBranchPrefix + slug + "-"; TaskBranchSlugPrefix(branch) != want {
			t.Errorf("TaskBranchSlugPrefix(%q) = %q, want %q", branch, TaskBranchSlugPrefix(branch), want)
		}
	}
}

// TestSlugifyBranchName_OutputPassesRefValidation pins the contract the branch
// GC depends on: a slug of arbitrary title text must always be a legal git ref,
// otherwise the GC's `git branch --list task/<slug>-*` lookup is unsafe.
func TestSlugifyBranchName_OutputPassesRefValidation(t *testing.T) {
	for _, title := range []string{
		"Fix login bug #123", "Merge: a/b [urgent]?", "~~~", "- leading",
		"tab\tsep", "a^b", "a~1", "a*b", "a[b", `a\b`, "a:b", "a?b", "  ", "ok",
	} {
		slug := SlugifyBranchName(title)
		if slug == "" {
			continue
		}
		if err := ValidateBranchName(TaskBranchPrefix + slug); err != nil {
			t.Errorf("SlugifyBranchName(%q) = %q rejected as a ref: %v", title, slug, err)
		}
	}
}
