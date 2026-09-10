package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Replaces a file in one visible step: temp in the same directory, chmod, rename over the target.
func writeAtomic(path, content string) (int, error) {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), ".kanban-tmp-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()

	// Every failure removes the temp file. A leftover `.kanban-tmp-*` is not invisible
	// housekeeping: it shows up in the task's own `git status` and a `git add -A` would
	// commit it.
	n, err := tmpFile.WriteString(content)
	if err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("write content: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("rename to %s: %w", path, err)
	}

	return n, nil
}
