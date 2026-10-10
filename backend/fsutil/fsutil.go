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

// InsideRoots reports whether an already-resolved path is one of roots or below it. The comparison
// is lexical, so it is only sound once the candidate has been through ResolveSymlinks() and the roots
// through NormalizeRoots() — both in the same form. Contained() is what does both halves for a path that
// has not been resolved.
func InsideRoots(resolved string, roots []string) bool { return inside(resolved, roots) }

// RootForms returns a directory in the two forms a candidate path is compared against: absolute and
// cleaned, and symlink-resolved — the same string when it does not resolve yet. ok is false for a
// blank entry, which jails nothing.
func RootForms(raw string) (absolute, resolved string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	absolute = raw
	if !filepath.IsAbs(absolute) {
		if a, err := filepath.Abs(absolute); err == nil {
			absolute = a
		}
	}
	absolute = filepath.Clean(absolute)
	resolved = absolute
	if r, err := filepath.EvalSymlinks(absolute); err == nil {
		resolved = filepath.Clean(r)
	}
	return absolute, resolved, true
}

// NormalizeRoots puts each root in the form a candidate path is compared in — the resolved one when
// it resolves — dropping blanks and duplicates.
func NormalizeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, raw := range roots {
		_, resolved, ok := RootForms(raw)
		if !ok || seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	return out
}

// inside reports whether p is one of roots or below it. Empty roots are skipped, so an entry that
// carried no path cannot open the jail.
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

// ErrSwapped says the name a containment check accepted no longer names that file. It names no path:
// a refusal answered from it reaches the requester, and the host path is not theirs.
var ErrSwapped = errors.New("path changed after containment")

// ErrNotRegular says the name holds something other than a file. Opening a FIFO waits for its writer.
var ErrNotRegular = errors.New("path is not a regular file")

// OpenVerified opens a path Contained() returned and returns the file only while it is still the file
// that check looked at: the check and the open are two syscalls, and whoever owns the directory can put
// another file, a link or a FIFO at the name in between. before is the name's os.Lstat() as checked —
// os.Stat() describes where a link points — and the caller closes the file.
func OpenVerified(p string, before os.FileInfo) (*os.File, os.FileInfo, error) {
	if before == nil {
		return nil, nil, ErrNotRegular
	}
	// Contained() never resolves to a link, so a link here was planted after the check: that is a swap.
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, ErrSwapped
	}
	if !before.Mode().IsRegular() {
		return nil, nil, ErrNotRegular
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !os.SameFile(before, info) {
		f.Close()
		return nil, nil, ErrSwapped
	}
	return f, info, nil
}

// OpenChecked is OpenVerified() for a caller that has no use for the name's stat of its own. A caller
// that answers with a size takes OpenVerified(), so the number is the one the check saw.
func OpenChecked(p string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(p)
	if err != nil {
		return nil, nil, err
	}
	return OpenVerified(p, before)
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
