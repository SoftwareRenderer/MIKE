package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These fixtures drive a real git with a real local remote, because what these tests ask is whether a task
// ever needs that remote.

var testIdentity = []string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid"}

func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, append(testIdentity, "commit", "-qm", message)...)
}

// upstreamRepo is the repository a submodule points at, a real clone source rather than a stand-in.
func upstreamRepo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, dir, "lib.txt", body)
	runGit(t, dir, "add", "-A")
	commitAll(t, dir, "lib v1")
	return dir
}

// superRepo makes a checkout that names upstream at vendor/lib and has populated it the way a person would.
// The file transport is allowed here and only here, because git refuses `file:` for a submodule operation
// unless told otherwise.
func superRepo(t *testing.T, upstream string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "-A")
	commitAll(t, dir, "init")
	runGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", upstream, "vendor/lib")
	commitAll(t, dir, "add submodule")
	runGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "-q")
	return dir
}

// taskWorktree is the shape CreateWorktree leaves: a linked worktree whose submodule path is an empty
// directory.
func taskWorktree(t *testing.T, project, name string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), name)
	runGit(t, project, "worktree", "add", "--detach", wt, "HEAD")
	return wt
}

// TestValidateSubmodulePath: a gitlink path becomes a filesystem path and a container mount point.
func TestValidateSubmodulePath(t *testing.T) {
	for _, bad := range []string{"", "/abs", "../escape", "a/../b", "./now", "a//b", "a/./b", ".git",
		"vendor/.git/lib", "a\x00b", `back\slash`, ".mike-tmp-claimed"} {
		if err := validateSubmodulePath(bad); err == nil {
			t.Errorf("validateSubmodulePath(%q) accepted it", bad)
		}
	}
	for _, good := range []string{"lib", "vendor/lib", ".hidden/lib", "a/b/c"} {
		if err := validateSubmodulePath(good); err != nil {
			t.Errorf("validateSubmodulePath(%q) = %v", good, err)
		}
	}
}

// TestPopulateSubmodulesNeedsNoUpstream: the upstream is deleted first, so a clone or a fetch would fail
// rather than pass quietly.
func TestPopulateSubmodulesNeedsNoUpstream(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := superRepo(t, upstream)
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatal(err)
	}

	wt := taskWorktree(t, project, "task-1")
	if entries, err := os.ReadDir(filepath.Join(wt, "vendor", "lib")); err != nil || len(entries) != 0 {
		t.Fatalf("a fresh linked worktree should hold an empty submodule dir, err=%v entries=%v", err, entries)
	}

	got, err := PopulateSubmodules(t.Context(), wt)
	if err != nil {
		t.Fatalf("PopulateSubmodules with no upstream reachable: %v", err)
	}
	if strings.Join(got, ",") != "vendor/lib" {
		t.Errorf("PopulateSubmodules() = %v, want [vendor/lib]", got)
	}
	body, err := os.ReadFile(filepath.Join(wt, "vendor", "lib", "lib.txt"))
	if err != nil || !strings.Contains(string(body), "lib v1") {
		t.Errorf("the submodule working copy is missing or wrong: %q, %v", body, err)
	}
	// The pointer must name the project's own shared store, which is what the container's pin checks.
	gitDir := strings.TrimSpace(runGitOutput(t, filepath.Join(wt, "vendor", "lib"), "rev-parse", "--absolute-git-dir"))
	if store := filepath.Join(project, ".git", "modules", "vendor", "lib"); !strings.HasPrefix(gitDir, store) {
		t.Errorf("the submodule's gitdir is %q, want it under the project's store %s", gitDir, store)
	}
	if out := runGitOutput(t, wt, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Errorf("populating left the superproject dirty: %q", out)
	}
}

// TestPopulateSubmodulesWalksNestedStores: a nested store at <gitDir>/modules/<outer>/modules/<inner> is
// populated too, parents first.
func TestPopulateSubmodulesWalksNestedStores(t *testing.T) {
	t.Parallel()
	deep := upstreamRepo(t, "deep\n")
	mid := superRepo(t, deep)
	top := superRepo(t, mid)
	for _, gone := range []string{deep, mid} {
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
	}

	wt := taskWorktree(t, top, "task-nested")
	got, err := PopulateSubmodules(t.Context(), wt)
	if err != nil {
		t.Fatalf("PopulateSubmodules with no upstream reachable: %v", err)
	}
	if strings.Join(got, ",") != "vendor/lib,vendor/lib/vendor/lib" {
		t.Errorf("PopulateSubmodules() = %v, want the outer submodule then its own", got)
	}
	inner := filepath.Join(wt, "vendor", "lib", "vendor", "lib", "lib.txt")
	if body, err := os.ReadFile(inner); err != nil || !strings.Contains(string(body), "deep") {
		t.Errorf("the nested submodule is missing: %q, %v", body, err)
	}
}

// TestPopulateSubmodulesReportsAnUnpopulatedStore: an unpopulated store stops the task and names the
// submodule, rather than fetching the `.gitmodules` URL or handing over an empty directory.
func TestPopulateSubmodulesReportsAnUnpopulatedStore(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	if err := os.RemoveAll(filepath.Join(project, ".git", "modules", "vendor", "lib")); err != nil {
		t.Fatal(err)
	}

	wt := taskWorktree(t, project, "task-nostore")
	got, err := PopulateSubmodules(t.Context(), wt)
	if err == nil {
		t.Fatalf("an unpopulated store must stop the task, got %v", got)
	}
	if !strings.Contains(err.Error(), "vendor/lib") || !strings.Contains(err.Error(), "no local copy") {
		t.Errorf("the failure should name the submodule and say the local copy is missing: %v", err)
	}
}

// TestSubmodulePathsMatchesWhatWasPopulated: the pins a container derives must agree with what populate
// handed out.
func TestSubmodulePathsMatchesWhatWasPopulated(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-2")
	populated, err := PopulateSubmodules(t.Context(), wt)
	if err != nil {
		t.Fatalf("PopulateSubmodules: %v", err)
	}

	derived, err := SubmodulePaths(wt)
	if err != nil {
		t.Fatalf("SubmodulePaths: %v", err)
	}
	if strings.Join(derived, ",") != strings.Join(populated, ",") {
		t.Errorf("SubmodulePaths() = %v, want the paths PopulateSubmodules() returned (%v)", derived, populated)
	}

	// A worktree with no submodules is the ordinary case.
	plain := t.TempDir()
	runGit(t, plain, "init", "-q")
	writeFile(t, plain, "a.txt", "a\n")
	runGit(t, plain, "add", "-A")
	commitAll(t, plain, "one")
	plainWT := taskWorktree(t, plain, "task-3")
	if got, err := SubmodulePaths(plainWT); err != nil || len(got) != 0 {
		t.Errorf("SubmodulePaths() = %v, %v for a repository with no gitlinks", got, err)
	}
}

// TestPopulateSubmodulesIsIdempotent: a second call must not touch a checkout a running container may have
// mounted.
func TestPopulateSubmodulesIsIdempotent(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-4")
	first, err := PopulateSubmodules(t.Context(), wt)
	if err != nil {
		t.Fatalf("PopulateSubmodules: %v", err)
	}
	marker := filepath.Join(wt, "vendor", "lib", "scratch.txt")
	writeFile(t, filepath.Join(wt, "vendor", "lib"), "scratch.txt", "written by the task\n")

	again, err := PopulateSubmodules(t.Context(), wt)
	if err != nil {
		t.Fatalf("second PopulateSubmodules: %v", err)
	}
	if strings.Join(again, ",") != strings.Join(first, ",") {
		t.Errorf("second call returned %v, want %v", again, first)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the second call replaced the submodule checkout, taking the task's own file with it: %v", err)
	}
}

// TestPruneSubmoduleWorktreesCleansTheRegistration: the submodule store remembers a task's directory until
// it is pruned, as the project's own registry does.
func TestPruneSubmoduleWorktreesCleansTheRegistration(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-5")
	if _, err := PopulateSubmodules(t.Context(), wt); err != nil {
		t.Fatalf("PopulateSubmodules: %v", err)
	}
	store := filepath.Join(project, ".git", "modules", "vendor", "lib")

	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	runGit(t, project, "worktree", "prune")
	if listed := runGitOutput(t, store, "worktree", "list"); !strings.Contains(listed, wt) {
		t.Fatalf("fixture: the submodule store should still remember %q, got %q", wt, listed)
	}

	PruneSubmoduleWorktrees(project)
	if listed := runGitOutput(t, store, "worktree", "list"); strings.Contains(listed, wt) {
		t.Errorf("the submodule store still remembers a directory that is gone: %q", listed)
	}
	// The person's own checkout is never a leftover.
	if sha := strings.TrimSpace(runGitOutput(t, store, "rev-parse", "--verify", "HEAD")); sha == "" {
		t.Errorf("pruning left the store without a HEAD: %q", sha)
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "lib", "lib.txt")); err != nil {
		t.Errorf("pruning took the person's own submodule checkout: %v", err)
	}
}

// TestPopulateSubmodulesFetchesAStaleCommit: the pointer names a commit the store lacks, and the one fetch
// that answers it lands in the store, over the store's own remote.
func TestPopulateSubmodulesFetchesAStaleCommit(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := superRepo(t, upstream)

	// A second checkout populated while the submodule is still at v1, and cloned before it advances, so its
	// store holds v1 and nothing newer.
	elsewhere := filepath.Join(t.TempDir(), "clone")
	runGit(t, filepath.Dir(elsewhere), "-c", "protocol.file.allow=always", "clone", "-q", project, "clone")
	runGit(t, elsewhere, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q")

	// The submodule advances, and the first checkout bumps the pointer.
	writeFile(t, upstream, "lib.txt", "lib v2\n")
	commitAll(t, upstream, "lib v2")
	runGit(t, filepath.Join(project, "vendor", "lib"), "fetch", "-q", "origin")
	runGit(t, filepath.Join(project, "vendor", "lib"), "checkout", "-q", "FETCH_HEAD")
	runGit(t, project, "add", "vendor/lib")
	commitAll(t, project, "bump submodule")

	// The second checkout learns of the bump without learning the submodule commit, which needs recursion
	// turned off: `fetch.recurseSubmodules` is on-demand by default.
	runGit(t, elsewhere, "-c", "fetch.recurseSubmodules=no", "pull", "-q", "origin", "HEAD")
	store := filepath.Join(elsewhere, ".git", "modules", "vendor", "lib")
	wanted := strings.TrimSpace(runGitOutput(t, elsewhere, "rev-parse", "HEAD:vendor/lib"))
	if Command(store, "cat-file", "-e", wanted+"^{commit}").Run() == nil {
		t.Fatalf("fixture: %s already holds %s, so this proves nothing", store, wanted)
	}

	wt := taskWorktree(t, elsewhere, "task-6")
	if _, err := PopulateSubmodules(t.Context(), wt); err != nil {
		t.Fatalf("PopulateSubmodules should fetch the missing commit: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(wt, "vendor", "lib", "lib.txt"))
	if err != nil || strings.TrimSpace(string(body)) != "lib v2" {
		t.Errorf("the task got %q, want the commit the pointer names (lib v2), err=%v", body, err)
	}
	// And it is in the store, so the next task on this pointer is local again.
	if Command(store, "cat-file", "-e", wanted+"^{commit}").Run() != nil {
		t.Errorf("the fetch did not land in the shared store at %s, so every task would repeat it", store)
	}
}
