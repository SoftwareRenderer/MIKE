// Package archive reads the contents of a compressed archive: the entries it holds, and the text of one of
// them. It never extracts, so nothing here writes to a filesystem.
//
// The format comes from the bytes, never the filename, so a wrong name gets this package's refusal rather
// than a decompressor's noise. Limits arrive as Options, because the caller owns them.
package archive

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// The containers this package reads. The rest — xz, zstd, 7z, rar — answer ErrNotArchive, since reading
// them would mean a dependency this tree does not carry.
const (
	FormatTar    = "tar"
	FormatTarGz  = "tar.gz"
	FormatTarBz2 = "tar.bz2"
	FormatZip    = "zip"
)

// The refusals. A caller shows these sentences to a person, so they stay in MIKE's own words and carry no
// path or archive text.
var (
	ErrNotArchive    = errors.New("not a readable archive")
	ErrNoEntry       = errors.New("no such file in the archive")
	ErrEntryTooLarge = errors.New("file too large")
	ErrBinaryEntry   = errors.New("not a text file")
)

// Entry is one member as the listing shows it. ModTime is fixed-width UTC text, empty when the archive
// recorded none.
type Entry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mtime"`
	IsDir   bool   `json:"isDir"`
}

// Listing is what one archive holds. Truncated means these rows are not all of them: the entry ceiling was
// reached or the deadline expired mid-walk.
type Listing struct {
	Format    string  `json:"format"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated,omitempty"`
}

// Options carries the caller's limits. They are required rather than defaulted: a zero means "no room".
type Options struct {
	// MaxEntries bounds the rows a listing collects. It bounds memory, not size: gzip packs tar's
	// 512-byte headers well enough that a small file can hold an enormous number of entries.
	MaxEntries int
	// MaxMemberBytes bounds the one member a read returns.
	MaxMemberBytes int64
}

// Source is where the archive's bytes are, reachable at any offset. A zip is indexed from its end, so a
// caller that can only stream has to hold the bytes first; this package does not do that holding.
type Source struct {
	at   io.ReaderAt
	size int64
}

// NewSeekable is an open file or a blob already in memory.
func NewSeekable(at io.ReaderAt, size int64) *Source {
	return &Source{at: at, size: size}
}

// openStream starts one pass over the bytes from the beginning, so passes do not disturb each other. The
// deadline is checked inside the read: skipping a large member is one long read the walk would never see
// between entries.
func (s *Source) openStream(ctx context.Context) io.ReadCloser {
	return &ctxReadCloser{
		ReadCloser: io.NopCloser(io.NewSectionReader(s.at, 0, s.size)),
		ctx:        ctx,
	}
}

// ctxReadCloser makes a deadline real for a read that could otherwise wait forever.
type ctxReadCloser struct {
	io.ReadCloser
	ctx context.Context
}

func (c *ctxReadCloser) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.ReadCloser.Read(p)
}

// Reader reads one archive whose format has been recognised.
type Reader struct {
	format string
	src    *Source
	opts   Options
}

// sniffLen covers what deciding the format needs: the longest magic is tar's, five bytes at offset 257.
const sniffLen = 512

// Open recognises the archive in src.
func Open(ctx context.Context, src *Source, opts Options) (*Reader, error) {
	rc := src.openStream(ctx)
	defer rc.Close()
	buf := make([]byte, sniffLen)
	n, err := io.ReadFull(rc, buf)
	// A file shorter than the sniff is a file to look at, not a read failure.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	format, err := detect(buf[:n])
	if err != nil {
		return nil, err
	}
	return &Reader{format: format, src: src, opts: opts}, nil
}

// detect names the container from the first bytes. A slice too short to hold a magic is not that archive.
func detect(head []byte) (string, error) {
	switch {
	case len(head) >= 3 && head[0] == 0x1f && head[1] == 0x8b && head[2] == 0x08:
		return FormatTarGz, nil
	case len(head) >= 3 && string(head[:3]) == "BZh":
		return FormatTarBz2, nil
	case len(head) >= 4 && (string(head[:4]) == "PK\x03\x04" || string(head[:4]) == "PK\x05\x06"):
		return FormatZip, nil
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return FormatTar, nil
	}
	return "", ErrNotArchive
}

// modTime is an entry's stamp as stored: fixed-width UTC, or nothing when it carries the zero time.
func modTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// entryName is the name a member is shown and matched by: slash-separated, no leading "./" or "/", and no
// trailing "/" on a directory. Listing and lookup both use it, so a row's name is the name that reads it.
func entryName(name string) string {
	return strings.TrimSuffix(requestName(name), "/")
}

// requestName is a requested name, normalised the same way but keeping a trailing "/": a request for
// "readme.md/" names a directory and reaches no file.
func requestName(name string) string {
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	return strings.TrimPrefix(name, "/")
}

// describe is the one place an entry row is built, so a listing and a lookup cannot disagree on a name.
func describe(name string, size int64, mod time.Time, isDir bool) Entry {
	return Entry{Name: entryName(name), Size: size, ModTime: modTime(mod), IsDir: isDir}
}
