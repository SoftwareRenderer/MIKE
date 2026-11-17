package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSetMountRootAimsAndRestores: the injected root is what MountPath builds under and what a caller
// comparing a request's spelling asks, and the function SetMountRoot answers puts both back. A test that
// left the mount aimed at its own temp directory would jail every later request in the process to it.
func TestSetMountRootAimsAndRestores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mount")

	restore := SetMountRoot(root)
	if got := MountRootPath(); got != root {
		t.Errorf("MountRootPath() = %q, want the injected %q", got, root)
	}
	if got, want := MountPath("proj"), filepath.Join(root, "proj"); got != want {
		t.Errorf("MountPath(\"proj\") = %q, want %q", got, want)
	}
	if _, ok := TrimMountPrefix("proj", filepath.Join(root, "proj", "a.go")); !ok {
		t.Error("TrimMountPrefix did not recognise a path under the injected mount")
	}
	restore()

	if got := MountRootPath(); got != defaultMountRoot {
		t.Errorf("after the restore MountRootPath() = %q, want %q", got, defaultMountRoot)
	}
	if _, ok := TrimMountPrefix("proj", filepath.Join(root, "proj", "a.go")); ok {
		t.Error("the injected mount was still recognised as a project mount after the restore")
	}
}

func TestTrimMountPrefix(t *testing.T) {
	const name = "proj"
	tests := []struct {
		path    string
		wantRel string
		wantOK  bool
	}{
		{"/home/sandbox/proj", ".", true},
		{"/home/sandbox/proj/", ".", true},
		{"/home/sandbox/proj/a/b.go", "a/b.go", true},
		{"/home/sandbox/proj/./a", "a", true},
		// A lookalike sibling is not the project's mount.
		{"/home/sandbox/proj2/a", "", false},
		// Neither is a path that leaves the mount via "..".
		{"/home/sandbox/proj/../proj2/a", "", false},
		{"/home/sandbox", "", false},
		{"/etc/passwd", "", false},
	}
	for _, tt := range tests {
		rel, ok := TrimMountPrefix(name, tt.path)
		if ok != tt.wantOK || rel != tt.wantRel {
			t.Errorf("TrimMountPrefix(%q) = (%q, %v), want (%q, %v)", tt.path, rel, ok, tt.wantRel, tt.wantOK)
		}
	}
}

// TestHostContainerRoundTrip: a host path survives the trip through the container
// namespace unchanged, in both directions.
func TestHostContainerRoundTrip(t *testing.T) {
	const name = "demo"
	worktree := filepath.Join(string(filepath.Separator), "srv", "worktrees", "demo", "task-1")
	for _, p := range []string{worktree, worktree + "/main.go", worktree + "/a/b/c.go"} {
		container, ok := HostToContainer(worktree, name, p)
		if !ok {
			t.Fatalf("HostToContainer(%q) refused a path under the worktree", p)
		}
		back, ok := ContainerToHost(worktree, name, container)
		if !ok {
			t.Fatalf("ContainerToHost(%q) not recognised as under the mount", container)
		}
		if want := filepath.Clean(p); back != want {
			t.Errorf("round trip of %q = %q, want %q (container %q)", p, back, want, container)
		}
	}

	// The worktree root maps to the mount root.
	if got, ok := HostToContainer(worktree, name, worktree); !ok || got != MountPath(name) {
		t.Errorf("HostToContainer(worktree root) = (%q, %v), want (%q, true)", got, ok, MountPath(name))
	}
	// A host path from another tree names no file in the mount, so it produces no container path at all —
	// never a ".."-stepping join or a fallback to the mount root, which name a directory nobody asked for.
	for _, p := range []string{
		"/etc/passwd",
		"/srv/worktrees/demo/task-2/main.go", // a sibling tree: Rel succeeds and steps out
		"/srv/elsewhere/main.go",
	} {
		if got, ok := HostToContainer(worktree, name, p); ok {
			t.Errorf("HostToContainer(%q) = %q; a path outside the worktree must yield no container path", p, got)
		}
	}
	// A host path outside the mount does not translate, so the caller keeps what
	// it already has.
	if _, ok := ContainerToHost(worktree, name, "/etc/passwd"); ok {
		t.Error("ContainerToHost accepted a path outside the project mount")
	}
}

// TestContainerToHostCraftedProjectName: a legacy crafted name cannot carry a
// translation out of the worktree, because the mount is the same cleaned
// MountPath the container was created with. A lookalike under the mount root
// translates to nothing.
func TestContainerToHostCraftedProjectName(t *testing.T) {
	worktree := filepath.Join(string(filepath.Separator), "srv", "wt")
	const name = "a/../../b" // MountPath → /home/b

	// A lookalike under the mount root is not this project's mount.
	if got, ok := ContainerToHost(worktree, name, "/home/sandbox/b/x"); ok {
		t.Errorf("translated a path outside the mount (%q) into %q", "/home/sandbox/b/x", got)
	}

	// Every spelling of the project's own mount — including one that reaches it
	// through ".." — resolves to its cleaned form, and lands inside the worktree.
	for _, p := range []string{"/home/b/x", "/home/sandbox/a/../../b/x", "/home/b/../b/x"} {
		got, ok := ContainerToHost(worktree, name, p)
		if !ok {
			t.Errorf("ContainerToHost(%q) not recognised as under the mount %q", p, MountPath(name))
			continue
		}
		if rel, err := filepath.Rel(worktree, got); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Errorf("ContainerToHost(%q) = %q escaped the worktree %q", p, got, worktree)
		}
	}
}
