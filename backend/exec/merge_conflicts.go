package exec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mike/backend/git"
)

// ErrNotAMergeConflict means git merge failed with no merge in progress (no MERGE_HEAD): a
// hook error, a missing branch, a lock — not a conflict. Callers detect it with errors.Is and show
// the underlying git error instead.
//
// It carries text because an error that prints as nothing is a bug waiting for the one path that
// surfaces it alone; being wrapped with %w is not the only way an error reaches a log.
var ErrNotAMergeConflict = errors.New("not a merge conflict")

// isMergeInProgress checks whether a git merge is currently in progress in the given directory.
// Uses `git rev-parse --git-dir` to correctly resolve the git directory in both
// regular repos and worktrees (where .git is a file, not a directory).
func isMergeInProgress(dir string) bool {
	cmd := git.Command("", "rev-parse", "--git-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(string(out))
	// git-dir might be relative (e.g., "../.git/worktrees/mytree")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	_, err = os.Stat(filepath.Join(gitDir, "MERGE_HEAD"))
	return err == nil
}

// GetConflictedFiles returns a deduplicated list of files with merge conflicts.
// Uses git status --porcelain (catches UU, AD, DU, AA, DD, UD, UA) and falls back
// to git diff --diff-filter=U (catches binary conflicts and renames).
//
// Both listings are read with -z: every name here goes back to git as a path, and the plain
// form is display text that quotes a name outside plain ASCII.
func GetConflictedFiles(dir string) []string {
	seen := make(map[string]struct{})
	var files []string

	// Primary: git status --porcelain -z
	cmd := git.Command("", "status", "--porcelain", "-z")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		entries := strings.Split(string(out), "\x00")
		for i := 0; i < len(entries); i++ {
			line := entries[i]
			if len(line) < 4 {
				continue
			}
			status := line[:2]
			if strings.ContainsAny(status, "RC") {
				i++ // a rename or copy names its source path in the next field
				continue
			}
			if strings.ContainsRune(status, 'U') {
				files = addUnique(files, seen, line[3:])
			}
		}
	}

	// Fallback: git diff --name-only -z --diff-filter=U (binary conflicts, renames)
	if len(files) == 0 {
		cmd = git.Command("", "diff", "--name-only", "-z", "--diff-filter=U")
		cmd.Dir = dir
		if out, err := cmd.Output(); err == nil {
			for _, name := range strings.Split(string(out), "\x00") {
				if name != "" {
					files = addUnique(files, seen, name)
				}
			}
		}
	}

	return files
}

// addUnique appends f to files unless seen already holds it, recording it in
// seen. Dedup only — callers must not care whether f was new.
func addUnique(files []string, seen map[string]struct{}, f string) []string {
	if _, ok := seen[f]; ok {
		return files
	}
	seen[f] = struct{}{}
	return append(files, f)
}

// GetAllConflictsResolved reports whether every conflicted file is resolved, by asking
// `git diff --check MERGE_HEAD -- <files>` for conflict markers rather than scanning file
// contents. It needs all of them clean: git refuses the merge commit until every conflict is
// staged, so partial resolution does not count.
func GetAllConflictsResolved(dir string) bool {
	conflictedFiles := GetConflictedFiles(dir)
	if len(conflictedFiles) == 0 {
		return true
	}

	// Check MERGE_HEAD exists (we should be in a merge state).
	// If not, fall back to file-content scan.
	mergeHeadCmd := git.Command("", "rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	mergeHeadCmd.Dir = dir
	if mergeHeadCmd.Run() == nil {
		// Use git diff --check to detect conflict markers.
		// Compares working tree against MERGE_HEAD; any markers in the
		// diff are reported as warnings and cause a non-zero exit code.
		args := append([]string{"diff", "--check", "MERGE_HEAD", "--"}, conflictedFiles...)
		cmd := git.Command("", args...)
		cmd.Dir = dir
		return cmd.Run() == nil
	}

	// Fallback: scan file contents for markers (should not be reached in
	// normal operation — MERGE_HEAD always exists during an active merge).
	for _, f := range conflictedFiles {
		if hasConflictMarkers(filepath.Join(dir, f)) {
			return false
		}
	}
	return true
}

// hasConflictMarkers checks if a file contains git merge conflict markers.
// Kept as a fallback when MERGE_HEAD is unavailable.
func hasConflictMarkers(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "<<<<<<<") ||
		strings.Contains(content, "=======") ||
		strings.Contains(content, ">>>>>>>")
}

// GetResolvedConflictedFiles returns the conflicted files to stage for the
// resolution commit. If all conflicts are resolved, returns all conflicted
// files (git requires ALL to be staged for the merge commit). If any file
// still has conflict markers, returns nil — the caller should skip the commit
// and re-prompt the LLM.
func GetResolvedConflictedFiles(dir string) []string {
	if GetAllConflictsResolved(dir) {
		return GetConflictedFiles(dir)
	}
	return nil
}

// getMergeStatusOutput returns the raw git status --porcelain output for diagnostics.
func getMergeStatusOutput(dir string) string {
	cmd := git.Command("", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("(git status failed: %v)", err)
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return "(working tree clean)"
	}
	return result
}
