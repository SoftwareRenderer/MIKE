package fsutil

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyBufSize is the buffer every copy in this package moves bytes through, so memory does not grow
// with the size of what is copied.
const copyBufSize = 32 * 1024

// ErrTreeChanged reports an entry that stopped being what its header promised while the archive was
// going out. The response is already streaming, so the caller can only log it.
var ErrTreeChanged = errors.New("the tree changed while it was being archived")

// WriteTarGz streams the named paths as one gzip'd tar archive, each entry named relative to root and
// accepted by walkTree. Nothing is buffered: bytes move through one buffer into w, and ctx is checked
// at every entry.
func WriteTarGz(ctx context.Context, w io.Writer, root string, paths []string) error {
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	buf := make([]byte, copyBufSize)
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		clean := filepath.Clean(p)
		rel, err := filepath.Rel(filepath.Clean(root), clean)
		if err != nil || !RelBelowRoot(rel) {
			return fmt.Errorf("%w: %s", ErrOutside, clean)
		}
		info, err := os.Lstat(clean)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // the listing is a moment old; the rest of the selection still goes out
			}
			return err
		}
		if err := writeTarEntry(ctx, tw, clean, filepath.ToSlash(rel), info, buf); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// writeTarEntry writes one entry and, for a directory, everything below it. name is its
// slash-separated archive name; walking through walkTree is what keeps a socket out of
// tar.FileInfoHeader, which refuses one outright.
func writeTarEntry(ctx context.Context, tw *tar.Writer, path, name string, info fs.FileInfo, buf []byte) error {
	return walkTree(path, name, "/", info, func(p, out string, fi fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return writeTarOne(tw, p, out, fi, buf)
	})
}

// writeTarOne writes one entry walkTree accepted. A file is written at exactly the size its header
// promised, so a file still being written ends the archive with ErrTreeChanged instead of breaking its
// framing.
func writeTarOne(tw *tar.Writer, path, name string, info fs.FileInfo, buf []byte) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		header.Linkname = target
		return tw.WriteHeader(header)

	case info.IsDir():
		header.Name = name + "/" // the archive's own spelling of a directory
		return tw.WriteHeader(header)

	default: // the walk leaves only a regular file here
		// Checked against the walk's lstat: a file swapped into the name afterwards still fills the size
		// this header promises.
		f, _, err := OpenVerified(path, info)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		copied, err := io.CopyBuffer(tw, io.LimitReader(f, header.Size), buf)
		if err != nil {
			return err
		}
		if copied != header.Size {
			return ErrTreeChanged
		}
		return nil
	}
}
