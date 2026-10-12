package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"unicode/utf8"
)

// List walks the members in archive order and never re-sorts, so the rows are a property of the file and
// not of the reader's locale. A walk short of the end answers the rows it reached with Truncated set; one
// that read nothing answers the error that stopped it.
func (r *Reader) List(ctx context.Context) (*Listing, error) {
	out := &Listing{Format: r.format, Entries: []Entry{}}
	var err error
	if r.format == FormatZip {
		err = r.listZip(ctx, out)
	} else {
		err = r.listTar(ctx, out)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Reader) listTar(ctx context.Context, out *Listing) error {
	rc := r.src.openStream(ctx)
	defer rc.Close()

	body, err := r.decompressor(rc)
	if err != nil {
		return stoppedEarly(ctx, out, err)
	}
	defer body.Close()

	tr := tar.NewReader(body)
	for {
		if err := ctx.Err(); err != nil {
			return stoppedEarly(ctx, out, err)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return stoppedEarly(ctx, out, ErrNotArchive)
		}
		if len(out.Entries) >= r.opts.MaxEntries {
			// A header has been read with no room for it, so the walk is short, not finished.
			out.Truncated = true
			return nil
		}
		out.Entries = append(out.Entries, describe(hdr.Name, hdr.Size, hdr.ModTime, hdr.Typeflag == tar.TypeDir))
	}
}

// stoppedEarly is what a walk answers when it did not reach the end: the rows it read, or the reason it
// stopped when it read none.
func stoppedEarly(ctx context.Context, out *Listing, cause error) error {
	if len(out.Entries) > 0 {
		out.Truncated = true
		return nil
	}
	return ctxOr(ctx, cause)
}

// ctxOr is the same rule with no partial answer to fall back on: the deadline is the answer when it is
// why nothing could be read.
func ctxOr(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return cause
}

// decompressor unwraps the container back to a tar stream. The caller closes what this returns: a gzip
// reader comes out of a pool.
func (r *Reader) decompressor(rc io.ReadCloser) (io.ReadCloser, error) {
	switch r.format {
	case FormatTarGz:
		zr, err := gzip.NewReader(rc)
		if err != nil {
			return nil, ErrNotArchive
		}
		return zr, nil
	case FormatTarBz2:
		return io.NopCloser(bzip2.NewReader(rc)), nil
	default:
		return rc, nil
	}
}

func (r *Reader) listZip(ctx context.Context, out *Listing) error {
	zr, err := zip.NewReader(r.src.at, r.src.size)
	if err != nil {
		return ErrNotArchive
	}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return stoppedEarly(ctx, out, err)
		}
		if len(out.Entries) >= r.opts.MaxEntries {
			out.Truncated = true
			return nil
		}
		out.Entries = append(out.Entries, describe(f.Name, int64(f.UncompressedSize64), f.Modified, f.FileInfo().IsDir()))
	}
	return nil
}

// ReadEntry returns the text of one member, matched by exactly the normalised name its row shows, so no
// prefix or separator trick reaches a different member. An archive may hold one name twice; the first
// member written is the one returned.
func (r *Reader) ReadEntry(ctx context.Context, name string) ([]byte, error) {
	if r.format == FormatZip {
		return r.readZipEntry(ctx, name)
	}
	return r.readTarEntry(ctx, name)
}

func (r *Reader) readTarEntry(ctx context.Context, name string) ([]byte, error) {
	rc := r.src.openStream(ctx)
	defer rc.Close()

	want := requestName(name)
	body, err := r.decompressor(rc)
	if err != nil {
		return nil, ctxOr(ctx, err)
	}
	defer body.Close()

	tr := tar.NewReader(body)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, ErrNoEntry
		}
		if err != nil {
			return nil, ctxOr(ctx, ErrNotArchive)
		}
		if hdr.Typeflag == tar.TypeDir || entryName(hdr.Name) != want {
			continue
		}
		return readMember(tr, r.opts.MaxMemberBytes)
	}
}

func (r *Reader) readZipEntry(ctx context.Context, name string) ([]byte, error) {
	zr, err := zip.NewReader(r.src.at, r.src.size)
	if err != nil {
		return nil, ErrNotArchive
	}
	want := requestName(name)
	for _, f := range zr.File {
		if entryName(f.Name) != want || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return readMember(&ctxReadCloser{ReadCloser: rc, ctx: ctx}, r.opts.MaxMemberBytes)
	}
	return nil, ErrNoEntry
}

// readMember takes one member's bytes, refusing one over the limit rather than holding it. The extra byte
// is what tells "exactly at the cap" from "over it".
func readMember(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrEntryTooLarge
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return nil, ErrBinaryEntry
	}
	return b, nil
}
