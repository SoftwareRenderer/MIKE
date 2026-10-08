package fsutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

/**
 * These tests read the archive back and check its framing, because that is what a folder's bytes have to
 * survive: the right entries, in the right names, unpackable.
 */

type tarEntry struct {
	typeflag byte
	mode     os.FileMode
	linkname string
	size     int64
	body     string
}

func (e tarEntry) isLink() bool { return e.typeflag == tar.TypeSymlink }

// unpack reads the whole archive into name → entry.
func unpack(t *testing.T, r io.Reader) map[string]tarEntry {
	t.Helper()
	return eachEntry(t, r, true)
}

// unpackSizes reads the whole archive but keeps no body.
func unpackSizes(t *testing.T, r io.Reader) map[string]int64 {
	t.Helper()
	entries := eachEntry(t, r, false)
	sizes := make(map[string]int64, len(entries))
	for name, e := range entries {
		sizes[name] = e.size
	}
	return sizes
}

func eachEntry(t *testing.T, r io.Reader, keepBody bool) map[string]tarEntry {
	t.Helper()
	zr, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(zr)
	entries := map[string]tarEntry{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		// Read every entry's bytes: skipping ahead would hide a header that promised the wrong size.
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read entry %s: %v", header.Name, err)
		}
		if int64(len(body)) != header.Size {
			t.Fatalf("%s: header promises %d bytes, the archive carries %d", header.Name, header.Size, len(body))
		}
		e := tarEntry{
			typeflag: header.Typeflag,
			mode:     os.FileMode(header.Mode).Perm(),
			linkname: header.Linkname,
			size:     int64(len(body)),
		}
		if keepBody {
			e.body = string(body)
		}
		entries[header.Name] = e
	}
}

func names(entries map[string]tarEntry) []string {
	return slices.Sorted(maps.Keys(entries))
}

func entry(t *testing.T, entries map[string]tarEntry, name string) tarEntry {
	t.Helper()
	found, ok := entries[name]
	if !ok {
		t.Fatalf("%s is not in the archive (it holds %v)", name, names(entries))
	}
	return found
}

func TestWriteTarGzArchivesOneFolderUnderItsOwnName(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pkg", "a.go"), "package a")
	writeTestFile(t, filepath.Join(dir, "pkg", "deep", "b.go"), "package b")
	if err := os.Chmod(filepath.Join(dir, "pkg", "deep"), 0o750); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteTarGz(context.Background(), &buf, dir, []string{filepath.Join(dir, "pkg")}); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}

	entries := unpack(t, &buf)
	if got := entry(t, entries, "pkg/a.go").body; got != "package a" {
		t.Fatalf("pkg/a.go body: %q", got)
	}
	if got := entry(t, entries, "pkg/deep/b.go").body; got != "package b" {
		t.Fatalf("pkg/deep/b.go body: %q", got)
	}
	if mode := entry(t, entries, "pkg/deep/").mode; mode != 0o750 {
		t.Fatalf("directory mode: %o, want 750", mode)
	}
}

func TestWriteTarGzArchivesEachSelectedNameAtTheTopLevel(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "first")
	writeTestFile(t, filepath.Join(dir, "src", "main.go"), "package main")

	var buf bytes.Buffer
	err := WriteTarGz(context.Background(), &buf, dir, []string{
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "src"),
	})
	if err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}

	entries := unpack(t, &buf)
	if got := entry(t, entries, "a.txt").body; got != "first" {
		t.Fatalf("a.txt body: %q", got)
	}
	if got := entry(t, entries, "src/main.go").body; got != "package main" {
		t.Fatalf("src/main.go body: %q", got)
	}
}

// The route has no size refusal, so a tree over the copy limits has to arrive whole.
func TestWriteTarGzHasNoSizeLimit(t *testing.T) {
	dir := t.TempDir()
	size := int(CopyMaxBytes) + 1024
	writeTestFile(t, filepath.Join(dir, "big.bin"), strings.Repeat("x", size))

	var buf bytes.Buffer
	if err := WriteTarGz(context.Background(), &buf, dir, []string{filepath.Join(dir, "big.bin")}); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}
	if got := unpackSizes(t, &buf)["big.bin"]; got != int64(size) {
		t.Fatalf("archive carries %d bytes of a %d byte file", got, size)
	}
}

func TestWriteTarGzLeavesEveryGitDirectoryOut(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pkg", "a.go"), "package a")
	writeTestFile(t, filepath.Join(dir, "pkg", ".git", "config"), "[remote]\nurl = https://tok:secret@example.com/r.git\n")
	writeTestFile(t, filepath.Join(dir, "pkg", ".git", "refs", "head"), "ref: refs/heads/main\n")

	var buf bytes.Buffer
	if err := WriteTarGz(context.Background(), &buf, dir, []string{filepath.Join(dir, "pkg")}); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}

	for _, name := range names(unpack(t, &buf)) {
		if strings.Contains(name, GitDirName) {
			t.Fatalf("the archive carries %s", name)
		}
	}
}

// A worktree's git pointer is a file; extracted elsewhere it points git at a directory that is not there.
func TestWriteTarGzLeavesAWorktreesGitPointerOut(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "wt", "main.go"), "package main")
	writeTestFile(t, filepath.Join(dir, "wt", ".git"), "gitdir: /server/data/worktrees/wt/.git\n")

	var buf bytes.Buffer
	if err := WriteTarGz(context.Background(), &buf, dir, []string{filepath.Join(dir, "wt")}); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}
	if _, seen := unpack(t, &buf)["wt/.git"]; seen {
		t.Fatal("the worktree's git pointer went into the archive")
	}
}

// A link out of the tree is archived as the link; reading through it would import outside bytes.
func TestWriteTarGzRecordsALinkRatherThanWhatItNames(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	writeTestFile(t, secret, "host secret")
	writeTestFile(t, filepath.Join(dir, "proj", "keep.txt"), "kept")
	if err := os.Symlink(secret, filepath.Join(dir, "proj", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "proj", "dirlink")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteTarGz(context.Background(), &buf, dir, []string{filepath.Join(dir, "proj")}); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}

	entries := unpack(t, &buf)
	link := entry(t, entries, "proj/link.txt")
	if !link.isLink() {
		t.Fatalf("proj/link.txt arrived as a %q entry holding %q", link.typeflag, link.body)
	}
	if link.linkname != secret || link.body != "" {
		t.Fatalf("proj/link.txt: link=%q body=%q, want the target text and no body", link.linkname, link.body)
	}
	if !entry(t, entries, "proj/dirlink").isLink() {
		t.Fatal("proj/dirlink is not a link entry")
	}
	if got := entry(t, entries, "proj/keep.txt").body; got != "kept" {
		t.Fatalf("proj/keep.txt body: %q", got)
	}
	for name, e := range entries {
		if strings.Contains(e.body, "host secret") {
			t.Fatalf("%s carries the bytes its target names", name)
		}
	}
}

func TestWriteTarGzDropsAPathThatVanishedAndKeepsTheSelection(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "first")
	writeTestFile(t, filepath.Join(dir, "b.txt"), "second")
	gone := filepath.Join(dir, "c.txt")
	writeTestFile(t, gone, "third")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := WriteTarGz(context.Background(), &buf, dir, []string{
		filepath.Join(dir, "a.txt"), gone, filepath.Join(dir, "b.txt"),
	})
	if err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}
	if got := entry(t, unpack(t, &buf), "b.txt").body; got != "second" {
		t.Fatal("the selection after the missing name did not arrive")
	}
}

func TestWriteTarGzRefusesAPathOutsideItsRoot(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	writeTestFile(t, filepath.Join(elsewhere, "private.txt"), "private")

	var buf bytes.Buffer
	err := WriteTarGz(context.Background(), &buf, dir, []string{filepath.Join(elsewhere, "private.txt")})
	if !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
	if buf.Len() > 0 {
		t.Fatal("a refused archive still wrote bytes")
	}
}

// The root itself is not one of the names: an archive whose top level is "." unpacks to nothing useful.
func TestWriteTarGzRefusesItsOwnRoot(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.txt"), "first")

	var buf bytes.Buffer
	if err := WriteTarGz(context.Background(), &buf, dir, []string{dir}); !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
}

func TestWriteTarGzStopsWhenTheContextEnds(t *testing.T) {
	dir := t.TempDir()
	for i := range 40 {
		writeTestFile(t, filepath.Join(dir, "tree", "f"+string(rune('a'+i))+".txt"), "body")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	if err := WriteTarGz(ctx, &buf, dir, []string{filepath.Join(dir, "tree")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a cancelled context", err)
	}
	if buf.Len() > 0 {
		t.Fatal("a download nobody is waiting for still wrote bytes")
	}
}
