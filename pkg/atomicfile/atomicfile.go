// Package atomicfile replaces a file in one visible step and names the temporary file the write
// leaves until the rename lands. The server and the sandbox bridge both write through it, so the
// host's ignore rule meets one name however the write was asked for.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TempPrefix is the name a mid-write file carries. The guest writes it and the host's filters drop it,
// so both sides read it from here; a leftover is committed by a `git add -A`.
const TempPrefix = ".mike-tmp-"

// tempNameBaseMaxLen keeps a temporary name within NAME_MAX.
const tempNameBaseMaxLen = 180

// IsTemp reports whether path is one of this package's temporary files.
func IsTemp(path string) bool {
	return strings.HasPrefix(filepath.Base(path), TempPrefix)
}

// Write replaces path's contents in one visible step: a temporary file beside it, the bytes, the
// mode, a sync, then the rename over the target. It reports the bytes written, and removes the
// temporary file on every failure.
//
// An existing regular file keeps its own mode, so an edit does not quietly strip the +x from a script
// the task then commits; anything else, a link included, gets mode.
func Write(path string, content []byte, mode os.FileMode) (int, error) {
	if existing, err := os.Lstat(path); err == nil && existing.Mode().IsRegular() {
		mode = existing.Mode().Perm()
	}

	pattern := TempPrefix
	if base := filepath.Base(path); len(base) <= tempNameBaseMaxLen {
		pattern += base + "-"
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	n, err := tmp.Write(content)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("write content: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return 0, fmt.Errorf("rename to %s: %w", path, err)
	}
	return n, nil
}
