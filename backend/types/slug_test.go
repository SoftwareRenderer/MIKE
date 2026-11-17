package types

import (
	"strings"
	"testing"
)

// TestSlugify is the one slugifier every name MIKE derives from user text goes through: lowercase
// ASCII letters and digits, runs of anything else collapsed to a single '-', and nothing left at either
// end. Non-ASCII letters are dropped, not transliterated — they act as word separators, which is why
// "café" becomes "caf".
func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Fix login bug #123": "fix-login-bug-123",
		"a  b":               "a-b",
		"a--b":               "a-b",
		"a__b":               "a-b",
		"a /b/ c":            "a-b-c",
		"/a/b/c":             "a-b-c", // leading separator dropped
		"foo/":               "foo",   // trailing separator dropped
		"Foo Bar":            "foo-bar",
		"Ünïcode!":           "n-code",
		"café-au-lait":       "caf-au-lait",
		"":                   "",
		"   ":                "",
	} {
		if got := Slugify(in, 0); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSlugifyMaxLen pins the cap, and the project-ID usage of it: maxLen <= 0 never truncates, and a
// positive maxLen is the length the slug ends at.
func TestSlugifyMaxLen(t *testing.T) {
	long := strings.Repeat("a", 300)
	for _, tc := range []struct {
		in          string
		maxLen      int
		want        string
		wantUnbound bool
	}{
		{in: long, maxLen: 0, wantUnbound: true},
		{in: long, maxLen: -1, wantUnbound: true},
		{in: "aaa bbb ccc", maxLen: 7, want: "aaa-bbb"},
	} {
		got := Slugify(tc.in, tc.maxLen)
		if tc.wantUnbound {
			if len(got) != len(tc.in) {
				t.Errorf("Slugify(long, %d) len = %d, want the untruncated %d", tc.maxLen, len(got), len(tc.in))
			}
			continue
		}
		if got != tc.want {
			t.Errorf("Slugify(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
		}
	}
}

// TestSlugifyBranchNameNamesASafeRef pins what a title slug may become inside a branch name. The slug
// reaches git as part of `git branch --list task/<slug>-*` and as the name a branch is created with, so
// it must never start with '-' (git would read the name as an option), must always be a ref this repo
// accepts, and must stay short enough for TaskBranchPrefix plus the random tail to fit MaxBranchName.
// A title holding nothing but separators has no slug at all.
func TestSlugifyBranchNameNamesASafeRef(t *testing.T) {
	const longTitle = "Fix a very long bug description that exceeds the maximum allowed branch name length limit"
	for _, tc := range []struct {
		title string
		// want is the exact slug, for the titles whose shape is the point; "" with wantExact set is
		// the nothing-survives case.
		want      string
		wantExact bool
	}{
		{title: "###", want: "", wantExact: true},
		{title: "---", want: "", wantExact: true},
		{title: "-", want: "", wantExact: true},
		{title: "", want: "", wantExact: true},
		{title: "!!!fix!!!", want: "fix", wantExact: true},
		{title: "Fix login", want: "fix-login", wantExact: true},
		{title: "-fix login", want: "fix-login", wantExact: true},
		{title: "  -fix", want: "fix", wantExact: true},
		{title: "---fix", want: "fix", wantExact: true},
		{title: "-123", want: "123", wantExact: true},
		{title: "-a-b-c", want: "a-b-c", wantExact: true},
		{title: "123 fix", want: "123-fix", wantExact: true},
		{title: "Fix login bug #123"},
		{title: "Merge: a/b [urgent]?"},
		{title: "~~~"},
		{title: "- leading"},
		{title: "tab\tsep"},
		{title: "a^b"},
		{title: "a~1"},
		{title: "a*b"},
		{title: "a[b"},
		{title: `a\b`},
		{title: "a:b"},
		{title: "a?b"},
		{title: "  "},
		{title: "ok"},
		{title: longTitle},
	} {
		slug := SlugifyBranchName(tc.title)
		if tc.wantExact && slug != tc.want {
			t.Errorf("SlugifyBranchName(%q) = %q, want %q", tc.title, slug, tc.want)
		}
		if strings.HasPrefix(slug, "-") {
			t.Errorf("SlugifyBranchName(%q) = %q, which git would read as an option", tc.title, slug)
		}
		if len(TaskBranchPrefix+slug) > MaxBranchName {
			t.Errorf("SlugifyBranchName(%q) = %q: task/%s is %d chars, want <= %d",
				tc.title, slug, slug, len(TaskBranchPrefix+slug), MaxBranchName)
		}
		if err := ValidateBranchName(TaskBranchPrefix + slug); err != nil {
			t.Errorf("SlugifyBranchName(%q) = %q rejected as a ref: %v", tc.title, slug, err)
		}
	}
}

// TestTaskBranchSlugPrefix pins the parser that matches a live branch back to its task's slug. It must
// be the shape the executor writes, or a GC that parses a different shape deletes live branches.
func TestTaskBranchSlugPrefix(t *testing.T) {
	t.Run("refuses what is not a slug-named task branch", func(t *testing.T) {
		for branch, want := range map[string]string{
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
		} {
			if got := TaskBranchSlugPrefix(branch); got != want {
				t.Errorf("TaskBranchSlugPrefix(%q) = %q, want %q", branch, got, want)
			}
		}
	})

	// The contract the GC and Main-thread recovery actually rely on: a branch name built the way the
	// executor builds one parses back to exactly TaskBranchPrefix+slug+"-", the same string
	// taskSlugPrefixSet records for the task.
	t.Run("parses a branch the executor writes", func(t *testing.T) {
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
	})
}
