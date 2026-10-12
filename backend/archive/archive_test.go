package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"testing"
	"time"
)

// The listing every fixture produces, in the order the archives hold them. Each format's fixture is
// built from this one member list, so one expectation covers four containers.
var fixtureMembers = []struct {
	name, body string
	dir        bool
}{
	{name: "dir", dir: true},
	{name: "dir/hello.txt", body: "hello archive"},
	{name: "readme.md", body: "# readme\n"},
	{name: "bin/tool", body: "\x7fELF\x00\x00\x00\x00not text"},
}

// fixtureStamp is 2023-11-14T22:13:20Z, the moment every fixture member carries.
var fixtureStamp = time.Unix(1700000000, 0).UTC()

const fixtureStampText = "2023-11-14T22:13:20Z"

// tarBz2Fixture is the same four members, bzip2'd. compress/bzip2 reads and the stdlib holds no writer, so
// the bytes are a fixture.
const tarBz2Fixture = "QlpoOTFBWSZTWZH1uYMAANXfgMrQSAH3gAMECMR+Z59AzAgwAMzYJSKPIRp5AgDRpoeo0H6oJVNM1RoaNMmTBMAAEwlNUn6k9TJtQ0NqAaABkFi+eeMI2sJ5mgwwg0QShyCIhrm2MkG1GxA0hWpPMFDDMIGUUHiUfs71HnW1AG1AFIZQmAC23jywgGkM8GS3uCNAUg7EmhAHqCKngiB0FsV2aU4iA2xSw3ssQCE+h5/xaWy4G7BDQofjllIfhEkenHu3OPp7Vg8Zcv4FJ1B1kFDRTVdUCB/F3JFOFCQkfW5gwA=="

func buildTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarMembers(t, tw)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func buildTarGz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	writeTarMembers(t, tw)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func writeTarMembers(t *testing.T, tw *tar.Writer) {
	t.Helper()
	for _, m := range fixtureMembers {
		hdr := &tar.Header{Name: m.name, Mode: 0o644, ModTime: fixtureStamp, Format: tar.FormatGNU}
		if m.dir {
			hdr.Typeflag = tar.TypeDir
			hdr.Name = m.name + "/" // the archive's own spelling of a directory
			hdr.Mode = 0o755
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(m.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", m.name, err)
		}
		if !m.dir {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatalf("write %s: %v", m.name, err)
			}
		}
	}
}

func buildZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range fixtureMembers {
		name := m.name
		if m.dir {
			name += "/"
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fixtureStamp})
		if err != nil {
			t.Fatalf("zip header %s: %v", m.name, err)
		}
		if !m.dir {
			if _, err := fw.Write([]byte(m.body)); err != nil {
				t.Fatalf("zip %s: %v", m.name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func tarBz2Bytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tarBz2Fixture)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return b
}

func testOptions() Options {
	return Options{MaxEntries: 1000, MaxMemberBytes: 1 << 20}
}

// openBytes recognises an archive held in memory, with the room every fixture needs.
func openBytes(t *testing.T, b []byte) *Reader {
	t.Helper()
	return openBytesWith(t, b, testOptions())
}

func openBytesWith(t *testing.T, b []byte, opts Options) *Reader {
	t.Helper()
	r, err := Open(context.Background(), NewSeekable(bytes.NewReader(b), int64(len(b))), opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return r
}

// wantListing is the whole answer the fixtures must produce: four rows in archive order, each with its
// size and its stamp.
func wantListing(t *testing.T, got *Listing, format string) {
	t.Helper()
	if got.Format != format {
		t.Errorf("format = %q, want %q", got.Format, format)
	}
	if got.Truncated {
		t.Errorf("truncated = true for an archive of four members")
	}
	want := []Entry{
		{Name: "dir", Size: 0, ModTime: fixtureStampText, IsDir: true},
		{Name: "dir/hello.txt", Size: 13, ModTime: fixtureStampText},
		{Name: "readme.md", Size: 9, ModTime: fixtureStampText},
		{Name: "bin/tool", Size: 16, ModTime: fixtureStampText},
	}
	if len(got.Entries) != len(want) {
		t.Fatalf("entries = %d (%v), want %d", len(got.Entries), got.Entries, len(want))
	}
	for i, w := range want {
		if got.Entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, got.Entries[i], w)
		}
	}
}

func TestListEachFormat(t *testing.T) {
	for _, tc := range []struct {
		format string
		bytes  func(*testing.T) []byte
	}{
		{FormatTar, buildTar},
		{FormatTarGz, buildTarGz},
		{FormatTarBz2, tarBz2Bytes},
		{FormatZip, buildZip},
	} {
		t.Run(tc.format, func(t *testing.T) {
			listing, err := openBytes(t, tc.bytes(t)).List(context.Background())
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			wantListing(t, listing, tc.format)
		})
	}
}

// The name a person clicked decides which viewer opens, never which bytes are read.
func TestFormatComesFromTheBytesNotTheName(t *testing.T) {
	listing, err := openBytes(t, buildTarGz(t)).List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listing.Format != FormatTarGz {
		t.Errorf("format = %q, want %q", listing.Format, FormatTarGz)
	}
	for name, b := range map[string][]byte{
		"a text file":          []byte("just a sentence, no archive at all"),
		"truncated zip header": []byte("PK\x03"),
		"an empty file":        {},
	} {
		if _, err := Open(context.Background(), NewSeekable(bytes.NewReader(b), int64(len(b))), testOptions()); !errors.Is(err, ErrNotArchive) {
			t.Errorf("%s: err = %v, want ErrNotArchive", name, err)
		}
	}
}

// A gzip of a plain file passes the sniff and fails the walk: still ErrNotArchive, still nothing listed.
func TestCompressedFileThatHoldsNoTarIsRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte("hello"))
	zw.Close()

	r := openBytes(t, buf.Bytes())
	if _, err := r.List(context.Background()); !errors.Is(err, ErrNotArchive) {
		t.Errorf("err = %v, want ErrNotArchive", err)
	}
	if _, err := r.ReadEntry(context.Background(), "hello"); !errors.Is(err, ErrNotArchive) {
		t.Errorf("read entry err = %v, want ErrNotArchive", err)
	}
}

func TestListStopsAtTheEntryCeiling(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < 5; i++ {
		hdr := &tar.Header{
			Name: string(rune('a'+i)) + ".txt", Typeflag: tar.TypeReg,
			Size: 1, ModTime: fixtureStamp, Format: tar.FormatGNU,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("header: %v", err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	opts := testOptions()
	opts.MaxEntries = 2
	listing, err := openBytesWith(t, buf.Bytes(), opts).List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listing.Entries) != 2 || !listing.Truncated {
		t.Errorf("got %d entries, truncated=%v; want 2 and truncated", len(listing.Entries), listing.Truncated)
	}
}

// A listing that runs out of time reports what it read and says it is short; one that read nothing answers
// the deadline, since "this archive is empty" would be a claim about the file.
func TestListStopsAtTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A source that ends the context after its first read stops the walk one entry in: a deadline landing
	// mid-walk rather than before it started.
	body := buildTar(t)
	r := openBytes(t, body)
	r.src = NewSeekable(&cancelAfterReads{at: bytes.NewReader(body), after: 1, cancel: cancel}, int64(len(body)))
	listing, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !listing.Truncated {
		t.Errorf("truncated = false; want a partial listing reported as short")
	}
	if len(listing.Entries) == 0 {
		t.Error("a walk that read entries before the deadline reports them")
	}

	expired, cancelExpired := context.WithCancel(context.Background())
	cancelExpired()
	if _, err := openBytes(t, body).List(expired); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation to reach the caller", err)
	}
}

// cancelAfterReads is a ReaderAt that ends the context once it has served `after` reads.
type cancelAfterReads struct {
	at     io.ReaderAt
	after  int
	reads  int
	cancel context.CancelFunc
}

func (c *cancelAfterReads) ReadAt(p []byte, off int64) (int, error) {
	c.reads++
	if c.reads > c.after {
		c.cancel()
	}
	return c.at.ReadAt(p, off)
}

func TestReadEntryReturnsTheMemberText(t *testing.T) {
	members := map[string]string{"dir/hello.txt": "hello archive", "readme.md": "# readme\n"}
	for _, build := range []struct {
		format string
		bytes  func(*testing.T) []byte
	}{
		{FormatTar, buildTar},
		{FormatTarGz, buildTarGz},
		{FormatTarBz2, tarBz2Bytes},
		{FormatZip, buildZip},
	} {
		r := openBytes(t, build.bytes(t))
		for name, want := range members {
			got, err := r.ReadEntry(context.Background(), name)
			if err != nil {
				t.Fatalf("%s %s: %v", build.format, name, err)
			}
			if string(got) != want {
				t.Errorf("%s %s = %q, want %q", build.format, name, got, want)
			}
		}
	}
}

// A member is matched by exactly the name its row shows. A prefix, a directory of that name, or a "./"
// the archive added are not that member.
func TestReadEntryMatchesOnlyTheWholeName(t *testing.T) {
	r := openBytes(t, buildTarGz(t))
	for _, name := range []string{"dir", "dir/", "./readme.md/", "readme", "readme.md/", "../readme.md", "nope.txt"} {
		if _, err := r.ReadEntry(context.Background(), name); !errors.Is(err, ErrNoEntry) {
			t.Errorf("%q: err = %v, want ErrNoEntry", name, err)
		}
	}
}

func TestReadEntryRefusesWhatAnEditorCannotShow(t *testing.T) {
	body := buildTarGz(t)
	if _, err := openBytes(t, body).ReadEntry(context.Background(), "bin/tool"); !errors.Is(err, ErrBinaryEntry) {
		t.Errorf("binary member: err = %v, want ErrBinaryEntry", err)
	}

	opts := testOptions()
	opts.MaxMemberBytes = 8
	if _, err := openBytesWith(t, body, opts).ReadEntry(context.Background(), "dir/hello.txt"); !errors.Is(err, ErrEntryTooLarge) {
		t.Errorf("oversized member: err = %v, want ErrEntryTooLarge", err)
	}
	opts.MaxMemberBytes = 9
	if _, err := openBytesWith(t, body, opts).ReadEntry(context.Background(), "readme.md"); err != nil {
		t.Errorf("a member of exactly the cap must read: %v", err)
	}
}

// Names are shown and matched in one spelling, so the row a person clicked is the name that reads it.
func TestEntryNamesAreNormalised(t *testing.T) {
	for name, want := range map[string]string{
		"./dir/":         "dir",
		"././readme.md":  "readme.md",
		"/readme.md":     "readme.md",
		"a/./b.txt":      "a/./b.txt",
		"deep/nested/x/": "deep/nested/x",
	} {
		if got := entryName(name); got != want {
			t.Errorf("entryName(%q) = %q, want %q", name, got, want)
		}
	}
}

// A tar or zip may write one name twice, so the listing repeats it and a row cannot be keyed by name. A
// read of that name answers the first member the archive holds.
func TestOneNameTwiceListsBothRowsAndReadsTheFirst(t *testing.T) {
	for _, build := range []struct {
		format string
		bytes  func(*testing.T) []byte
	}{
		{FormatTar, buildTarTwice},
		{FormatTarGz, buildTarGzTwice},
		{FormatZip, buildZipTwice},
	} {
		body := build.bytes(t)
		r := openBytes(t, body)
		listing, err := r.List(context.Background())
		if err != nil {
			t.Fatalf("%s list: %v", build.format, err)
		}
		if len(listing.Entries) != 2 || listing.Entries[0].Name != "a.txt" || listing.Entries[1].Name != "a.txt" {
			t.Fatalf("%s listing = %+v, want two a.txt rows", build.format, listing.Entries)
		}
		if listing.Entries[0].Size != 5 || listing.Entries[1].Size != 6 {
			t.Errorf("%s sizes = %d, %d, want 5 then 6", build.format, listing.Entries[0].Size, listing.Entries[1].Size)
		}
		got, err := r.ReadEntry(context.Background(), "a.txt")
		if err != nil {
			t.Fatalf("%s read: %v", build.format, err)
		}
		if string(got) != "first" {
			t.Errorf("%s read a.txt = %q, want the first member", build.format, got)
		}
	}
}

// The two-name-twice fixtures: tar and zip both accept one name twice.
func writeTwice(t *testing.T, write func(name, body string)) {
	t.Helper()
	write("a.txt", "first")
	write("a.txt", "second")
}

func writeTarMember(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: fixtureStamp,
		Typeflag: tar.TypeReg, Format: tar.FormatGNU}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header %s: %v", name, err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func buildTarTwice(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTwice(t, func(name, body string) {
		writeTarMember(t, tw, name, body)
	})
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func buildTarGzTwice(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	writeTwice(t, func(name, body string) {
		writeTarMember(t, tw, name, body)
	})
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func buildZipTwice(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeTwice(t, func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	})
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
