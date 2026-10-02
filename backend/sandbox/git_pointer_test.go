package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeGitDir makes a project checkout with a git directory whose admin tree holds taskID.
func writeGitDir(t *testing.T, projectPath, taskID string) string {
	t.Helper()
	gitDir := filepath.Join(projectPath, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "worktrees", taskID), 0o755); err != nil {
		t.Fatalf("seed git dir: %v", err)
	}
	return gitDir
}

func writePointer(t *testing.T, worktreePath, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktreePath, ".git"), []byte(content), 0o644); err != nil {
		t.Fatalf("seed pointer: %v", err)
	}
}

// expectedPin is the `-v` pair that pins the worktree's pointer at its in-container place.
func expectedPin(worktreePath, mountPath string) []string {
	return []string{"-v", filepath.Join(worktreePath, ".git") + ":" + mountPath + "/.git:rprivate,ro"}
}

// TestGitPointerMountArgsPinsTaskWorktree is the shape every task container must carry: the
// rw worktree mount is joined by a read-only mount of the pointer sitting inside it.
func TestGitPointerMountArgsPinsTaskWorktree(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	gitDir := writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: "+filepath.Join(gitDir, "worktrees", "t1")+"\n")

	got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project)
	if err != nil {
		t.Fatalf("gitPointerMountArgs: %v", err)
	}
	if want := expectedPin(worktree, MountPath("proj")); !reflect.DeepEqual(got, want) {
		t.Errorf("gitPointerMountArgs() = %q, want %q", got, want)
	}
}

// TestGitPointerMountArgsSkipsProjectContainer states the deliberate half of the rule: a project
// container's worktree is the checkout itself, and the shell around it is a person's own.
func TestGitPointerMountArgsSkipsProjectContainer(t *testing.T) {
	got, err := gitPointerMountArgs(ProjectKey("p1"), t.TempDir(), MountPath("proj"), t.TempDir())
	if err != nil {
		t.Fatalf("gitPointerMountArgs for a project container: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a project container must ask for no pointer pin, got %q", got)
	}
}

// TestGitPointerMountArgsRefusesARepointedPointer: a pointer naming a gitdir inside the worktree is
// how a guest moves the host's git onto configuration it wrote, and that configuration is a program
// git will run.
func TestGitPointerMountArgsRefusesARepointedPointer(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: .git-evil\n")

	if got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project); err == nil {
		t.Fatalf("a repointed pointer must refuse the container, got %q", got)
	}
}

// TestGitPointerMountArgsRefusesAMissingPointer: a task worktree is always a linked worktree, so a
// directory with no pointer is not the shape `Create` was asked to sandbox.
func TestGitPointerMountArgsRefusesAMissingPointer(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	writeGitDir(t, project, "t1")

	if got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project); err == nil {
		t.Fatalf("a task worktree with no git pointer must refuse the container, got %q", got)
	}
}

// TestGitPointerMountArgsPinsWhenProjectHasNoGitDir covers the project that is itself a worktree: its
// gitdir lives in a `.git` file, so there is no admin tree to check the pointer against. The pin still
// applies.
func TestGitPointerMountArgsPinsWhenProjectHasNoGitDir(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".git"), []byte("gitdir: /elsewhere/.git\n"), 0o644); err != nil {
		t.Fatalf("seed project pointer: %v", err)
	}
	writePointer(t, worktree, "gitdir: .git-evil\n")

	got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project)
	if err != nil {
		t.Fatalf("gitPointerMountArgs: %v", err)
	}
	if want := expectedPin(worktree, MountPath("proj")); !reflect.DeepEqual(got, want) {
		t.Errorf("gitPointerMountArgs() = %q, want %q — the pin is what holds when the check cannot", got, want)
	}
}

// taskWorktreeDir is the fixture every task-container test needs, because `Create` refuses a task
// worktree with no git pointer. A bare `t.TempDir()` is not the shape of one.
func taskWorktreeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writePointer(t, dir, "gitdir: /worktrees/admin/task\n")
	return dir
}
