package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validatePath ensures the path stays within the current working directory (worktree).
// Rejects absolute paths, .. traversal, and symlink escapes.
func validatePath(path string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	cleaned := filepath.Clean(path)

	// Reject absolute paths
	if filepath.IsAbs(cleaned) {
		return fmt.Errorf("absolute paths not allowed: %s", path)
	}

	// Reject .. traversal
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("path escape rejected: %s", path)
	}

	// Resolve full path and verify it stays within CWD
	fullPath := filepath.Join(cwd, cleaned)
	cwdPrefix := cwd + string(os.PathSeparator)
	if fullPath != cwd && !strings.HasPrefix(fullPath, cwdPrefix) {
		return fmt.Errorf("path escape rejected: %s resolves outside worktree", path)
	}

	return nil
}


