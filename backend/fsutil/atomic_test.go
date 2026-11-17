package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileAtomicReplacesTheFileWholesale: the save is a rename of a temporary file, so the bytes
// that land are the bytes asked for (never a prefix of what was there) and the mode is the file's own —
// 0644 only when there was no file to ask. os.WriteFile never changed the mode of a file it overwrote,
// so a save must not quietly strip the +x from a script.
func TestWriteFileAtomicReplacesTheFileWholesale(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(t *testing.T, path string)
		content  string
		wantMode os.FileMode
	}{
		{
			name:     "a new file is 0644 whatever the umask would have made it",
			content:  "package main\n",
			wantMode: 0644,
		},
		{
			name: "an existing file keeps its mode, executable bit included",
			prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("#!/bin/sh\necho old\n"), 0755); err != nil {
					t.Fatal(err)
				}
			},
			content:  "#!/bin/sh\necho new\n",
			wantMode: 0755,
		},
		{
			name: "a shorter save leaves no tail of the old content",
			prepare: func(t *testing.T, path string) {
				if err := WriteFileAtomic(path, []byte("aaaaaaaaaaaaaaaaaaaa")); err != nil {
					t.Fatal(err)
				}
			},
			content:  "bb",
			wantMode: 0644,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "f")
			if tc.prepare != nil {
				tc.prepare(t, path)
			}
			if err := WriteFileAtomic(path, []byte(tc.content)); err != nil {
				t.Fatalf("WriteFileAtomic: %v", err)
			}
			if got := mustRead(t, path); got != tc.content {
				t.Errorf("content = %q, want %q", got, tc.content)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != tc.wantMode {
				t.Errorf("mode = %v, want %v", fi.Mode().Perm(), tc.wantMode)
			}
			assertNoTempLeft(t, dir)
		})
	}
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
