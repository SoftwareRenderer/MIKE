package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// walkTree calls visit for path and then, for a directory, for everything below it. Symlinks are visited as links, irregular files (sockets, FIFOs, devices) are skipped, and .git directories are not entered. An error from visit stops the walk.
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
