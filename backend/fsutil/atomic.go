package fsutil

import (
	"os"

	"mike/pkg/atomicfile"
)

// IsAtomicTemp reports whether path is one of atomicfile's temporary files, the name a write carries
// while it is in flight.
func IsAtomicTemp(path string) bool {
	return atomicfile.IsTemp(path)
}

// WriteFileAtomic replaces the file contents via a temporary file in the same directory to ensure atomicity. Existing files keep their mode; new files are 0644. The bytes are synced before renaming. Symlinks are refused here, before anything is opened, to prevent redirection of containment-checked paths — the write itself is atomicfile's.
func WriteFileAtomic(path string, content []byte) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return &os.PathError{Op: "write", Path: path, Err: os.ErrInvalid}
	}
	_, err := atomicfile.Write(path, content, 0o644)
	return err
}
