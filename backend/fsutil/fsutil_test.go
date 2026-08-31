package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInside(t *testing.T) {
	cases := []struct {
		p     string
		roots []string
		want  bool
	}{
		{"/work/proj", []string{"/work/proj"}, true},
		{"/work/proj/a/b.go", []string{"/work/proj"}, true},
		// A sibling whose name merely starts with the root is NOT inside it.
		{"/work/project2/a.go", []string{"/work/proj"}, false},
		{"/work/proj/a.go", []string{"/other", ""}, false},
		// Optional roots arrive empty; they must never match.
		{"/anything", []string{""}, false},
		{"/work/x", []string{"", "/work"}, true},
	}
	for _, tc := range cases {
		if got := Inside(tc.p, tc.roots); got != tc.want {
			t.Errorf("Inside(%q, %v) = %v, want %v", tc.p, tc.roots, got, tc.want)
		}
	}
}

func TestResolveSymlinks_ExistingFile(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outside, "link-to-secret")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveSymlinks(link)
	if err != nil {
		t.Fatalf("ResolveSymlinks(link): %v", err)
	}
	if got != secret {
		t.Errorf("link resolved to %q, want %q", got, secret)
	}
	// A caller checking Inside(root=outside, resolved) still sees it as inside,
	// but a caller whose root is a sibling directory now has the resolved form
	// needed to reject it.
}

func TestResolveSymlinks_IntermediateEscapeIsVisible(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{proj, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(proj, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveSymlinks(filepath.Join(proj, "link", "new.txt"))
	if err != nil {
		t.Fatalf("ResolveSymlinks through a directory symlink: %v", err)
	}
	if want := filepath.Join(outside, "new.txt"); got != want {
		t.Errorf("missing tail re-joined wrongly: got %q, want %q", got, want)
	}
	if Inside(got, []string{proj}) {
		t.Error("resolved path still reports as inside the project root")
	}
}

func TestResolveSymlinks_MissingTailUnderRealDir(t *testing.T) {
	base := t.TempDir()
	got, err := ResolveSymlinks(filepath.Join(base, "a", "b", "new.txt"))
	if err != nil {
		t.Fatalf("a not-yet-existing write target must resolve, not fail: %v", err)
	}
	if !Inside(got, []string{base}) {
		t.Errorf("resolved target %q no longer inside %q", got, base)
	}
}

func TestResolveSymlinks_RejectsDanglingTail(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	// Target does not exist yet: writing here would CREATE it, outside the root.
	dangling := filepath.Join(proj, "dangling")
	if err := os.Symlink(filepath.Join(base, "escape", "payload"), dangling); err != nil {
		t.Fatal(err)
	}

	if got, err := ResolveSymlinks(dangling); err == nil {
		t.Fatalf("dangling symlink accepted, resolved to %q", got)
	} else if !strings.Contains(err.Error(), "dangling symlink") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveSymlinks_AllMissingFallsBackToLexical(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "gone", "deeper", "file.txt")
	got, err := ResolveSymlinks(deep)
	if err != nil {
		t.Fatalf("everything-missing must still resolve against the nearest ancestor: %v", err)
	}
	if got != filepath.Clean(deep) {
		t.Errorf("got %q, want %q", got, filepath.Clean(deep))
	}
}
