package sandbox

// The task sandbox is where a granted dependency directory gets filled: it mounts read-write what the
// language server's sandbox mounts read-only. These tests pin the two properties that make that safe —
// no `:U`, and no assignment without its mount.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

func depOpts(dir string, extra ...types.SandboxMount) SandboxOpts {
	return SandboxOpts{DependencyMounts: []types.SandboxMount{
		{HostPath: dir, ContainerPath: "/go/pkg/mod", Mode: "ro"}, // whatever was asked, this one is rw
	}, DependencyEnvs: []types.SandboxEnv{{Key: "GOMODCACHE", Value: "/go/pkg/mod"}}}
}

func TestDependencyCaches_AreReadWriteAndNeverChownTheHost(t *testing.T) {
	dir := t.TempDir()
	specs, envs := dependencyCaches("task-1", "/home/sandbox/MIKE", "/host/MIKE", depOpts(dir))

	if len(specs) != 1 || specs[0] != dir+":/go/pkg/mod:rprivate,rw" {
		t.Fatalf("dependencyCaches() = %v, want one rprivate,rw bind of the granted directory", specs)
	}
	// `:U` is not a sharper mode. It chowns the *host* source recursively into the container's mapped uid:
	// measured on a scratch directory, `stat` afterwards reported 525287 and the account owner could no
	// longer delete their own files.
	for _, spec := range specs {
		mode := spec[strings.LastIndex(spec, ":")+1:]
		if mode != "rprivate,rw" {
			t.Errorf("mount %q carries the option %q: any option that changes host ownership is refused here", spec, mode)
		}
	}
	if len(envs) != 1 || envs[0].Key != "GOMODCACHE" || envs[0].Value != "/go/pkg/mod" {
		t.Errorf("envs = %v, want GOMODCACHE pointed at the container path", envs)
	}
}

func TestDependencyCaches_NoAssignmentWithoutTheMount(t *testing.T) {
	// The two halves are decided together. GOMODCACHE naming a container path with nothing behind it
	// sends the toolchain to an empty directory, which is a worse answer than the cache its image
	// already contains — and it looks like an empty cache rather than like a missing grant.
	gone := filepath.Join(t.TempDir(), "deleted-after-the-grant")
	specs, envs := dependencyCaches("task-1", "/home/sandbox/MIKE", "/host/MIKE", depOpts(gone))
	if len(specs) != 0 {
		t.Errorf("a directory that is not there produced a mount: %v", specs)
	}
	if len(envs) != 0 {
		t.Errorf("the assignment survived without its mount: %v", envs)
	}

	// A file is not a directory, and an entry with a half-filled path is not a mount either. Neither
	// is created on the fly, unlike a project's own mount: a grant that has gone stale stays gone.
	file := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(file, []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if specs, envs := dependencyCaches("task-1", "/home/sandbox/MIKE", "", depOpts(file)); len(specs) != 0 || len(envs) != 0 {
		t.Errorf("a file was mounted as a cache directory: %v %v", specs, envs)
	}
	if specs, _ := dependencyCaches("task-1", "/home/sandbox/MIKE", "", SandboxOpts{DependencyMounts: []types.SandboxMount{
		{HostPath: "", ContainerPath: "/go/pkg/mod"}, {HostPath: t.TempDir(), ContainerPath: ""},
	}}); len(specs) != 0 {
		t.Errorf("an incomplete mount spec reached the container: %v", specs)
	}
}

func TestDependencyCaches_NeverTakesAPathSomebodyElseMounted(t *testing.T) {
	dir := t.TempDir()
	const worktree = "/home/sandbox/MIKE"

	// The project configured this container path itself. Its configuration is the more specific
	// intent — and a project that wants its own GOMODCACHE can set the variable, which wins by
	// precedence anyway.
	opts := depOpts(dir)
	opts.SandboxMounts = []types.SandboxMount{{HostPath: "/tmp/project-cache", ContainerPath: "/go/pkg/mod", Mode: "rw"}}
	if specs, envs := dependencyCaches("task-1", worktree, "/host/MIKE", opts); len(specs) != 0 || len(envs) != 0 {
		t.Errorf("the dependency cache overrode the project's own mount: %v %v", specs, envs)
	}

	// The worktree, the project's .git and the skills directory are the container's own structure.
	// Stealing one of those would not look like a cache problem, it would look like a vanished
	// checkout, so those paths are claimed before the dependency mounts are considered.
	for _, path := range []string{worktree, filepath.Join("/host/MIKE", ".git"), SkillsMount} {
		opts := depOpts(dir)
		opts.DependencyMounts[0].ContainerPath = path
		if specs, _ := dependencyCaches("task-1", worktree, "/host/MIKE", opts); len(specs) != 0 {
			t.Errorf("a dependency cache took the container's own path %s: %v", path, specs)
		}
	}

	// An agent sandbox mount is claimed too, at the path it was declared with.
	opts = depOpts(dir)
	opts.AgentSandbox = &types.AgentSandbox{Mounts: []types.SandboxMount{{HostPath: "/x", ContainerPath: "/go/pkg/mod"}}}
	if specs, _ := dependencyCaches("task-1", worktree, "/host/MIKE", opts); len(specs) != 0 {
		t.Errorf("the dependency cache displaced an agent mount: %v", specs)
	}
}

func TestDependencyCaches_TwoCachesOnePathKeepsOne(t *testing.T) {
	// Two extensions can declare the same directory (a second Go server does). Two -v specs on one
	// container path make podman refuse the whole create, which would read as a broken sandbox.
	first, second := t.TempDir(), t.TempDir()
	opts := SandboxOpts{DependencyMounts: []types.SandboxMount{
		{HostPath: first, ContainerPath: "/go/pkg/mod"},
		{HostPath: second, ContainerPath: "/go/pkg/mod"},
		{HostPath: second, ContainerPath: "/usr/local/cargo"},
	}, DependencyEnvs: []types.SandboxEnv{
		{Key: "GOMODCACHE", Value: "/go/pkg/mod"}, {Key: "CARGO_HOME", Value: "/usr/local/cargo"},
	}}

	specs, envs := dependencyCaches("task-1", "/home/sandbox/MIKE", "", opts)
	if len(specs) != 2 {
		t.Fatalf("want one mount per container path, got %v", specs)
	}
	if specs[0] != first+":/go/pkg/mod:rprivate,rw" {
		t.Errorf("the first claimant should win: %v", specs)
	}
	if len(envs) != 2 {
		t.Errorf("both surviving mounts should keep their assignment, got %v", envs)
	}
}

func TestContainerEnvArgs_DependencyAssignmentsLoseToEverythingElse(t *testing.T) {
	// Lowest priority: the dependency assignment is an install-wide default, while a project that
	// names GOMODCACHE itself, or an agent extension that declares it, has said something more
	// specific. podman takes the last -e, so the default goes first.
	opts := SandboxOpts{
		AgentSandbox: &types.AgentSandbox{Envs: []types.SandboxEnv{{Key: "GOMODCACHE", Value: "/agent/mod"}}},
		SandboxEnvs:  []types.SandboxEnv{{Key: "GOFLAGS", Value: "-mod=mod"}},
	}
	depEnvs := []types.SandboxEnv{{Key: "GOMODCACHE", Value: "/go/pkg/mod"}}

	got := envFlags(t, containerEnvArgs("task-1", opts, depEnvs))
	want := []string{"GOMODCACHE=/go/pkg/mod", "GOMODCACHE=/agent/mod", "GOFLAGS=-mod=mod"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("containerEnvArgs() = %v, want %v", got, want)
	}
}
