// Package fsutil answers one question: is this path inside that root, once
// symlinks are followed?
//
// The fs handlers, the preview handler and the extension mount policy must agree
// on the answer: a symlink resolves against the host, so comparing strings alone
// lets a link planted in a project reach outside it.
package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Inside reports whether p is one of roots or below it. Empty roots are
// skipped. Lexical only — resolve with ResolveSymlinks first when this is a
// security check.
func Inside(p string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ResolveSymlinks resolves every symlink in p. A path that does not exist yet (a
// write target, a mount not yet created) has its nearest existing ancestor
// resolved and the missing tail re-joined, so a link on the existing part is
// still caught.
//
// A symlink standing where p itself would be created is an error: writing through
// it would land wherever the link points.
func ResolveSymlinks(p string) (string, error) {
	var tail []string
	cur := filepath.Clean(p)
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			out := resolved
			for _, seg := range tail {
				out = filepath.Join(out, seg)
				if fi, lErr := os.Lstat(out); lErr == nil && fi.Mode()&os.ModeSymlink != 0 {
					return "", fmt.Errorf("path %q traverses a dangling symlink at %q", p, out)
				}
			}
			return out, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor for %q", p)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}
