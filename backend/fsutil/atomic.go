package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// AtomicTempPrefix is used by WriteFileAtomic; watchers ignore these files to avoid reporting temporary names during renames.
const AtomicTempPrefix = ".mike-tmp-"

// Capped to ensure temporary filenames stay within NAME_MAX.
const tempNameBaseMaxLen = 180

// IsAtomicTemp reports whether path is one of WriteFileAtomic's temporary files.
func IsAtomicTemp(path string) bool {
	return strings.HasPrefix(filepath.Base(path), AtomicTempPrefix)
}

// WriteFileAtomic replaces the file contents via a temporary file in the same directory to ensure atomicity. Existing files keep their mode; new files are 0644. Symlinks are refused to prevent redirection of containment-checked paths. The file is synced before renaming.
func WriteFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)

	mode := os.FileMode(0644)
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return &os.PathError{Op: "write", Path: path, Err: os.ErrInvalid}
		}
		mode = fi.Mode().Perm()
	}

	pattern := AtomicTempPrefix
	if base := filepath.Base(path); len(base) <= tempNameBaseMaxLen {
		pattern += base + "-"
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort removal of temporary files on failure to avoid hiding the primary error.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
