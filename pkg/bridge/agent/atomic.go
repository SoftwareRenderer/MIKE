package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeAtomic replaces the contents of path with content in one visible step: write a
// temp file in the same directory (same filesystem, so the rename is atomic), set the
// mode, rename over the target. A reader therefore sees either the old file or the new
// one, never a half-written file — which matters because the bridge writes while the
// task's own shell, an LSP server and the editor are all reading the same checkout.
//
// This is the only place that sequence lives. It used to be copied verbatim into
// handleWrite and handleEdit, and because the bridge is a *committed binary*, a fix
// applied to one copy is an edit that silently never runs in the other.
//
// The mode is 0o644 exactly as both copies had it, so an edit still drops the executable
// bit. That is a behaviour worth deciding on on its own — preserving the target's mode
// means stating what a brand-new file gets, since the file need not exist yet — and it is
// deliberately not smuggled into a de-duplication.
//
// The parent directory is the caller's business: write creates it, edit requires the file
// it is editing to already exist.
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
