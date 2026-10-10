package fsutil

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Every limit below is measured before any byte is copied, and a copy checks its running total as
// it writes, because the tree may still be growing while it is copied.

// The limits of one copy, or of a move that has to copy instead of renaming.
const (
	CopyMaxBytes   int64 = 64 << 20
	CopyMaxEntries       = 5000
	CopyMaxDepth         = 32
)

// UniqueNameMaxTries is how many names UniqueName() tries before giving up.
const UniqueNameMaxTries = 200

// GitDirName is the name that takes a path out of the file operations.
const GitDirName = ".git"

// The errors a caller reports per item. None of them names a host path; the handler logs that.
var (
	// ErrProtectedPath: the path is a project checkout or a task worktree, or holds one.
	ErrProtectedPath = errors.New("the project or task directory itself cannot be deleted")
	// ErrGitPath: the path is or is under a .git directory. Copying a worktree's .git pointer gives
	// a repository that answers for the wrong tree, and deleting it breaks every worktree the
	// project has.
	ErrGitPath = errors.New("a .git directory cannot be copied, moved, renamed or deleted")
	// ErrCopyTooLarge: the tree passed one of the limits above.
	ErrCopyTooLarge = errors.New("too large to copy")
	// ErrCopyIntoItself: the destination is inside the source.
	ErrCopyIntoItself = errors.New("cannot copy or move a directory into itself")
)

// HasGitSegment reports whether path is named .git or has it in the middle. The last name counts as
// well as the middle ones, because a worktree's .git is a pointer file rather than a folder.
func HasGitSegment(path string) bool {
	for _, seg := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if seg == GitDirName {
			return true
		}
	}
	return false
}

// guardCopyTarget rejects a move or copy whose destination is inside its source, which would
// recurse until the disk is full. Equal paths are a paste in place, which the caller decides about.
func guardCopyTarget(src, dst string) error {
	srcClean, dstClean := filepath.Clean(src), filepath.Clean(dst)
	if srcClean == dstClean {
		return nil
	}
	if inside(dstClean, []string{srcClean}) {
		return ErrCopyIntoItself
	}
	return nil
}

// UniqueName returns a name in dir that nothing has yet: `name` if it is free, otherwise
// `name copy`, `name copy2`, … keeping the extension. renamed says whether it had to change.
func UniqueName(dir, name string) (string, bool, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
		return "", false, fmt.Errorf("invalid name %q", name)
	}
	if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
		return name, false, nil
	}
	stem, ext := splitName(name)
	for i := 1; i <= UniqueNameMaxTries; i++ {
		candidate := stem + " copy" + ext
		if i > 1 {
			candidate = stem + " copy" + strconv.Itoa(i) + ext
		}
		if _, err := os.Lstat(filepath.Join(dir, candidate)); err != nil {
			return candidate, true, nil
		}
	}
	return "", false, fmt.Errorf("no free name beside %q", name)
}

// splitName splits a file name into stem and extension. A leading dot is part of the name, so
// ".gitignore" is all stem.
func splitName(name string) (stem, ext string) {
	dot := strings.LastIndex(name, ".")
	if dot <= 0 {
		return name, ""
	}
	return name[:dot], name[dot:]
}

// RemoveTree deletes path and everything under it. It rejects a protected root, anything holding
// one, and anything under .git. A symlink at path is deleted, not followed.
func RemoveTree(path string, protected []string) error {
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || clean == "." || clean == "" {
		return ErrProtectedPath
	}
	if HasGitSegment(clean) {
		return ErrGitPath
	}
	for _, root := range protected {
		if root == "" {
			continue
		}
		rootClean := filepath.Clean(root)
		// Deleting a folder that holds a tree root would delete that tree.
		if clean == rootClean || inside(rootClean, []string{clean}) {
			return ErrProtectedPath
		}
	}
	return os.RemoveAll(clean)
}

// CopyTree copies src to dst, which must not exist yet. Symlinks are copied as links with their
// target string unchanged (never read through), sockets, fifos and devices are skipped, and the
// whole tree is measured before the first byte is written. On any failure dst is removed again.
func CopyTree(src, dst string) error {
	srcClean, dstClean := filepath.Clean(src), filepath.Clean(dst)
	if err := guardCopyTarget(srcClean, dstClean); err != nil {
		return err
	}
	info, err := os.Lstat(srcClean)
	if err != nil {
		return err
	}
	if err := checkCopySize(srcClean, info); err != nil {
		return err
	}
	if err := copyOne(srcClean, dstClean, info); err != nil {
		_ = os.RemoveAll(dstClean)
		return err
	}
	return nil
}

// checkCopySize applies the byte, entry and depth limits before the copy starts. It does not
// follow symlinks, so a link out of the tree counts as its own size.
func checkCopySize(src string, info fs.FileInfo) error {
	if !info.IsDir() {
		if info.Mode().IsRegular() && info.Size() > CopyMaxBytes {
			return ErrCopyTooLarge
		}
		return nil
	}
	var total int64
	var entries int
	rootDepth := strings.Count(src, string(filepath.Separator))
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != src && d.IsDir() && d.Name() == GitDirName {
			return filepath.SkipDir // a nested repository is not copied wholesale
		}
		entries++
		if entries > CopyMaxEntries {
			return fmt.Errorf("%w: %d entries", ErrCopyTooLarge, CopyMaxEntries)
		}
		if depth := strings.Count(p, string(filepath.Separator)) - rootDepth; depth > CopyMaxDepth {
			return fmt.Errorf("%w: %d levels", ErrCopyTooLarge, CopyMaxDepth)
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		total += fi.Size()
		if total > CopyMaxBytes {
			return fmt.Errorf("%w: %d bytes", ErrCopyTooLarge, CopyMaxBytes)
		}
		return nil
	})
}

// copyOne copies src to dst. Which parts of the tree come along is walkTree's.
func copyOne(src, dst string, info fs.FileInfo) error {
	return walkTree(src, dst, string(filepath.Separator), info, copyEntry)
}

// copyEntry makes one visited entry into its copy.
func copyEntry(src, dst string, info fs.FileInfo) error {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		return os.Mkdir(dst, info.Mode().Perm())
	default: // the walk leaves only a regular file here
		return copyFile(src, dst, info)
	}
}

// copyFile streams src into dst, checking the byte limit as it goes so a file that grew after the size
// check is still stopped. It opens through OpenVerified(): the tree being copied belongs to the task.
func copyFile(src, dst string, info fs.FileInfo) (err error) {
	in, _, err := OpenVerified(src, info)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := in.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		closeErr := out.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	var written int64
	buf := make([]byte, copyBufSize)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > CopyMaxBytes {
				return ErrCopyTooLarge
			}
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// MovePath moves src to dst. os.Rename does the whole job whenever both names are on one
// filesystem; otherwise it copies and deletes, under the same limits as any other copy.
func MovePath(src, dst string) error {
	srcClean, dstClean := filepath.Clean(src), filepath.Clean(dst)
	if err := guardCopyTarget(srcClean, dstClean); err != nil {
		return err
	}
	if _, err := os.Lstat(srcClean); err != nil {
		return err
	}
	renameErr := os.Rename(srcClean, dstClean)
	if renameErr == nil {
		return nil
	}
	if !errors.Is(renameErr, syscall.EXDEV) {
		return renameErr
	}
	if err := CopyTree(srcClean, dstClean); err != nil {
		return err
	}
	return os.RemoveAll(srcClean)
}
