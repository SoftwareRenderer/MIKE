package git

import (
	"fmt"
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

// taskCommitFormat defines the git log format used for task commit lists. It uses
// %x1f as a separator because commit subjects may contain pipes.
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

// TaskCommitsIn reads the commit log from the checkout in dir. It returns commits
// in the range baseRef..headRef. If baseRef is provided, it also resolves the base commit.
// --end-of-options is used to prevent refs starting with '-' from being read as flags.
func TaskCommitsIn(dir, baseRef, headRef string) (*TaskCommitsResult, error) {
	if headRef == "" {
		headRef = "HEAD"
	}
	result := &TaskCommitsResult{Commits: []TaskCommit{}}

	// The divergence point is display metadata: an unresolvable baseRef still leaves a
	// commit list worth showing.
	if baseRef != "" {
		if baseOut, err := Run(dir, "log", taskCommitFormat, "-1", "--end-of-options", baseRef); err == nil {
			if base := ParseTaskCommits(baseOut); len(base) > 0 {
				result.BaseCommit = &base[0]
			}
		}
	}

	ref := headRef
	if baseRef != "" {
		ref = baseRef + ".." + headRef
	}
	out, err := Run(dir, "log", taskCommitFormat, "--end-of-options", ref)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	result.Commits = ParseTaskCommits(out)
	return result, nil
}
