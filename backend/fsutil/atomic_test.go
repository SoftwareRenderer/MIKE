package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.go")

	if err := WriteFileAtomic(path, []byte("package main\n")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("content = %q", data)
	}
	// 0644 whatever the umask would have made it.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0644 {
		t.Errorf("new file mode = %v, want -rw-r--r--", fi.Mode().Perm())
	}
	assertNoTempLeft(t, dir)
}

func TestWriteFileAtomicKeepsExistingMode(t *testing.T) {
	// os.WriteFile never changed the mode of a file it overwrote, so a save must not
	// quietly strip the +x from a script.
	dir := t.TempDir()
	path := filepath.Join(dir, "build.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho old\n"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(path, []byte("#!/bin/sh\necho new\n")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0755 {
		t.Errorf("mode after save = %v, want -rwxr-xr-x", fi.Mode().Perm())
	}
	if !strings.Contains(mustRead(t, path), "new") {
		t.Error("content was not replaced")
	}
	assertNoTempLeft(t, dir)
}

func TestWriteFileAtomicKeepsOriginalWhenRenameFails(t *testing.T) {
	// A directory at the target fails only the rename — the shape of a late error.
	dir := t.TempDir()
	path := filepath.Join(dir, "occupied")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(path, "keep.txt")
	if err := os.WriteFile(inside, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(path, []byte("must not land")); err == nil {
		t.Fatal("expected the rename to fail")
	}

	// The "original" survives whole.
	if got := mustRead(t, inside); got != "untouched" {
		t.Errorf("contents behind the target changed: %q", got)
	}
	entries := listNames(t, dir)
	if len(entries) != 1 || entries[0] != "occupied" {
		t.Errorf("directory after a failed write = %v, want only the original", entries)
	}
}

func TestWriteFileAtomicRefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(link, []byte("written through a link")); err == nil {
		t.Fatal("expected a symlink target to be refused")
	}
	if got := mustRead(t, outside); got != "original" {
		t.Errorf("the link's target was modified: %q", got)
	}
	assertNoTempLeft(t, dir)
}

func TestWriteFileAtomicOverwritesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grows.go")
	// The old content must be gone, not left as a prefix of a longer file.
	if err := WriteFileAtomic(path, []byte("aaaaaaaaaaaaaaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("bb")); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, path); got != "bb" {
		t.Errorf("content = %q, want %q", got, "bb")
	}
}

func TestIsAtomicTemp(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".mike-tmp-foo.go-1234", true},
		{"/work/proj/.mike-tmp-foo.go-1234", true},
		// The names a user could plausibly have are not ours.
		{"foo.go", false},
		{".mike-tmp", false},
		{"mike-tmp-foo.go-1", false},
		{"foo.mike-tmp.go", false},
		{".mike-tmp-notes.md", true}, // ours: the prefix is the contract
	}
	for _, tc := range cases {
		if got := IsAtomicTemp(tc.path); got != tc.want {
			t.Errorf("IsAtomicTemp(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// The name/filter coupling is proven where it bites:
// backend/stream/atomic_write_watch_test.go runs a real watcher over a real write.

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func assertNoTempLeft(t *testing.T, dir string) {
	t.Helper()
	for _, name := range listNames(t, dir) {
		if IsAtomicTemp(name) {
			t.Errorf("temporary file left behind: %s", name)
		}
	}
}
