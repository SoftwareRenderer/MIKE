package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// walkTree calls visit for path and then, for a directory, for everything below it, under the rules
// both a copy and an archive follow: a symlink is visited as the link and never read through, an
// irregular file (socket, fifo, device) is not visited, and a `.git` directory is not entered. out is
// the entry's name where it lands, joined with outSep — the host's for a copy, a slash for an archive —
// and an error from visit stops the walk.
func walkTree(path, out, outSep string, info fs.FileInfo, visit func(path, out string, info fs.FileInfo) error) error {
	if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	if err := visit(path, out, info); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	children, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.Name() == GitDirName {
			continue
		}
		childInfo, err := child.Info()
		if err != nil {
			return err
		}
		if err := walkTree(filepath.Join(path, child.Name()), out+outSep+child.Name(), outSep, childInfo, visit); err != nil {
			return err
		}
	}
	return nil
}
