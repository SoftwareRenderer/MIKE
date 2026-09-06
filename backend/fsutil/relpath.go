package fsutil

import (
	"fmt"
	"strings"
)

// Limits on a client-supplied relative path: past them the write fails with
// ENAMETOOLONG, which is a poor place to find out.
const (
	MaxRelPathLen    = 4096
	MaxRelSegmentLen = 255 // NAME_MAX on every supported filesystem
)

// ValidateRelPath checks the shape of a client-supplied path meant to be joined
// onto a directory that is already contained: non-empty, relative, free of
// "."/".." and empty segments, free of backslashes (another tool's separator) and
// control characters, within the length limits above, and not a name
// WriteFileAtomic's temporary files use. Shape only — whether the joined path is
// inside the project is a separate check (see Server.validateFSPath).
func ValidateRelPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("path is empty")
	}
	if len(rel) > MaxRelPathLen {
		return fmt.Errorf("path is too long (max %d characters)", MaxRelPathLen)
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("path must be relative")
	}
	if strings.ContainsRune(rel, '\\') {
		return fmt.Errorf(`path must not contain "\"`)
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("path contains a control character")
		}
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "":
			return fmt.Errorf("path contains an empty segment")
		case ".", "..":
			return fmt.Errorf(`path must not contain "." or ".."`)
		}
		if len(seg) > MaxRelSegmentLen {
			return fmt.Errorf("path segment is too long (max %d characters)", MaxRelSegmentLen)
		}
	}
	if strings.HasPrefix(rel[strings.LastIndex(rel, "/")+1:], AtomicTempPrefix) {
		return fmt.Errorf("file name is reserved")
	}
	return nil
}
