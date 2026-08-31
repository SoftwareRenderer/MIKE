package types

import (
	"regexp"
	"strconv"
	"strings"
)

// MaxBranchName is the maximum length for any git branch name.
const MaxBranchName = 50

// TaskBranchPrefix is the branch namespace every task branch lives in.
const TaskBranchPrefix = "task/"

// BranchSlugMaxLen is the longest title slug that still fits behind
// TaskBranchPrefix inside MaxBranchName.
const BranchSlugMaxLen = MaxBranchName - len(TaskBranchPrefix)

// Slugify turns s into a lowercase slug of ASCII letters and digits
func Slugify(s string, maxLen int) string {
	var result []rune
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			result = append(result, r)
		case len(result) > 0 && result[len(result)-1] != '-':
			result = append(result, '-')
		}
	}
	out := strings.Trim(string(result), "-")
	if maxLen > 0 && len(out) > maxLen {
		out = out[:maxLen]
	}
	return out
}

// SlugifyBranchName is the slug of a task title as it appears inside a task
// branch name — e.g. "Fix login bug #123" → "fix-login-bug-123". The result is
// capped so TaskBranchPrefix+slug fits within MaxBranchName, and can never
// start with '-' (see ValidateBranchName: a leading '-' would read as a git
// option).
func SlugifyBranchName(title string) string {
	return Slugify(title, BranchSlugMaxLen)
}

// TaskBranchSuffixLen is the length of the random suffix every task branch name
// carries (task/<slug>-<random6>) so a new task never collides with a stale
// branch left by a deleted one. exec writes it; the parser below reads it.
const TaskBranchSuffixLen = 6

// taskBranchSlugRe matches a titled task's branch name: task/<slug>-<random>.
var taskBranchSlugRe = regexp.MustCompile(
	`^(` + regexp.QuoteMeta(TaskBranchPrefix) + `.+)-[a-z0-9]{` + strconv.Itoa(TaskBranchSuffixLen) + `}$`,
)

// TaskBranchSlugPrefix returns the slug prefix ("task/<slug>-") of a slug-named
// task branch, or "" when the branch is not slug-named (a bare task/<taskID>, an
// ordinary branch, or a slug branch without its random suffix).
func TaskBranchSlugPrefix(branch string) string {
	m := taskBranchSlugRe.FindStringSubmatch(branch)
	if len(m) >= 2 {
		return m[1] + "-"
	}
	return ""
}
