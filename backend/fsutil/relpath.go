package fsutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Limits on a client-supplied relative path: past them the write fails with
// ENAMETOOLONG, which is a poor place to find out.
const (
	MaxRelPathLen    = 4096
	MaxRelSegmentLen = 255 // NAME_MAX on every supported filesystem
)

// RelInsideRoot reports whether rel (produced by filepath.Rel) names a target that is the root itself or something under it. It rejects ".." and paths starting with "../". For symlink-resolved checks, use Contained(). For paths using forward slashes on all platforms, see server.repoRelativePath().
func RelInsideRoot(rel string) bool {
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// RelBelowRoot reports whether rel is strictly below the root (not the root itself).
func RelBelowRoot(rel string) bool {
	return rel != "." && RelInsideRoot(rel)
}

// ValidateRelPath ensures a client-supplied relative path is well-formed: no absolute paths, no "." or "..", no empty segments, no backslashes or control characters, and within length limits. It also rejects names reserved for atomic writes.
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
