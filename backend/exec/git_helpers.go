package exec

import (
	"os/exec"
	"strings"

	"kanban/backend/git"
)

// binaryPathsFromStaged returns paths of staged binary non-image files.
func binaryPathsFromStaged(numstatOutput string) []string {
	var paths []string
	for _, line := range strings.Split(numstatOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		// Binary files show "-" in numstat
		if strings.TrimSpace(parts[0]) != "-" {
			continue
		}
		path := parts[2]
		if isImageFile(path) {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// unstagePaths resets paths from staging via WorktreeManager.
func unstagePaths(wm *git.WorktreeManager, taskID string, paths []string) {
	for _, path := range paths {
		_ = wm.GitRunInWorktree(taskID, append([]string{"reset", "HEAD", "--", path}, git.GitAuthorFlags()...)...)
	}
}

// unstagePathsInDir resets paths from staging in a plain directory.
func unstagePathsInDir(dir string, paths []string) {
	for _, path := range paths {
		_, _ = runGitCommand(dir, "reset", "HEAD", "--", path)
	}
}

// runGitCommand runs a git command in a directory.
func runGitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
