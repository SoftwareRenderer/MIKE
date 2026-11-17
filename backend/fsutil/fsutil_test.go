package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInsideLexicalOnly(t *testing.T) {
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
		if got := inside(tc.p, tc.roots); got != tc.want {
			t.Errorf("inside(%q, %v) = %v, want %v", tc.p, tc.roots, got, tc.want)
		}
	}
}

// TestResolveSymlinksResolvesEveryShapeOfPath is the one question Contained asks before it compares:
// what would this path open? An existing link answers its target, and a name that is not there yet — the
// shape of a write target — answers the nearest existing ancestor with the missing tail re-joined, so a
// create is judged where it will land rather than refused for not existing.
func TestResolveSymlinksResolvesEveryShapeOfPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, base string) (path, want string)
	}{
		{
			name: "an existing link resolves to its target",
			setup: func(t *testing.T, base string) (string, string) {
				secret := filepath.Join(base, "secret.txt")
				if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(base, "link-to-secret")
				if err := os.Symlink(secret, link); err != nil {
					t.Fatal(err)
				}
				return link, secret
			},
		},
		{
			name: "a missing tail under a real directory is re-joined onto it",
			setup: func(t *testing.T, base string) (string, string) {
				return filepath.Join(base, "a", "b", "new.txt"), filepath.Join(base, "a", "b", "new.txt")
			},
		},
		{
			name: "a path whose every part is missing falls back to the lexical form",
			setup: func(t *testing.T, base string) (string, string) {
				deep := filepath.Join(base, "gone", "deeper", "file.txt")
				return deep, filepath.Clean(deep)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			path, want := tc.setup(t, base)
			got, err := ResolveSymlinks(path)
			if err != nil {
				t.Fatalf("ResolveSymlinks(%q): %v", path, err)
			}
			if got != want {
				t.Errorf("ResolveSymlinks(%q) = %q, want %q", path, got, want)
			}
			if !inside(got, []string{base}) {
				t.Errorf("resolved target %q no longer inside %q", got, base)
			}
		})
	}
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
	if inside(got, []string{proj}) {
		t.Error("resolved path still reports as inside the project root")
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
