package exec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mike/backend/git"
)

// ErrNotAMergeConflict means git merge failed without a merge in progress (no MERGE_HEAD).
// This happens for hook errors, missing branches, or locks. Callers use errors.Is to show
// the underlying git error.
//
// It contains text to ensure it's visible in logs even if not wrapped with %w.
var ErrNotAMergeConflict = errors.New("not a merge conflict")

// isMergeInProgress reports if a git merge is in progress in dir. It uses
// git rev-parse --git-dir to resolve the git directory in both repos and worktrees.
func isMergeInProgress(dir string) bool {
	cmd := git.Command(dir, "rev-parse", "--git-dir")
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
// It uses git status --porcelain and falls back to git diff --diff-filter=U for
// binary conflicts and renames. Lists are read with -z to preserve paths with non-ASCII characters.
func GetConflictedFiles(dir string) []string {
	seen := make(map[string]struct{})
	var files []string

	// Primary: git status --porcelain -z
	cmd := git.Command(dir, "status", "--porcelain", "-z")
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
		cmd = git.Command(dir, "diff", "--name-only", "-z", "--diff-filter=U")
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

// addUnique appends f to files if not already in seen.
func addUnique(files []string, seen map[string]struct{}, f string) []string {
	if _, ok := seen[f]; ok {
		return files
	}
	seen[f] = struct{}{}
	return append(files, f)
}

// GetAllConflictsResolved reports if every conflicted file is resolved. It uses
// git diff --check MERGE_HEAD to detect markers. All files must be clean for a merge commit.
func GetAllConflictsResolved(dir string) bool {
	conflictedFiles := GetConflictedFiles(dir)
	if len(conflictedFiles) == 0 {
		return true
	}

	// Check MERGE_HEAD exists (we should be in a merge state).
	// If not, fall back to file-content scan.
	mergeHeadCmd := git.Command(dir, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	if mergeHeadCmd.Run() == nil {
		// Use git diff --check to detect conflict markers.
		// Compares working tree against MERGE_HEAD; any markers in the
		// diff are reported as warnings and cause a non-zero exit code.
		args := append([]string{"diff", "--check", "MERGE_HEAD", "--"}, conflictedFiles...)
		cmd := git.Command(dir, args...)
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

// GetResolvedConflictedFiles returns conflicted files to stage for the resolution commit.
// If any file still has conflict markers, it returns nil.
func GetResolvedConflictedFiles(dir string) []string {
	if GetAllConflictsResolved(dir) {
		return GetConflictedFiles(dir)
	}
	return nil
}

// getMergeStatusOutput returns the raw git status --porcelain output for diagnostics.
func getMergeStatusOutput(dir string) string {
	cmd := git.Command(dir, "status", "--porcelain")
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
