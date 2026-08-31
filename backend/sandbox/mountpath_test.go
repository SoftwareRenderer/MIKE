package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestMountPathIsCleaned: the mount comes from filepath.Join(MountRoot, name), so
// a name stored before ValidateProjectName rejected ".." cannot move it.
func TestMountPathIsCleaned(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"proj", "/home/sandbox/proj"},
		{"a/../../b", "/home/b"},
		{"../escape", "/home/escape"},
		{"sub/dir", "/home/sandbox/sub/dir"},
		{"", MountRoot},
	}
	for _, tt := range tests {
		if got := MountPath(tt.name); got != tt.want {
			t.Errorf("MountPath(%q) = %q, want %q", tt.name, got, tt.want)
		}
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
		container := HostToContainer(worktree, name, p)
		back, ok := ContainerToHost(worktree, name, container)
		if !ok {
			t.Fatalf("ContainerToHost(%q) not recognised as under the mount", container)
		}
		if want := filepath.Clean(p); back != want {
			t.Errorf("round trip of %q = %q, want %q (container %q)", p, back, want, container)
		}
	}

	// The worktree root maps to the mount root.
	if got := HostToContainer(worktree, name, worktree); got != MountPath(name) {
		t.Errorf("HostToContainer(worktree root) = %q, want %q", got, MountPath(name))
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
