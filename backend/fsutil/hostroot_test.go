package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jail points a jailed policy at a home directory the test owns and returns it. The widening is a
// constructor argument, so no case here needs or touches the variable.
func jail(t *testing.T, declared ...string) *HostRoot {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return NewHostRoot(false, declared...)
}

// mustWrite makes a file under base and returns its path.
func mustWrite(t *testing.T, base, rel, content string) string {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// assertRefused requires a refusal that names the widening variable: a 403 that does not say
// which knob to turn is a support ticket.
func assertRefused(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected refusal, got success", what)
	}
	if !errors.Is(err, ErrHostRoot) {
		t.Fatalf("%s: expected ErrHostRoot, got %v", what, err)
	}
	if !strings.Contains(err.Error(), AllowRootFSEnv) {
		t.Errorf("%s: refusal must name %s, got %q", what, AllowRootFSEnv, err.Error())
	}
}

func TestHostRootRefusesEverythingOutsideTheRoots(t *testing.T) {
	h := jail(t)
	elsewhere := t.TempDir() // a second temp dir: outside home, outside every root
	mustWrite(t, elsewhere, "secret.txt", "do not read me")

	if _, err := h.Stat(elsewhere); !errors.Is(err, ErrHostRoot) {
		t.Fatalf("Stat(outside) = %v, want ErrHostRoot", err)
	}
	_, err := h.ReadDir(elsewhere)
	assertRefused(t, err, "ReadDir(outside)")
	target := filepath.Join(elsewhere, "created")
	err = h.MkdirAll(target, 0o700)
	assertRefused(t, err, "MkdirAll(outside)")
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused MkdirAll must create nothing; stat(%s) = %v", target, err)
	}
	// The system root is the case the refusal is really about.
	if _, err := h.ReadDir("/etc"); !errors.Is(err, ErrHostRoot) {
		t.Errorf(`ReadDir("/etc") = %v, want ErrHostRoot`, err)
	}
}

func TestHostRootAllowsInsideHome(t *testing.T) {
	h := jail(t)
	home := os.Getenv("HOME")
	mustWrite(t, home, "project/main.go", "package main\n")

	fi, err := h.Stat(filepath.Join(home, "project"))
	if err != nil {
		t.Fatalf("Stat(in-home dir): %v", err)
	}
	if !fi.IsDir() {
		t.Errorf("Stat(in-home dir).IsDir() = false")
	}
	entries, err := h.ReadDir(filepath.Join(home, "project"))
	if err != nil {
		t.Fatalf("ReadDir(in-home dir): %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "main.go" {
		t.Errorf("ReadDir listed %v, want [main.go]", entries)
	}
	if _, err := h.ReadDir(filepath.Join(home, "project", "main.go")); err == nil {
		t.Error("ReadDir of a regular file must fail, not list an empty directory")
	}
	if err := h.MkdirAll(filepath.Join(home, "a", "b", "c"), 0o700); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if fi, err := os.Stat(filepath.Join(home, "a", "b", "c")); err != nil || !fi.IsDir() {
		t.Fatalf("nested directory missing: %v", err)
	}
	// MkdirAll keeps os.MkdirAll's contract for a directory that already exists.
	if err := h.MkdirAll(filepath.Join(home, "a", "b", "c"), 0o700); err != nil {
		t.Errorf("MkdirAll of an existing directory = %v, want nil", err)
	}
}

func TestHostRootDeclaredDirectoriesAreRoots(t *testing.T) {
	declared := t.TempDir()
	h := jail(t, declared)

	if err := h.MkdirAll(filepath.Join(declared, "worktrees", "proj"), 0o700); err != nil {
		t.Fatalf("a declared directory must be usable: %v", err)
	}
	if _, err := h.ReadDir(filepath.Join(declared, "worktrees")); err != nil {
		t.Errorf("ReadDir under a declared directory: %v", err)
	}
	if !h.IsRoot(declared) {
		t.Error("IsRoot(declared) = false; the picker would offer .. above a root")
	}
	if h.IsRoot(filepath.Join(declared, "worktrees")) {
		t.Error("IsRoot must be true only for a root itself")
	}
}

// The extensions directory defaults to the relative "extensions", so a declared directory
// must become absolute — a request always names an absolute path.
func TestHostRootNormalizesARelativeDeclaredDir(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	if err := os.MkdirAll(filepath.Join(repo, "extensions"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := jail(t, "extensions")

	if _, err := h.ReadDir(filepath.Join(repo, "extensions")); err != nil {
		t.Fatalf("relative declared dir not usable: %v", err)
	}
	found := false
	for _, r := range h.Roots() {
		if r == filepath.Clean(repo+"/extensions") {
			found = true
		}
	}
	if !found {
		t.Errorf("Roots() = %v, want the absolute extensions dir", h.Roots())
	}
}

func TestHostRootInputMustBeAbsoluteAndCannotWalkOut(t *testing.T) {
	h := jail(t)
	home := os.Getenv("HOME")

	if _, err := h.Stat("relative/thing"); !errors.Is(err, ErrHostRoot) {
		t.Errorf(`Stat("relative/thing") = %v, want ErrHostRoot`, err)
	}
	if _, err := h.Stat(filepath.Join(home, "a", "..", "..", "..", "etc")); !errors.Is(err, ErrHostRoot) {
		t.Error("a .. walk out of home must be refused")
	}
	// A sibling whose name merely starts with the root is not inside it.
	if _, err := h.Stat(home + "-sibling"); !errors.Is(err, ErrHostRoot) {
		t.Errorf("Stat(%s-sibling) = %v, want ErrHostRoot", home, err)
	}
}

func TestHostRootRefusesASymlinkPointingOutOfTheRoots(t *testing.T) {
	h := jail(t)
	home := os.Getenv("HOME")
	outside := t.TempDir()
	mustWrite(t, outside, "id_rsa", "PRIVATE KEY\n")
	link := filepath.Join(home, "id_rsa")
	if err := os.Symlink(filepath.Join(outside, "id_rsa"), link); err != nil {
		t.Fatal(err)
	}

	_, err := h.Stat(link)
	assertRefused(t, err, "Stat(symlink out of root)")
	_, err = h.ReadDir(link)
	assertRefused(t, err, "ReadDir(symlink out of root)")

	// Listing the directory that HOLDS the link still shows the name — the entry
	// is a directory entry, not a read of the target.
	names, err := h.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	var sawLink bool
	for _, e := range names {
		if e.Name() == "id_rsa" {
			sawLink = true
		}
	}
	if !sawLink {
		t.Error("the listing must still show the link entry itself")
	}
}

// os.Root refuses a final component that is a symlink even when the link stays inside the
// root. The fallback through Contained is what keeps such a directory usable.
func TestHostRootReadsThroughASymlinkThatStaysInside(t *testing.T) {
	h := jail(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, "storage", "go"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, home, "storage/go/notes.txt", "x")
	link := filepath.Join(home, "go")
	if err := os.Symlink(filepath.Join(home, "storage", "go"), link); err != nil {
		t.Fatal(err)
	}

	fi, err := h.Stat(link)
	if err != nil {
		t.Fatalf("Stat(in-root symlink) = %v, want success via the Contained fallback", err)
	}
	if !fi.IsDir() {
		t.Errorf("Stat(in-root symlink).IsDir() = false")
	}
	entries, err := h.ReadDir(link)
	if err != nil {
		t.Fatalf("ReadDir(in-root symlink) = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "notes.txt" {
		t.Errorf("ReadDir through the link listed %v", entries)
	}
}

// The refusal must come from the walk, not from os.Root: only ErrHostRoot becomes a 403, and it
// names the link.
func TestHostRootMkdirNeverTraversesASymlink(t *testing.T) {
	h := jail(t)
	home := os.Getenv("HOME")
	outside := t.TempDir()
	inside := filepath.Join(home, "real")
	absTarget := filepath.Join(outside, "abs-target")
	relTargetDir := filepath.Join(outside, "rel-target")
	for _, dir := range []string{absTarget, relTargetDir, inside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	relTarget, rerr := filepath.Rel(home, relTargetDir)
	if rerr != nil {
		t.Fatal(rerr)
	}

	for _, tc := range []struct{ name, target, created string }{
		{"abs", absTarget, filepath.Join(absTarget, "sub")},
		{"rel", relTarget, filepath.Join(relTargetDir, "sub")},
		{"in", inside, filepath.Join(inside, "sub")},
	} {
		link := filepath.Join(home, "link-"+tc.name)
		if err := os.Symlink(tc.target, link); err != nil {
			t.Fatal(err)
		}
		err := h.MkdirAll(filepath.Join(link, "sub"), 0o700)
		if err == nil {
			t.Errorf("MkdirAll through the %s symlink %q succeeded, want refusal", tc.name, link)
			continue
		}
		if !IsHostRootRefusal(err) {
			t.Errorf("MkdirAll through %q: want a jail refusal, got %v", link, err)
		}
		if !strings.Contains(err.Error(), "link-"+tc.name) {
			t.Errorf("MkdirAll through %q: the refusal must name the link, got %q", link, err)
		}
		if _, err := os.Stat(tc.created); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("MkdirAll through %q created %s — the walk followed the link", link, tc.created)
		}
	}
}

// The other half of the fallback, and why it is not dead code: os.Root refuses an in-root
// symlink on its own, while HostRoot.Stat reads it.
func TestStdlibRootRefusesAnInsideSymlinkAndOursReadsIt(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "storage", "go"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "storage", "go"), filepath.Join(home, "go")); err != nil {
		t.Fatal(err)
	}

	r, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, serr := r.Stat("go")
	if serr == nil || !strings.Contains(serr.Error(), escapeMarker) {
		t.Fatalf(`expected (*os.Root).Stat to refuse the in-root symlink with %q, got %v`, escapeMarker, serr)
	}

	t.Setenv("HOME", home)
	if _, err := NewHostRoot(false).Stat(filepath.Join(home, "go")); err != nil {
		t.Errorf("HostRoot.Stat must read through the in-root symlink, got %v", err)
	}
}

// The widening a policy has is the decision it was constructed with. The rule that only the exact
// value "1" produces that decision is startup's (TestOptInSwitchesAreTheExactValueOne), including the
// values someone types believing they widen it ("true", "yes", "on", " 1").
func TestHostRootWideningIsTheDecisionItWasBuiltWith(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	elsewhere := t.TempDir()

	wide := NewHostRoot(true)
	if !wide.Unrestricted() {
		t.Fatal("a policy built widened is not unrestricted")
	}
	if wide.Jailed() {
		t.Error("Jailed() = true while unrestricted")
	}
	if err := wide.MkdirAll(filepath.Join(elsewhere, "widened"), 0o700); err != nil {
		t.Errorf("MkdirAll outside every root = %v, want success", err)
	}
	if _, err := wide.ReadDir("/etc"); err != nil {
		t.Errorf("ReadDir(/etc) = %v, want success", err)
	}
	if wide.IsRoot("/etc") {
		t.Error("unrestricted mode has no roots, so .. must stay available everywhere")
	}

	jailed := NewHostRoot(false)
	if jailed.Unrestricted() {
		t.Error("a policy built jailed is unrestricted")
	}
	if !jailed.Jailed() {
		t.Error("Jailed() = false while jailed")
	}
	if _, err := jailed.ReadDir(elsewhere); !errors.Is(err, ErrHostRoot) {
		t.Errorf("ReadDir outside the roots = %v, want ErrHostRoot", err)
	}

	// The decision cannot be changed under a policy that is in use. Two calls of one request must be
	// measured against the same jail, and the roots quoted in a refusal must be the roots that
	// refused — which is why this reads an argument and not the environment.
	t.Setenv(AllowRootFSEnv, "1")
	if !jailed.Jailed() || jailed.Unrestricted() {
		t.Error("a policy in use widened itself from the environment after construction")
	}
}

// With no usable home there is nothing to jail TO, so nothing is allowed: the policy fails
// closed rather than falling back to the whole filesystem.
func TestHostRootFailsClosedWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	h := NewHostRoot(false, t.TempDir())

	if len(h.Roots()) != 1 {
		t.Errorf("Roots() = %v, want only the declared dir", h.Roots())
	}
	if _, err := h.ReadDir("/etc"); !errors.Is(err, ErrHostRoot) {
		t.Errorf("ReadDir(/etc) with no home = %v, want ErrHostRoot", err)
	}
	// The declared directory is still usable — the missing home costs only home.
	declared := h.Roots()[0]
	if _, err := h.ReadDir(declared); err != nil {
		t.Errorf("ReadDir(declared root) = %v, want success", err)
	}
	// The widening still works on a policy built with it, which is how the test suites run: the
	// missing home costs only home.
	if _, err := NewHostRoot(true).ReadDir("/etc"); err != nil {
		t.Errorf("widened ReadDir(/etc) = %v, want success", err)
	}
}

// A home of "/" is a root that constrains nothing; the startup warning has to be able to say
// so.
func TestHostRootReportsAUniversalRoot(t *testing.T) {
	t.Setenv("HOME", "/")
	h := NewHostRoot(false)
	if !h.HasUniversalRoot() {
		t.Errorf("HasUniversalRoot() = false for HOME=/, Roots()=%v", h.Roots())
	}
	t.Setenv("HOME", t.TempDir())
	if NewHostRoot(false).HasUniversalRoot() {
		t.Error("HasUniversalRoot() = true for an ordinary home")
	}
}

// A nil policy is valid and fails closed, the way MountPolicy already is.
func TestNilHostRootFailsClosed(t *testing.T) {
	var h *HostRoot
	if !h.Jailed() {
		t.Error("a nil policy must report itself jailed")
	}
	if h.Unrestricted() {
		t.Error("a nil policy must never report itself unrestricted")
	}
	if _, err := h.ReadDir("/etc"); !errors.Is(err, ErrHostRoot) {
		t.Errorf("nil.ReadDir(/etc) = %v, want ErrHostRoot", err)
	}
	if err := h.MkdirAll("/etc/whatever", 0o700); !errors.Is(err, ErrHostRoot) {
		t.Errorf("nil.MkdirAll = %v, want ErrHostRoot", err)
	}
	if h.HasUniversalRoot() || len(h.Roots()) != 0 || h.IsRoot("/") {
		t.Error("a nil policy has no roots and nothing universal about it")
	}
}

// A root that disappears while the server runs must produce an error, not a success that read
// something else.
func TestHostRootMissingRootDirectoryIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gone := filepath.Join(home, "gone")
	if err := os.MkdirAll(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	h := NewHostRoot(false, gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReadDir(filepath.Join(gone, "x")); err == nil {
		t.Error("ReadDir under a vanished root must fail")
	} else if errors.Is(err, ErrHostRoot) {
		t.Errorf("a vanished root is not a containment refusal: %v", err)
	}
}

// The refusal must be recognisable by type as well as by text, and name the path.
func TestHostRootErrShape(t *testing.T) {
	h := jail(t)
	err := h.Err("/somewhere/else")
	if !errors.Is(err, ErrHostRoot) {
		t.Fatalf("Err() = %v, want it to wrap ErrHostRoot", err)
	}
	if !strings.Contains(err.Error(), "/somewhere/else") {
		t.Errorf("Err() must name the refused path, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), AllowRootFSEnv) {
		t.Errorf("Err() must name %s, got %q", AllowRootFSEnv, err.Error())
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		t.Skip("Err() is not a PathError; that is fine")
	}
}
