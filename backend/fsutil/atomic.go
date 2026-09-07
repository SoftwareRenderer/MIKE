package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// AtomicTempPrefix names the temporary file WriteFileAtomic writes through. The
// watchers must know it: a rename also fires events for the temporary name, and
// a watcher reports one fs_event per project per 500 ms, so a slow save would
// otherwise tell the browser to create a file that has already been renamed away.
const AtomicTempPrefix = ".mike-tmp-"

// Keeps the generated name inside NAME_MAX; a long file name must still save.
const tempNameBaseMaxLen = 180

// IsAtomicTemp reports whether path is one of WriteFileAtomic's temporary files.
func IsAtomicTemp(path string) bool {
	return strings.HasPrefix(filepath.Base(path), AtomicTempPrefix)
}

// WriteFileAtomic replaces the contents of path through a temporary file in the same directory
// (rename is only atomic within one filesystem), so a crash mid-write costs the temporary, never
// the file. Like os.WriteFile: an existing file keeps its mode, a new one is 0644, and the parent
// directory must exist.
//
// A symlinked target is refused: renaming over a link would redirect a path the caller already
// containment-checked. The file is synced before the rename, the directory is not.
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
	// Any failure below leaves a temporary behind. Removing it is best-effort: a
	// failed unlink must not hide the error that caused it.
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
