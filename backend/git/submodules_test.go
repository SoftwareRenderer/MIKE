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

// submoduleFetchEnv is the environment a fixture appends so its upstream can be a directory on this machine.
// Production passes none, and TestInitSubmodulesRefusesALocalSubmoduleURL pins what that means.
var submoduleFetchEnv = []string{"GIT_ALLOW_PROTOCOL=http:https:git:ssh:file"}

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

	got, err := PopulateSubmodules(t.Context(), wt, project)
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
	got, err := PopulateSubmodules(t.Context(), wt, top)
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
	got, err := PopulateSubmodules(t.Context(), wt, project)
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
	got, err := PopulateSubmodules(t.Context(), wt, project)
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
	got, err := PopulateSubmodules(t.Context(), wt, top)
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
	got, err := PopulateSubmodules(t.Context(), wt, proj)
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
	_, err := PopulateSubmodules(t.Context(), wt, project)
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
	populated, err := PopulateSubmodules(t.Context(), wt, project)
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
	first, err := PopulateSubmodules(t.Context(), wt, project)
	if err != nil {
		t.Fatalf("PopulateSubmodules: %v", err)
	}
	marker := filepath.Join(wt, "vendor", "lib", "scratch.txt")
	writeFile(t, filepath.Join(wt, "vendor", "lib"), "scratch.txt", "written by the task\n")

	again, err := PopulateSubmodules(t.Context(), wt, project)
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

	got, err := initSubmodules(t.Context(), project, submoduleFetchEnv, nil)
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
	if _, err := PopulateSubmodules(t.Context(), wt, project); err != nil {
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
	if _, err := initSubmodules(t.Context(), project, submoduleFetchEnv, func(l string) {
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
	got, err := initSubmodules(t.Context(), project, submoduleFetchEnv, func(l string) {
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
	if _, err := PopulateSubmodules(t.Context(), wt, project); err == nil {
		t.Error("the task that needs the unfetched submodule must be the thing that stops")
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
	got, err := InitSubmodules(t.Context(), project, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("InitSubmodules: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a submodule whose URL is a local directory was fetched: %v", got)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "was not fetched") {
		t.Errorf("the refusal should be reported, got %v", lines)
	}
	if _, err := os.Stat(filepath.Join(project, "build-aux", "flatpak", "shared-modules", "lib.txt")); err == nil {
		t.Error("the submodule's files landed in the checkout although its URL was a path on this machine")
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
	if _, err := initSubmodules(t.Context(), project, submoduleFetchEnv, func(l string) {
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
	if _, err := PopulateSubmodules(t.Context(), wt, project); err != nil {
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
	if _, err := PopulateSubmodules(t.Context(), wt, elsewhere); err != nil {
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
