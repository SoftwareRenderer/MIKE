package sandbox

import (
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
	return []string{"-v", mountPair(filepath.Join(worktreePath, ".git"), mountPath+"/.git", "ro")}
}

// TestGitPointerMountArgsPinsTaskWorktree is the shape every task container must carry: the
// rw worktree mount is joined by a read-only mount of the pointer sitting inside it.
func TestGitPointerMountArgsPinsTaskWorktree(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	gitDir := writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: "+filepath.Join(gitDir, "worktrees", "t1")+"\n")

	got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project, "rw", nil)
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
	got, err := gitPointerMountArgs(ProjectKey("p1"), t.TempDir(), MountPath("proj"), t.TempDir(), "rw", nil)
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

	if got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project, "rw", nil); err == nil {
		t.Fatalf("a repointed pointer must refuse the container, got %q", got)
	}
}

// TestGitPointerMountArgsRefusesAMissingPointer: a task worktree is always a linked worktree, so a
// directory with no pointer is not the shape `Create` was asked to sandbox.
func TestGitPointerMountArgsRefusesAMissingPointer(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	writeGitDir(t, project, "t1")

	if got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project, "rw", nil); err == nil {
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

	got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project, "rw", nil)
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

// writeSubmodule makes worktreePath hold a populated submodule at subPath, its gitdir under the project's
// shared store.
func writeSubmodule(t *testing.T, worktreePath, gitDir, subPath string) {
	t.Helper()
	admin := filepath.Join(gitDir, "modules", filepath.FromSlash(subPath), "worktrees", filepath.Base(subPath))
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatalf("seed submodule store: %v", err)
	}
	dir := filepath.Join(worktreePath, filepath.FromSlash(subPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed submodule dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatalf("seed submodule pointer: %v", err)
	}
}

func mountPair(host, container, mode string) string {
	return host + ":" + container + ":rprivate," + mode
}

// TestGitPointerMountArgsPinsSubmoduleAncestorAndPointer: a guest that moves `vendor` can otherwise lay its
// own pointer down at the path git reads next.
func TestGitPointerMountArgsPinsSubmoduleAncestorAndPointer(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	gitDir := writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: "+filepath.Join(gitDir, "worktrees", "t1")+"\n")
	writeSubmodule(t, worktree, gitDir, "vendor/lib")
	mount := MountPath("proj")

	got, err := gitPointerMountArgs("t1", worktree, mount, project, "rw", []string{"vendor/lib"})
	if err != nil {
		t.Fatalf("gitPointerMountArgs: %v", err)
	}
	want := []string{
		"-v", mountPair(filepath.Join(worktree, ".git"), mount+"/.git", "ro"),
		"-v", mountPair(filepath.Join(worktree, "vendor"), path.Join(mount, "vendor"), "rw"),
		"-v", mountPair(filepath.Join(worktree, "vendor", "lib", ".git"), path.Join(mount, "vendor/lib/.git"), "ro"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gitPointerMountArgs() =\n%q\nwant\n%q", got, want)
	}
}

// TestGitPointerMountArgsAncestorIsShallowestFirst: a destination must exist as a directory before another
// is layered inside it.
func TestGitPointerMountArgsAncestorIsShallowestFirst(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	gitDir := writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: "+filepath.Join(gitDir, "worktrees", "t1")+"\n")
	writeSubmodule(t, worktree, gitDir, "vendor/x/lib")
	mount := MountPath("proj")

	got, err := gitPointerMountArgs("t1", worktree, mount, project, "rw", []string{"vendor/x/lib"})
	if err != nil {
		t.Fatalf("gitPointerMountArgs: %v", err)
	}
	var modes []string
	for _, a := range got {
		if strings.Contains(a, "rprivate") {
			modes = append(modes, a)
		}
	}
	if len(modes) != 4 {
		t.Fatalf("want the worktree pointer plus two ancestors plus the submodule pointer, got %q", modes)
	}
	if !strings.HasPrefix(modes[1], filepath.Join(worktree, "vendor")+":") {
		t.Errorf("the first ancestor mount is %q, want `vendor` before `vendor/x`", modes[1])
	}
	if !strings.HasPrefix(modes[2], filepath.Join(worktree, "vendor", "x")+":") {
		t.Errorf("the second ancestor mount is %q, want `vendor/x`", modes[2])
	}
}

// TestGitPointerMountArgsSubmoduleAncestorFollowsWorktreeMode: what is under the worktree mount keeps its
// mode, and only a pointer is read-only on a writable worktree.
func TestGitPointerMountArgsSubmoduleAncestorFollowsWorktreeMode(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	gitDir := writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: "+filepath.Join(gitDir, "worktrees", "t1")+"\n")
	writeSubmodule(t, worktree, gitDir, "vendor/lib")
	mount := MountPath("proj")

	got, err := gitPointerMountArgs("t1", worktree, mount, project, "ro", []string{"vendor/lib"})
	if err != nil {
		t.Fatalf("gitPointerMountArgs: %v", err)
	}
	want := mountPair(filepath.Join(worktree, "vendor"), path.Join(mount, "vendor"), "ro")
	if !slices.Contains(got, want) {
		t.Errorf("plan mode must mount the submodule read-only, got %q", got)
	}
	if slices.Contains(got, mountPair(filepath.Join(worktree, "vendor"), path.Join(mount, "vendor"), "rw")) {
		t.Errorf("a read-only worktree left %q writable", want)
	}
}

// TestGitPointerMountArgsRefusesARepointedSubmodulePointer: the submodule's `.git` is inside the read-write
// mount, so a guest can rewrite it.
func TestGitPointerMountArgsRefusesARepointedSubmodulePointer(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	gitDir := writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: "+filepath.Join(gitDir, "worktrees", "t1")+"\n")
	writeSubmodule(t, worktree, gitDir, "vendor/lib")
	if err := os.WriteFile(filepath.Join(worktree, "vendor", "lib", ".git"), []byte("gitdir: ./vendor/lib/evil\n"), 0o644); err != nil {
		t.Fatalf("repoint: %v", err)
	}

	if got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project, "rw", []string{"vendor/lib"}); err == nil {
		t.Fatalf("a submodule pointer naming a gitdir outside the project must refuse the container, got %q", got)
	}
}

// TestGitPointerMountArgsRefusesAMissingSubmodulePointer: a container cannot be built around a submodule
// whose pointer is not there to pin.
func TestGitPointerMountArgsRefusesAMissingSubmodulePointer(t *testing.T) {
	project, worktree := t.TempDir(), t.TempDir()
	gitDir := writeGitDir(t, project, "t1")
	writePointer(t, worktree, "gitdir: "+filepath.Join(gitDir, "worktrees", "t1")+"\n")
	if err := os.MkdirAll(filepath.Join(worktree, "vendor", "lib"), 0o755); err != nil {
		t.Fatalf("seed empty submodule: %v", err)
	}

	if got, err := gitPointerMountArgs("t1", worktree, MountPath("proj"), project, "rw", []string{"vendor/lib"}); err == nil {
		t.Fatalf("a submodule with no git pointer must refuse the container, got %q", got)
	}
}
