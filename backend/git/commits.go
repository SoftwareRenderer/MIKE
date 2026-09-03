package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// TaskCommit represents a single commit in the task's history.
type TaskCommit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
}

// TaskCommitsResult holds the base commit info and list of task-specific commits.
type TaskCommitsResult struct {
	BaseCommit *TaskCommit  `json:"base_commit,omitempty"` // commit at the divergence point
	Commits    []TaskCommit `json:"commits"`
}

// taskCommitFormat is the `git log --format` behind every task commit list. Both readers
// of that log (the worktree, and the project checkout after commit-and-done) parse this
// one format, so it is declared once.
//
// The separator is %x1f, not '|': a subject may contain a pipe, which would arrive as a
// truncated subject and a date that is really part of the subject.
const taskCommitFormat = "--format=%H%x1f%h%x1f%s%x1f%aI"

// ParseTaskCommits turns that output into commits, newest first. A line missing any of
// the four fields is dropped rather than half-parsed.
func ParseTaskCommits(out string) []TaskCommit {
	commits := []TaskCommit{}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 4)
		if len(parts) != 4 {
			continue
		}
		commits = append(commits, TaskCommit{
			Hash:    parts[0],
			Short:   parts[1],
			Subject: parts[2],
			Date:    parts[3],
		})
	}
	return commits
}

// TaskCommitsIn reads a task's commit log from the checkout in dir: BaseCommit is the
// commit baseRef names (nil if empty or unresolvable) and Commits are those in
// baseRef..headRef. An empty headRef means HEAD; an empty baseRef means all of headRef.
// A live worktree asks for HEAD; a task whose worktree is gone asks for the branch tip
// stored on its row. Refs get --end-of-options so a leading dash is not read as a flag.
func TaskCommitsIn(dir, baseRef, headRef string) (*TaskCommitsResult, error) {
	if headRef == "" {
		headRef = "HEAD"
	}
	result := &TaskCommitsResult{Commits: []TaskCommit{}}

	// The divergence point is display metadata: an unresolvable baseRef still leaves a
	// commit list worth showing.
	if baseRef != "" {
		if baseOut, err := runGitInDir(dir, "log", taskCommitFormat, "-1", "--end-of-options", baseRef); err == nil {
			if base := ParseTaskCommits(baseOut); len(base) > 0 {
				result.BaseCommit = &base[0]
			}
		}
	}

	ref := headRef
	if baseRef != "" {
		ref = baseRef + ".." + headRef
	}
	out, err := runGitInDir(dir, "log", taskCommitFormat, "--end-of-options", ref)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	result.Commits = ParseTaskCommits(out)
	return result, nil
}

// runGitInDir runs git in dir and returns what it printed.
func runGitInDir(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
