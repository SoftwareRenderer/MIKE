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

// inside reports whether p is one of roots or below it. Empty roots are skipped. It is
// unexported because it is lexical only: the answer this package gives to "is this path
// inside that root" is Contained, which requires that too *and* the symlink-resolved
// path, so there is no way to reach the weak half from outside.
func inside(p string, roots []string) bool {
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

// ErrOutside says a path is not under any of the roots it was jailed to.
var ErrOutside = errors.New("path is outside the allowed directory")

// Contained returns the form of path that is safe to open, after requiring both the
// lexical path and the symlink-resolved path to sit inside one of roots. Roots are
// compared lexically and resolved, so a root behind a link still works.
//
// The resolution is the part that matters: a symlink inside a project resolves
// against the host, so a string comparison alone reads through a `link -> /etc`
// planted in a worktree a task can write to.
func Contained(path string, roots []string) (string, error) {
	clean := filepath.Clean(path)
	if !inside(clean, roots) {
		return "", fmt.Errorf("%w: %q", ErrOutside, clean)
	}
	resolved, err := ResolveSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", clean, err)
	}
	check := make([]string, 0, 2*len(roots))
	check = append(check, roots...)
	for _, root := range roots {
		if rr, err := filepath.EvalSymlinks(root); err == nil {
			check = append(check, filepath.Clean(rr))
		}
	}
	if !inside(resolved, check) {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrOutside, clean, resolved)
	}
	return resolved, nil
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
