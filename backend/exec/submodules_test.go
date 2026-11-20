package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/git"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// These fixtures drive a real git with a real local remote, because what these tests ask is whether a turn
// reached for it. The fetch itself is stood in for: these tests are about which switches let it run and what
// the task is told when it cannot, and the git package's own tests prove a fetch lands the copy.

// unpopulatedSubmoduleProject is a checkout whose commit records a submodule at vendor/lib and never fetched
// it — the state a project added before its submodule was stays in.
func unpopulatedSubmoduleProject(t *testing.T) (project, upstream string) {
	t.Helper()
	upstream = t.TempDir()
	testgit.Init(t, upstream)
	testgit.WriteFile(t, upstream, "lib.txt", "lib v1\n")
	testgit.Git(t, upstream, "add", "-A")
	testgit.Git(t, upstream, "commit", "-qm", "lib v1")
	sha := testgit.GitOutput(t, upstream, "rev-parse", "HEAD")

	project = t.TempDir()
	testgit.Init(t, project)
	testgit.WriteFile(t, project, "main.go", "package main\n")
	testgit.Git(t, project, "add", "-A")
	testgit.Git(t, project, "commit", "-qm", "init")
	testgit.Git(t, project, "config", "-f", ".gitmodules", "submodule.vendor-lib.path", "vendor/lib")
	testgit.Git(t, project, "config", "-f", ".gitmodules", "submodule.vendor-lib.url", upstream)
	testgit.Git(t, project, "add", ".gitmodules")
	testgit.Git(t, project, "commit", "-qm", "gitmodules")
	testgit.Git(t, project, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/lib")
	testgit.Git(t, project, "commit", "-qm", "record submodule")
	return project, upstream
}

func submoduleSetup(t *testing.T, project string) *taskExecSetup {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "task-1")
	testgit.Git(t, project, "worktree", "add", "--detach", wt, "HEAD")
	return &taskExecSetup{
		task:         &types.Task{ID: "task-1", ProjectID: "p1"},
		project:      &types.Project{ID: "p1", Path: project},
		projectID:    "p1",
		worktreePath: wt,
		ctx:          t.Context(),
	}
}

// TestPrepareSubmodulesRefusesWithoutTheProjectsConsent: neither isolation nor a withheld consent lets a turn
// reach the network, and the task is stopped naming the address it would have used.
func TestPrepareSubmodulesRefusesWithoutTheProjectsConsent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		isolated bool
		auto     bool
	}{
		{"network isolated", true, true},
		{"isolated and unwilling", true, false},
		{"no consent given", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, upstream := unpopulatedSubmoduleProject(t)
			setup := submoduleSetup(t, project)
			setup.project.SandboxNetworkIsolation = tc.isolated
			setup.project.AutoFetchSubmodules = tc.auto

			asked := 0
			te := newTestExecutorWith(t, Deps{})
			te.submoduleFetcher = func(context.Context, string, []string, git.SubmoduleFetch, func(string)) ([]string, error) {
				asked++
				return nil, nil
			}

			err := te.prepareSubmodules(setup)
			var needInit *git.SubmodulesUninitialized
			if !errors.As(err, &needInit) {
				t.Fatalf("prepareSubmodules = %v, want *git.SubmodulesUninitialized", err)
			}
			if asked != 0 {
				t.Errorf("a turn that reaches the network was asked for despite the project's switches")
			}
			if len(needInit.Submodules) != 1 || needInit.Submodules[0].Path != "vendor/lib" {
				t.Fatalf("the notice lists %v, want vendor/lib", needInit.Submodules)
			}
			if !strings.Contains(err.Error(), upstream) {
				t.Errorf("the notice should name the address MIKE would contact (%s), got %v", upstream, err)
			}
			if _, err := os.Stat(filepath.Join(project, "vendor", "lib", "lib.txt")); err == nil {
				t.Error("the submodule was fetched into the person's checkout without consent")
			}
		})
	}
}

// TestPrepareSubmodulesAsksForExactlyTheMissingCopy: with both switches open the turn asks for the paths that
// are missing, and a fetch that brought nothing back still ends at the notice rather than starting on an empty
// directory.
func TestPrepareSubmodulesAsksForExactlyTheMissingCopy(t *testing.T) {
	project, _ := unpopulatedSubmoduleProject(t)
	setup := submoduleSetup(t, project)
	setup.project.AutoFetchSubmodules = true

	var asked []string
	te := newTestExecutorWith(t, Deps{})
	te.submoduleFetcher = func(_ context.Context, _ string, paths []string, _ git.SubmoduleFetch, onStatus func(string)) ([]string, error) {
		asked = paths
		if onStatus != nil {
			onStatus("fetching vendor/lib")
		}
		return nil, nil
	}

	err := te.prepareSubmodules(setup)
	var needInit *git.SubmodulesUninitialized
	if !errors.As(err, &needInit) {
		t.Fatalf("prepareSubmodules = %v, want the notice for a fetch that brought nothing", err)
	}
	if strings.Join(asked, ",") != "vendor/lib" {
		t.Errorf("the fetch was asked for %v, want exactly the missing path", asked)
	}
}

// TestPrepareSubmodulesStartsOnceTheCopyIsThere: the fetch that lands the working copy is the end of the
// matter — and the worktree can then be populated with the upstream gone, which is the point of doing it once.
func TestPrepareSubmodulesStartsOnceTheCopyIsThere(t *testing.T) {
	project, upstream := unpopulatedSubmoduleProject(t)
	setup := submoduleSetup(t, project)
	setup.project.AutoFetchSubmodules = true

	te := newTestExecutorWith(t, Deps{})
	te.submoduleFetcher = func(_ context.Context, path string, _ []string, _ git.SubmoduleFetch, _ func(string)) ([]string, error) {
		testgit.Git(t, path, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--", "vendor/lib")
		return []string{"vendor/lib"}, nil
	}

	if err := te.prepareSubmodules(setup); err != nil {
		t.Fatalf("prepareSubmodules after a fetch that arrived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "vendor", "lib", "lib.txt")); err != nil {
		t.Fatalf("the fetch did not land in the person's checkout: %v", err)
	}
	if err := os.RemoveAll(upstream); err != nil {
		t.Fatal(err)
	}
	got, err := git.PopulateSubmodules(t.Context(), setup.worktreePath, project, git.PopulateOpts{})
	if err != nil {
		t.Fatalf("the task still needs the upstream: %v", err)
	}
	if strings.Join(got, ",") != "vendor/lib" {
		t.Errorf("PopulateSubmodules() = %v, want [vendor/lib]", got)
	}
}

// TestAllowStoreFetch: a stale submodule pointer is answered with a fetch into the person's store under the
// same two switches as the task's own fetch, so a project that granted neither leaves a turn with no way to
// reach the network at all.
func TestAllowStoreFetch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		isolated bool
		auto     bool
		want     bool
	}{
		{"both switches open", false, true, true},
		{"network isolated", true, true, false},
		{"isolated and consenting", true, true, false},
		{"no consent given", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &types.Project{SandboxNetworkIsolation: tc.isolated, AutoFetchSubmodules: tc.auto}
			if got := allowStoreFetch(p); got != tc.want {
				t.Errorf("allowStoreFetch = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSubmoduleFailCode: the card's button is chosen by this code, so an ordinary setup failure must not
// borrow it.
func TestSubmoduleFailCode(t *testing.T) {
	needInit := &git.SubmodulesUninitialized{Project: "/p", Submodules: []git.SubmoduleState{{Path: "vendor/lib"}}}
	if got := submoduleFailCode(needInit); got != "submodules_uninitialized" {
		t.Errorf("submoduleFailCode = %q, want submodules_uninitialized", got)
	}
	if got := submoduleFailCode(errors.New("podman said no")); got != "sandbox_setup_failed" {
		t.Errorf("submoduleFailCode = %q, want sandbox_setup_failed", got)
	}
}
