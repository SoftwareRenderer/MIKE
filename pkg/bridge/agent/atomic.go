package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomic replaces the contents of path in one visible step: temp file in the same directory
// (so the rename is atomic), set the mode, rename over the target. A reader sees either the old
// file or the new one — the bridge writes while the task's shell, an LSP server and the editor
// are reading the same checkout. This is the only place that sequence lives, because the bridge
// is a committed binary and a fix applied to one copy never runs in the other.
//
// The mode is 0o644, so an edit still drops the executable bit: preserving the target's mode
// needs a decision about brand-new files and is not smuggled in here. The parent directory is the
// caller's business — write creates it, edit requires it.
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
