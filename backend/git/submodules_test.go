package git

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mike/pkg/logging"
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

// superRepo makes a checkout that names upstream at vendor/lib and has populated it the way a person would,
// under the section name `git submodule add` itself writes: the path.
func superRepo(t *testing.T, upstream string) string {
	t.Helper()
	return namedSubRepo(t, upstream, "vendor/lib", "vendor/lib")
}

// taskWorktree is the shape CreateWorktree leaves: a linked worktree whose submodule path is an empty
// directory.
func taskWorktree(t *testing.T, project, name string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), name)
	runGit(t, project, "worktree", "add", "--detach", wt, "HEAD")
	return wt
}

// namedSubRepo makes a checkout that records upstream at subPath under a `.gitmodules` section the path does
// not match — which `git submodule add` never writes, because it names the two the same — and populates it the
// way a person would. The `-c` is there because git refuses `file:` for a submodule operation by default.
func namedSubRepo(t *testing.T, upstream, section, subPath string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	writeFile(t, dir, "main.go", "package main\n")
	runGit(t, dir, "add", "-A")
	commitAll(t, dir, "init")
	runGit(t, dir, "config", "-f", ".gitmodules", "submodule."+section+".path", subPath)
	runGit(t, dir, "config", "-f", ".gitmodules", "submodule."+section+".url", upstream)
	runGit(t, dir, "add", ".gitmodules")
	commitAll(t, dir, "gitmodules")
	// The gitlink is recorded by hand and committed on its own: `git add -A` prunes it again, the directory
	// not existing on disk yet.
	runGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+revParse(t, upstream, "HEAD")+","+subPath)
	runGit(t, dir, append(testIdentity, "commit", "-qm", "record submodule")...)
	runGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "-q")
	return dir
}

// unpopulatedSubRepo is a namedSubRepo with the person's copy taken away again: the gitlink and the
// `.gitmodules` entry recorded, and nothing fetched — the state a clone arrives in.
func unpopulatedSubRepo(t *testing.T, upstream, section, subPath string) string {
	t.Helper()
	dir := namedSubRepo(t, upstream, section, subPath)
	if err := os.RemoveAll(filepath.Join(dir, ".git", "modules")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(subPath))); err != nil {
		t.Fatal(err)
	}
	return dir
}

// submoduleLocalFetch is what a fixture whose upstream is a directory on this machine passes: the transport
// opened for that one upstream, and the screen that would otherwise refuse the address. Production passes
// neither — TestInitSubmodulesRefusesALocalSubmoduleURL pins what that means.
var submoduleLocalFetch = SubmoduleFetch{
	screen: func(string) error { return nil },
	widen:  []string{"GIT_ALLOW_PROTOCOL=http:https:git:ssh:file"},
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

	got, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{})
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
	got, err := PopulateSubmodules(t.Context(), wt, top, PopulateOpts{})
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
	got, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{})
	if err == nil {
		t.Fatalf("an unpopulated store must stop the task, got %v", got)
	}
	for _, want := range []string{"vendor/lib", "no local copy", project, "git submodule update --init --recursive vendor/lib"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure should name %q, so the person knows what to run and where: %v", want, err)
		}
	}
}

// TestPopulateSubmodulesFindsAStoreNamedByItsSection: deriving the store from the path reports a populated
// submodule as missing, and the `submodule update --init` that refusal tells the person to run is then a no-op,
// git already considering it populated — so the task fails on every retry.
func TestPopulateSubmodulesFindsAStoreNamedByItsSection(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "shared modules\n")
	project := namedSubRepo(t, upstream, "shared-modules", "build-aux/flatpak/shared-modules")
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(project, ".git", "modules", "shared-modules")
	if _, err := os.Stat(store); err != nil {
		t.Fatalf("fixture: git should have kept the store at %s: %v", store, err)
	}

	wt := taskWorktree(t, project, "task-named")
	got, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{})
	if err != nil {
		t.Fatalf("a populated submodule must not stop a task with its upstream gone: %v", err)
	}
	if strings.Join(got, ",") != "build-aux/flatpak/shared-modules" {
		t.Errorf("PopulateSubmodules() = %v, want the workspace-relative path", got)
	}
	body, err := os.ReadFile(filepath.Join(wt, "build-aux", "flatpak", "shared-modules", "lib.txt"))
	if err != nil || !strings.Contains(string(body), "shared modules") {
		t.Errorf("the submodule working copy is missing or wrong: %q, %v", body, err)
	}
	// The pointer must name the store git actually created, which is what the container pins.
	gitDir := revParse(t, filepath.Join(wt, "build-aux", "flatpak", "shared-modules"), "--absolute-git-dir")
	if !strings.HasPrefix(gitDir, store) {
		t.Errorf("the submodule's gitdir is %q, want it under the store git named, %s", gitDir, store)
	}
}

// TestPopulateSubmodulesFindsNestedStoresByTheirSections: the nested walk reads a parent's `modules` tree the
// same way, so nested section names are no more derived from paths than top-level ones.
func TestPopulateSubmodulesFindsNestedStoresByTheirSections(t *testing.T) {
	t.Parallel()
	inner := upstreamRepo(t, "deep\n")
	outer := namedSubRepo(t, inner, "inner-name", "subdir/inner")
	top := namedSubRepo(t, outer, "outer-name", "build-aux/outer")
	for _, gone := range []string{inner, outer} {
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
	}

	wt := taskWorktree(t, top, "task-nested-named")
	got, err := PopulateSubmodules(t.Context(), wt, top, PopulateOpts{})
	if err != nil {
		t.Fatalf("nested stores must be found by the names git gave them: %v", err)
	}
	if want := "build-aux/outer,build-aux/outer/subdir/inner"; strings.Join(got, ",") != want {
		t.Errorf("PopulateSubmodules() = %v, want %s", got, want)
	}
	if body, err := os.ReadFile(filepath.Join(wt, "build-aux", "outer", "subdir", "inner", "lib.txt")); err != nil || !strings.Contains(string(body), "deep") {
		t.Errorf("the nested submodule is missing: %q, %v", body, err)
	}
}

// TestPopulateSubmodulesFindsTheStoresOfAProjectThatIsAWorktree: a checkout made by `git worktree add`
// populates submodule stores under its own gitdir, not the shared one, so a project added as a worktree of
// someone's clone has its copies where the common directory holds none.
func TestPopulateSubmodulesFindsTheStoresOfAProjectThatIsAWorktree(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	main := superRepo(t, upstream)
	proj := filepath.Join(t.TempDir(), "proj")
	runGit(t, main, "worktree", "add", "--detach", proj, "HEAD", "-q")
	runGit(t, proj, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "-q")
	// The main checkout's own copies, and the upstream, both go: only the project worktree's answer is left.
	if err := os.RemoveAll(filepath.Join(main, ".git", "modules")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatal(err)
	}

	wt := taskWorktree(t, proj, "task-proj-worktree")
	got, err := PopulateSubmodules(t.Context(), wt, proj, PopulateOpts{})
	if err != nil {
		t.Fatalf("a project that is itself a worktree must find the stores it populated: %v", err)
	}
	if strings.Join(got, ",") != "vendor/lib" {
		t.Errorf("PopulateSubmodules() = %v, want [vendor/lib]", got)
	}
	if body, err := os.ReadFile(filepath.Join(wt, "vendor", "lib", "lib.txt")); err != nil || !strings.Contains(string(body), "lib v1") {
		t.Errorf("the submodule working copy is missing or wrong: %q, %v", body, err)
	}
}

// TestPopulateSubmodulesRefusesAStoreWithoutItsWorkingTree: the person kept the shared copy and deleted their
// own checkout of the submodule, so the task is stopped with the sentence that fixes it rather than with git's
// complaint about a directory it could not enter.
func TestPopulateSubmodulesRefusesAStoreWithoutItsWorkingTree(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	if err := os.RemoveAll(filepath.Join(project, "vendor", "lib")); err != nil {
		t.Fatal(err)
	}

	wt := taskWorktree(t, project, "task-noworktree")
	_, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{})
	if err == nil {
		t.Fatal("a store without its working tree must stop the task")
	}
	for _, want := range []string{"vendor/lib", "no working tree", project, "git submodule update --init --recursive vendor/lib"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure should name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "chdir") {
		t.Errorf("git's own complaint reached the person instead of MIKE's sentence: %v", err)
	}
}

// TestSubmodulePathsMatchesWhatWasPopulated: the pins a container derives must agree with what populate
// handed out.
func TestSubmodulePathsMatchesWhatWasPopulated(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-2")
	populated, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{})
	if err != nil {
		t.Fatalf("PopulateSubmodules: %v", err)
	}

	derived, err := SubmodulePaths(wt, project)
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
	if got, err := SubmodulePaths(plainWT, plain); err != nil || len(got) != 0 {
		t.Errorf("SubmodulePaths() = %v, %v for a repository with no gitlinks", got, err)
	}
}

// TestPopulateSubmodulesIsIdempotent: a second call must not touch a checkout a running container may have
// mounted.
func TestPopulateSubmodulesIsIdempotent(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-4")
	first, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{})
	if err != nil {
		t.Fatalf("PopulateSubmodules: %v", err)
	}
	marker := filepath.Join(wt, "vendor", "lib", "scratch.txt")
	writeFile(t, filepath.Join(wt, "vendor", "lib"), "scratch.txt", "written by the task\n")

	again, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{})
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

// TestInitSubmodulesFetchesWhatThePersonNeverPopulated: a freshly imported checkout holds neither the store
// nor the working copy, and the import fetch is what puts both there — under a section name the path does not
// match, which is the shape the person's own `git submodule update` and MIKE's store lookup must agree on.
func TestInitSubmodulesFetchesWhatThePersonNeverPopulated(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "shared modules\n")
	project := unpopulatedSubRepo(t, upstream, "shared-modules", "build-aux/flatpak/shared-modules")

	got, err := InitSubmodules(t.Context(), project, submoduleLocalFetch, nil)
	if err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}
	if strings.Join(got, ",") != "build-aux/flatpak/shared-modules" {
		t.Errorf("InitSubmodules() = %v, want the submodule's path", got)
	}
	body, err := os.ReadFile(filepath.Join(project, "build-aux", "flatpak", "shared-modules", "lib.txt"))
	if err != nil || !strings.Contains(string(body), "shared modules") {
		t.Errorf("the person's checkout never got the submodule: %q, %v", body, err)
	}
	// And the task's worktree is then built out of that copy with no network left to reach.
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatal(err)
	}
	wt := taskWorktree(t, project, "task-imported")
	if _, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{}); err != nil {
		t.Fatalf("a task after the import must need no upstream: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(wt, "build-aux", "flatpak", "shared-modules", "lib.txt")); err != nil || !strings.Contains(string(body), "shared modules") {
		t.Errorf("the task's submodule copy is missing or wrong: %q, %v", body, err)
	}
}

// TestInitSubmodulesLeavesThePersonsCheckoutAlone: `git submodule update` checks the recorded commit out, so
// running it over a submodule the person has committed into detaches their copy off their own work and drops
// their files from the tree. Import fetches what is missing and touches nothing else.
func TestInitSubmodulesLeavesThePersonsCheckoutAlone(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := namedSubRepo(t, upstream, "shared-modules", "build-aux/flatpak/shared-modules")
	checkout := filepath.Join(project, "build-aux", "flatpak", "shared-modules")
	writeFile(t, checkout, "their-feature.c", "their work\n")
	runGit(t, checkout, "add", "-A")
	runGit(t, checkout, append(testIdentity, "commit", "-qm", "their submodule work")...)
	theirHead := revParse(t, checkout, "HEAD")

	var lines []string
	if _, err := InitSubmodules(t.Context(), project, submoduleLocalFetch, func(l string) {
		lines = append(lines, l)
	}); err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}
	if got := revParse(t, checkout, "HEAD"); got != theirHead {
		t.Errorf("import moved the person's submodule checkout from %s to %s", theirHead, got)
	}
	if _, err := os.Stat(filepath.Join(checkout, "their-feature.c")); err != nil {
		t.Errorf("import took the person's committed file out of their tree: %v", err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "left alone") || !strings.Contains(lines[0], "build-aux/flatpak/shared-modules") {
		t.Errorf("the person should hear that their submodule was left alone, got %v", lines)
	}
}

// TestInitSubmodulesImportsDespiteAnUnreachableUpstream: a project whose submodule cannot be fetched is still
// a project, and the report names which one and why. The task that needs it is what stops.
func TestInitSubmodulesImportsDespiteAnUnreachableUpstream(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := unpopulatedSubRepo(t, upstream, "shared-modules", "build-aux/flatpak/shared-modules")
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatal(err)
	}

	var lines []string
	got, err := InitSubmodules(t.Context(), project, submoduleLocalFetch, func(l string) {
		lines = append(lines, l)
	})
	if err != nil {
		t.Fatalf("an unreachable submodule must not fail the import, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("InitSubmodules() = %v for an unreachable upstream, want nothing reported as populated", got)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "was not fetched") || !strings.Contains(lines[0], "build-aux/flatpak/shared-modules") {
		t.Errorf("the report should name the submodule that stayed unfetched, got %v", lines)
	}

	wt := taskWorktree(t, project, "task-unreachable")
	if _, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{}); err == nil {
		t.Error("the task that needs the unfetched submodule must be the thing that stops")
	}
}

// TestSubmoduleReporterRedactsTheAddressItQuotes: git writes the remote it failed to reach into its own
// failure text, and these lines travel to the browser. The address a repository chose may carry a credential.
func TestSubmoduleReporterRedactsTheAddressItQuotes(t *testing.T) {
	t.Parallel()
	logged, restore := logging.TestSink()
	defer restore()

	var got []string
	report := submoduleReporter("/repos/project", func(l string) { got = append(got, l) })
	report("submodule %s was not fetched: %s", "lib/x",
		`fatal: could not read from remote repository "https://user:sekret@example.test/x.git"`)

	const want = "example.test/x.git"
	if len(got) != 1 || !strings.Contains(got[0], want) {
		t.Fatalf("the reader never got the address git failed to reach: %v", got)
	}
	if strings.Contains(got[0], "sekret") || strings.Contains(logged.String(), "sekret") {
		t.Errorf("the credential in the address survived to the reader and the log: %q / %s", got[0], logged.String())
	}
}

// TestInitSubmodulesRefusesALocalSubmoduleURL: a `.gitmodules` entry naming a directory on this machine is a
// read of anywhere the server user can reach, and MIKE's own allowlist refuses it whatever the operator's
// configuration says. Not parallel: it sets the git configuration file.
func TestInitSubmodulesRefusesALocalSubmoduleURL(t *testing.T) {
	upstream := upstreamRepo(t, "lib v1\n")
	project := unpopulatedSubRepo(t, upstream, "shared-modules", "build-aux/flatpak/shared-modules")

	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[protocol \"file\"]\n\tallow = always\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// GIT_CONFIG_GLOBAL is in git's environment allowlist, so this is the operator's own configuration as git
	// reads it.
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	var lines []string
	got, err := InitSubmodules(t.Context(), project, SubmoduleFetch{}, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a submodule whose URL is a local directory was fetched: %v", got)
	}
	// MIKE's own address screen refuses it, so git is never handed the path at all.
	if len(lines) != 1 || !strings.Contains(lines[0], "was refused") {
		t.Errorf("the refusal should be reported before git is asked, got %v", lines)
	}
	if _, err := os.Stat(filepath.Join(project, "build-aux", "flatpak", "shared-modules", "lib.txt")); err == nil {
		t.Error("the submodule's files landed in the checkout although its URL was a path on this machine")
	}
}

// TestInitSubmodulesScreensTheNestedURL: a nested `.gitmodules` arrives inside a clone, so no screen has read
// it — which is why import asks for one level at a time instead of letting git recurse. A nested address MIKE
// would refuse is refused, and its copy never comes down.
func TestInitSubmodulesScreensTheNestedURL(t *testing.T) {
	t.Parallel()
	inner := upstreamRepo(t, "deep\n")
	outer := unpopulatedSubRepo(t, inner, "inner-name", "subdir/inner")
	top := unpopulatedSubRepo(t, outer, "outer-name", "build-aux/outer")

	var screened []string
	fetch := submoduleLocalFetch
	fetch.screen = func(u string) error {
		screened = append(screened, u)
		if u == inner {
			return fmt.Errorf("its host is not allowed")
		}
		return nil
	}
	var reported []string
	if _, err := InitSubmodules(t.Context(), top, fetch, func(l string) { reported = append(reported, l) }); err != nil {
		t.Fatalf("a refused address is reported, not a fault: %v", err)
	}
	joined := strings.Join(reported, "\n")
	if !strings.Contains(joined, "subdir/inner") || !strings.Contains(joined, "was refused") {
		t.Errorf("the nested address was not refused by name; reported:\n%s", joined)
	}
	if !slices.Contains(screened, inner) {
		t.Errorf("the nested URL was never screened; screened %v", screened)
	}
	if _, err := os.Stat(filepath.Join(top, "build-aux", "outer", "subdir", "inner", "lib.txt")); err == nil {
		t.Error("the nested submodule was fetched over the refused address")
	}
	// The level MIKE did screen for still came down: a nested refusal is not a failed import.
	if _, err := os.Stat(filepath.Join(top, "build-aux", "outer", "main.go")); err != nil {
		t.Errorf("the outer submodule never came down: %v", err)
	}
}

// TestInitSubmodulesFetchesOnlyWhatTheCommitNames: `.gitmodules` is the repository's to advertise in, and a
// section no commit records a gitlink for is never fetched.
func TestInitSubmodulesFetchesOnlyWhatTheCommitNames(t *testing.T) {
	t.Parallel()
	advertised := upstreamRepo(t, "never recorded\n")
	project := unpopulatedSubRepo(t, upstreamRepo(t, "lib v1\n"), "shared-modules", "build-aux/flatpak/shared-modules")
	runGit(t, project, "config", "-f", ".gitmodules", "submodule.advertised.path", "vendor/advertised")
	runGit(t, project, "config", "-f", ".gitmodules", "submodule.advertised.url", advertised)

	var lines []string
	if _, err := InitSubmodules(t.Context(), project, submoduleLocalFetch, func(l string) {
		lines = append(lines, l)
	}); err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}
	for _, line := range lines {
		if strings.Contains(line, "vendor/advertised") {
			t.Errorf("a submodule no commit names was fetched or reported: %q", line)
		}
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "advertised")); err == nil {
		t.Error("the advertised submodule's directory was created")
	}
}

// TestPruneSubmoduleWorktreesCleansTheRegistration: the submodule store remembers a task's directory until
// it is pruned, as the project's own registry does.
func TestPruneSubmoduleWorktreesCleansTheRegistration(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-5")
	if _, err := PopulateSubmodules(t.Context(), wt, project, PopulateOpts{}); err != nil {
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
	_, elsewhere, store, wanted := stalePointerCheckout(t, upstream)

	wt := taskWorktree(t, elsewhere, "task-6")
	if _, err := PopulateSubmodules(t.Context(), wt, elsewhere, PopulateOpts{AllowStoreFetch: true, Fetch: submoduleLocalFetch}); err != nil {
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

// TestInitSubmodulesRefusesWhatTheCheckoutRecordsBehindGitmodules: git dials the address the checkout's own
// config records and falls back to `.gitmodules` only without it, so a fetch is judged on both. Screening one
// and dialling the other would have the person approve one address while the server contacts another.
func TestInitSubmodulesRefusesWhatTheCheckoutRecordsBehindGitmodules(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := unpopulatedSubRepo(t, upstream, "vendor/lib", "vendor/lib")
	// The address the checkout recorded the first time it was initialized, never synced since.
	runGit(t, project, "config", "submodule.vendor/lib.url", "http://127.0.0.1:59999/stolen")

	var screened []string
	fetch := submoduleLocalFetch
	fetch.screen = func(u string) error {
		screened = append(screened, u)
		if strings.Contains(u, "127.0.0.1") {
			return fmt.Errorf("its host is this machine")
		}
		return nil
	}
	var reported []string
	if _, err := InitSubmodules(t.Context(), project, fetch, func(l string) { reported = append(reported, l) }); err != nil {
		t.Fatalf("a refused address is an answer, not a fault: %v", err)
	}
	if !strings.Contains(strings.Join(reported, "\n"), "was refused") {
		t.Errorf("nothing was refused; reported:\n%s", strings.Join(reported, "\n"))
	}
	if !strings.Contains(strings.Join(screened, "\n"), "127.0.0.1") {
		t.Errorf("the address git would dial was never screened; screened %v", screened)
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "lib", "lib.txt")); err == nil {
		t.Error("the submodule was fetched into the checkout despite the refused address")
	}
}

// TestSubmoduleStatesNamesADivergingCheckoutAddress: the person decides from the row, so a row whose address
// is not the one git would dial says so instead of showing one address and contacting another.
func TestSubmoduleStatesNamesADivergingCheckoutAddress(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := unpopulatedSubRepo(t, upstream, "vendor/lib", "vendor/lib")
	runGit(t, project, "config", "submodule.vendor/lib.url", "https://mirror.example/lib")

	states, err := CheckoutSubmoduleStates(t.Context(), project, submoduleLocalFetch)
	if err != nil {
		t.Fatal(err)
	}
	row := stateOf(t, states, "vendor/lib")
	if row.Populated {
		t.Fatal("fixture: the copy is populated")
	}
	if !strings.Contains(row.URL, filepath.Base(upstream)) {
		t.Errorf("URL = %q, want the address %s records", row.URL, gitmodulesFile)
	}
	if !strings.Contains(row.Note, "git submodule sync") {
		t.Errorf("Note = %q, want it to name the checkout's different address", row.Note)
	}
}

// TestPopulateSubmodulesRefusesAnUnscreenedStoreRemote: the address this fetch dials lives in the store's own
// config — written by whoever populated the checkout, not by MIKE's screen — so it is screened like every
// other address a submodule names, and refused the same way.
func TestPopulateSubmodulesRefusesAnUnscreenedStoreRemote(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	_, elsewhere, store, _ := stalePointerCheckout(t, upstream)
	runGit(t, store, "config", "remote.origin.url", "http://127.0.0.1:59999/stolen")

	wt := taskWorktree(t, elsewhere, "task-7")
	_, err := PopulateSubmodules(t.Context(), wt, elsewhere, PopulateOpts{AllowStoreFetch: true})
	if err == nil || !strings.Contains(err.Error(), "would not be reached") {
		t.Fatalf("PopulateSubmodules with an unscreenable remote = %v, want the address refusal", err)
	}
}

// stalePointerCheckout is a project whose submodule pointer names a commit one of its checkouts does not
// hold: the first checkout bumps the pointer after the submodule advances, and a clone taken before the bump
// is left with a store that cannot answer it. It returns the project, the clone, the clone's shared store and
// the commit the pointer wants.
func stalePointerCheckout(t *testing.T, upstream string) (project, elsewhere, store, wanted string) {
	t.Helper()
	project = superRepo(t, upstream)

	elsewhere = filepath.Join(t.TempDir(), "clone")
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
	store = filepath.Join(elsewhere, ".git", "modules", "vendor", "lib")
	wanted = strings.TrimSpace(runGitOutput(t, elsewhere, "rev-parse", "HEAD:vendor/lib"))
	if Command(store, "cat-file", "-e", wanted+"^{commit}").Run() == nil {
		t.Fatalf("fixture: %s already holds %s, so this proves nothing", store, wanted)
	}
	return project, elsewhere, store, wanted
}

// --- the state of a checkout, and fetching what appeared after the project was added ---

// stateOf finds one row of a listing by path, and fails the test where it is absent: a listing that lost a
// submodule is exactly the silence a notice must not have.
func stateOf(t *testing.T, states []SubmoduleState, path string) SubmoduleState {
	t.Helper()
	for _, s := range states {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("the listing has no row for %s: %+v", path, states)
	return SubmoduleState{}
}

// TestSubmoduleStatesNamesTheMissingCopyWithItsURL: the notice a person decides from is built from this
// listing, so the path and the address must be there even when nothing can be read. The section is named
// `shared-modules` and its path is not, which is the case a derived lookup loses.
func TestSubmoduleStatesNamesTheMissingCopyWithItsURL(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := unpopulatedSubRepo(t, upstream, "shared-modules", "build-aux/flatpak/shared-modules")
	wt := taskWorktree(t, project, "task-listing")

	states, err := SubmoduleStates(context.Background(), wt, project, SubmoduleFetch{})
	if err != nil {
		t.Fatalf("SubmoduleStates: %v", err)
	}
	row := stateOf(t, states, "build-aux/flatpak/shared-modules")
	if row.Populated {
		t.Errorf("an unpopulated submodule is listed as populated: %+v", row)
	}
	if row.URL != upstream {
		t.Errorf("URL = %q, want the address git records for that path (%s)", row.URL, upstream)
	}
	if !strings.Contains(row.Note, "path on this machine") {
		t.Errorf("Note = %q, want the address refusal a fetch would meet", row.Note)
	}
}

// TestSubmoduleStatesListsAPopulatedCopy: the happy path is listed as it is, and without an address — a
// populated store costs no `.gitmodules` read, because nothing about it needs a decision.
func TestSubmoduleStatesListsAPopulatedCopy(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-listing-ok")

	states, err := SubmoduleStates(context.Background(), wt, project, SubmoduleFetch{})
	if err != nil {
		t.Fatalf("SubmoduleStates: %v", err)
	}
	row := stateOf(t, states, "vendor/lib")
	if !row.Populated || row.Note != "" {
		t.Errorf("a populated submodule is listed as %s (%q)", row.Path, row.Note)
	}
	if row.URL != "" {
		t.Errorf("URL = %q for a copy that is already there, want none", row.URL)
	}
}

// TestSubmoduleStatesReadsANestedURLFromItsParentsFile: a nested submodule is recorded in its parent's
// `.gitmodules`, not the project's, and the path the person is shown is the workspace-relative one.
func TestSubmoduleStatesReadsANestedURLFromItsParentsFile(t *testing.T) {
	t.Parallel()
	inner := upstreamRepo(t, "deep\n")
	outer := namedSubRepo(t, inner, "inner-name", "subdir/inner")
	top := namedSubRepo(t, outer, "outer-name", "build-aux/outer")
	if err := os.RemoveAll(filepath.Join(top, ".git", "modules", "outer-name", "modules", "inner-name")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(top, "build-aux", "outer", "subdir", "inner")); err != nil {
		t.Fatal(err)
	}

	states, err := SubmoduleStates(context.Background(), top, top, SubmoduleFetch{})
	if err != nil {
		t.Fatalf("SubmoduleStates: %v", err)
	}
	if out := stateOf(t, states, "build-aux/outer"); !out.Populated {
		t.Fatalf("the outer copy is there and was listed missing: %+v", out)
	}
	row := stateOf(t, states, "build-aux/outer/subdir/inner")
	if row.Populated {
		t.Fatalf("the nested copy is gone and was listed populated: %+v", row)
	}
	if row.URL != inner {
		t.Errorf("URL = %q, want the address its own parent records (%s)", row.URL, inner)
	}
}

// TestSubmoduleStatesReportsAGitlinkWithNoEntry: a gitlink whose `.gitmodules` entry is gone has no address to
// fetch. The row says so, because a notice that showed an empty address would still be clickable.
func TestSubmoduleStatesReportsAGitlinkWithNoEntry(t *testing.T) {
	t.Parallel()
	upstream := upstreamRepo(t, "lib v1\n")
	project := unpopulatedSubRepo(t, upstream, "shared-modules", "build-aux/flatpak/shared-modules")
	runGit(t, project, "config", "-f", ".gitmodules", "--remove-section", "submodule.shared-modules")
	runGit(t, project, "add", ".gitmodules")
	runGit(t, project, append(testIdentity, "commit", "-qm", "drop the entry")...)

	states, err := SubmoduleStates(context.Background(), project, project, SubmoduleFetch{})
	if err != nil {
		t.Fatalf("SubmoduleStates: %v", err)
	}
	row := stateOf(t, states, "build-aux/flatpak/shared-modules")
	if row.Populated || row.URL != "" {
		t.Fatalf("a gitlink no entry names is listed as %s at %q", row.Path, row.URL)
	}
	if !strings.Contains(row.Note, gitmodulesFile) {
		t.Errorf("Note = %q, want it to name the file that has no entry for this path", row.Note)
	}
}

// TestInitSubmodulesBringsTheNestedCopyWithoutRecursing: the button's fetch never asks git to recurse, so a
// level MIKE had not listed when it started would stay missing forever if the loop did not re-list. It does.
func TestInitSubmodulesBringsTheNestedCopyWithoutRecursing(t *testing.T) {
	t.Parallel()
	inner := upstreamRepo(t, "deep\n")
	outer := unpopulatedSubRepo(t, inner, "inner-name", "subdir/inner")
	top := unpopulatedSubRepo(t, outer, "outer-name", "build-aux/outer")

	populated, err := InitSubmodules(t.Context(), top, submoduleLocalFetch, nil)
	if err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}
	if !slices.Contains(populated, "build-aux/outer") {
		t.Errorf("InitSubmodules() = %v, want the asked-for copy among them", populated)
	}
	if _, err := os.Stat(filepath.Join(top, "build-aux", "outer", "subdir", "inner", "lib.txt")); err != nil {
		t.Errorf("the nested copy never came down: %v", err)
	}
}

// TestInitSubmodulesNeverFetchesWhatOnlyTheTaskRecords: a task's worktree commit is one the model writes into,
// so a gitlink that lives only there names an address nobody approved. The person's own commit is the only
// thing that may ask for a fetch.
func TestInitSubmodulesNeverFetchesWhatOnlyTheTaskRecords(t *testing.T) {
	t.Parallel()
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	wt := taskWorktree(t, project, "task-model-added")

	// The model records its own submodule in its own worktree: the entry and the gitlink, committed.
	other := upstreamRepo(t, "someone else's code\n")
	runGit(t, wt, "config", "-f", ".gitmodules", "submodule.sneaky.path", "vendor/sneaky")
	runGit(t, wt, "config", "-f", ".gitmodules", "submodule.sneaky.url", other)
	runGit(t, wt, "update-index", "--add", "--cacheinfo", "160000,"+revParse(t, other, "HEAD")+",vendor/sneaky")
	commitAll(t, wt, "add a submodule nobody asked for")

	got, err := InitSubmodules(t.Context(), project, submoduleLocalFetch, nil)
	if err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}
	if slices.Contains(got, "vendor/sneaky") {
		t.Errorf("a gitlink only the task's commit names was fetched: %v", got)
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "sneaky")); err == nil {
		t.Error("the person's checkout gained a directory the model chose")
	}
	if _, err := os.Stat(filepath.Join(wt, "vendor", "sneaky", "lib.txt")); err == nil {
		t.Error("the model's own worktree was filled from an address nobody approved")
	}
}

// TestInitSubmodulesReportsTheFetchAlreadyRunning: the import and the button both write one checkout's
// `.git/config` and `modules` tree, so a second arrival says so instead of racing it.
func TestInitSubmodulesReportsTheFetchAlreadyRunning(t *testing.T) {
	// Not parallel: the wait it shortens is one the whole package shares.
	project := superRepo(t, upstreamRepo(t, "lib v1\n"))
	restore := submoduleFetchWait
	submoduleFetchWait = 50 * time.Millisecond
	defer func() { submoduleFetchWait = restore }()
	release, err := lockSubmoduleFetch(project)
	if err != nil {
		t.Fatalf("lockSubmoduleFetch: %v", err)
	}
	defer release()

	if _, err := InitSubmodules(t.Context(), project, submoduleLocalFetch, nil); err == nil ||
		!strings.Contains(err.Error(), "already running") {
		t.Errorf("a second fetch of one checkout should be refused, got %v", err)
	}
}

// TestScreenAddress: the addresses a repository-chosen URL must never make the server dial, and the one
// widening that reaches the private ranges a self-hosted server may live in.
func TestScreenAddress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		ip           string
		allowPrivate bool
		wantOK       bool
	}{
		{"loopback v4", "127.0.0.1", true, false},
		{"loopback v6", "::1", true, false},
		{"unspecified", "0.0.0.0", true, false},
		{"metadata", "169.254.169.254", true, false},
		{"link-local", "fe80::1", true, false},
		{"private refused", "10.1.2.3", false, false},
		{"private allowed", "10.1.2.3", true, true},
		{"lan allowed", "192.168.4.5", true, true},
		{"ula refused", "fd00::5", false, false},
		{"public", "93.184.216.34", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := screenAddress(net.ParseIP(tc.ip), tc.ip, tc.allowPrivate)
			if tc.wantOK && err != nil {
				t.Errorf("screenAddress(%s) = %v, want it allowed", tc.ip, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("screenAddress(%s) allowed it", tc.ip)
			}
		})
	}
}

// TestSubmoduleAddressIsWhatGitWouldDial: the URL MIKE screens is the one git would dial, which means a
// relative entry resolved against the remote above it and an scp-style entry read as SSH. A path on this
// machine is refused either way, with the fence or without it.
func TestSubmoduleAddressIsWhatGitWouldDial(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	runGit(t, parent, "init", "-q")
	runGit(t, parent, "remote", "add", "origin", "https://github.com/org/super.git")

	for _, tc := range []struct{ raw, want string }{
		{"https://code.example.com/a/b.git", "https://code.example.com/a/b.git"},
		{"ssh://git@code.example.com/a/b.git", "ssh://git@code.example.com/a/b.git"},
		{"git@code.example.com:a/b.git", "ssh://git@code.example.com/a/b.git"},
		{"git://code.example.com/a/b.git", "git://code.example.com/a/b.git"},
		{"../b.git", "https://github.com/b.git"},
	} {
		address, err := submoduleAddress(parent, tc.raw)
		if err != nil {
			t.Errorf("submoduleAddress(%q) = %v, want %s", tc.raw, err, tc.want)
			continue
		}
		if got := address.String(); got != tc.want {
			t.Errorf("submoduleAddress(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	for _, bad := range []string{"", "-h", "/srv/git/b.git", "file:///srv/git/b.git", "ext::sh -c id", "https://", "ssh://", "git@:a/b.git"} {
		if address, err := submoduleAddress(parent, bad); err == nil {
			t.Errorf("submoduleAddress(%q) = %q, want it refused", bad, address)
		}
	}
}
