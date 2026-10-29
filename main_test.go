package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"mike/backend/git"
)

// TestOpenTraceFileIsOwnerOnly covers L8: the flight recorder is always-on and
// holds ~30s of runtime internals, so both the initial file and the rotated
// replacement must be created 0600.
func TestOpenTraceFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.trace")

	f, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile: %v", err)
	}
	// Rotate exactly like the 30s ticker does: truncate the same path again.
	f.Close()
	f2, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile (rotate): %v", err)
	}
	defer f2.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("trace file mode = %#o, want 0600", perm)
	}
}

// TestStampCommitIdentityPutsTheEnvironmentOnEveryCommit covers an identity set through
// GIT_AUTHOR_* ending up on every commit. A server whose container has no git config file can only
// commit this way.
func TestStampCommitIdentityPutsTheEnvironmentOnEveryCommit(t *testing.T) {
	origName, origEmail := git.AuthorIdentity()
	defer git.SetAuthorIdentity(origName, origEmail)

	stampCommitIdentity(func(string) string { return "" })
	if name, email := git.AuthorIdentity(); name != "" || email != "" {
		t.Errorf("stamped %q <%q>, want an empty environment to stamp nothing", name, email)
	}
	if flags := git.GitAuthorFlags(); flags != nil {
		t.Errorf("GitAuthorFlags()=%v, want nil so a repository's own identity keeps winning", flags)
	}

	env := map[string]string{"GIT_AUTHOR_NAME": "Docker User", "GIT_AUTHOR_EMAIL": "docker@example.com"}
	stampCommitIdentity(func(key string) string { return env[key] })
	want := []string{"-c", "user.name=Docker User", "-c", "user.email=docker@example.com"}
	if flags := git.GitAuthorFlags(); !slices.Equal(flags, want) {
		t.Errorf("GitAuthorFlags()=%v, want %v", flags, want)
	}
}
