package exec

import (
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
// reached for it. The one fetch they let happen is the person's own, run directly the way the card's button
// runs it through the API.

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

// pullTheSubmoduleByHand is the card's button: git's own verb over the project's checkout, which is what the
// API route runs. The `-c` is there because git refuses `file:` for a submodule operation by default.
func pullTheSubmoduleByHand(t *testing.T, project string) {
	t.Helper()
	testgit.Git(t, project, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--", "vendor/lib")
}

// TestPrepareSubmodulesStopsNamingTheMissingCopyAndItsURL: a submodule that appeared after the project was
// added ends the task with the path, the address the repository records and MIKE's reason, and reaches nothing
// while doing it — the approval is the person's click, and neither of the project's switches is what refuses
// this.
func TestPrepareSubmodulesStopsNamingTheMissingCopyAndItsURL(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		t.Run(map[bool]string{true: "isolated", false: "not isolated"}[isolated], func(t *testing.T) {
			project, upstream := unpopulatedSubmoduleProject(t)
			setup := submoduleSetup(t, project)
			setup.project.SandboxNetworkIsolation = isolated

			te := newBareTestExecutor(t)
			err := te.prepareSubmodules(setup)
			var needInit *git.SubmodulesUninitialized
			if !errors.As(err, &needInit) {
				t.Fatalf("prepareSubmodules = %v, want *git.SubmodulesUninitialized", err)
			}
			if len(needInit.Submodules) != 1 || needInit.Submodules[0].Path != "vendor/lib" {
				t.Fatalf("the notice lists %v, want vendor/lib", needInit.Submodules)
			}
			if !strings.Contains(err.Error(), upstream) {
				t.Errorf("the notice should name the address the repository records (%s), got %v", upstream, err)
			}
			if needInit.Submodules[0].Note == "" {
				t.Error("the row carries no reason for the person to read")
			}
			if _, statErr := os.Stat(filepath.Join(project, "vendor", "lib", "lib.txt")); statErr == nil {
				t.Error("the submodule was fetched into the person's checkout without a click")
			}
		})
	}
}

// TestPrepareSubmodulesStartsOnceThePersonPulledTheCopy: the click that runs git for the project is the end of
// the matter — nothing else about the task's own commit asks for another decision.
func TestPrepareSubmodulesStartsOnceThePersonPulledTheCopy(t *testing.T) {
	project, _ := unpopulatedSubmoduleProject(t)
	setup := submoduleSetup(t, project)
	te := newBareTestExecutor(t)

	if err := te.prepareSubmodules(setup); err == nil {
		t.Fatal("prepareSubmodules passed with the submodule never fetched")
	}
	pullTheSubmoduleByHand(t, project)
	if err := te.prepareSubmodules(setup); err != nil {
		t.Fatalf("prepareSubmodules after the person pulled the copy: %v", err)
	}
}

// TestAllowStoreFetch: keeping an already-fetched submodule's store current is what that copy is for, so the
// stale-pointer fetch needs no consent of its own — but isolation refuses it, because it dials the store's
// remote from the server process.
func TestAllowStoreFetch(t *testing.T) {
	if allowStoreFetch(&types.Project{SandboxNetworkIsolation: true}) {
		t.Error("an isolated project was allowed a fetch out of a task")
	}
	if !allowStoreFetch(&types.Project{}) {
		t.Error("a project with no isolation was refused the stale-pointer fetch")
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
