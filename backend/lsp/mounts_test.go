package lsp

import (
	"path/filepath"
	"testing"

	"kanban/backend/store"
	"kanban/backend/types"
)

// TestShadowsToolState: borrowing a project's mounts is how the server reads its dependency roots,
// but borrowing the server's *own* cache or scratch space silences it — borrowed mounts are
// read-only.
func TestShadowsToolState(t *testing.T) {
	cases := []struct {
		containerPath string
		shadowed      bool
		why           string
	}{
		{"/root/.cache/go-build", true, "gopls' GOCACHE — the mount that silenced this repo's diagnostics"},
		{"/root/.cache", true, "the cache root itself"},
		{"/home/node/.cache", true, "a cache root under a non-root home, as node-based images have"},
		{"/.cache", true, "a cache root at the filesystem root"},
		{"/tmp", true, "scratch space the container expects to write"},
		{"/var/tmp", true, "the other scratch space"},
		{"/tmp/fixtures", false, "a subtree of tmpfs does not stop the container writing to /tmp"},
		{"/root/projects", false, "the user's data under the container's home is exactly what borrowing is for"},
		{"/go", false, "a toolchain root the user may share read-only"},
		{"/mnt/MIKE/frontend/node_modules", false, "dependencies under the workspace mount"},
		{"/mnt/MIKE/frontend/.cache/dist", false, "a project's own .cache is the user's file tree, not the tool's state"},
	}
	for _, c := range cases {
		if got := shadowsToolState(c.containerPath); got != c.shadowed {
			t.Errorf("shadowsToolState(%q) = %v, want %v (%s)", c.containerPath, got, c.shadowed, c.why)
		}
	}
}

// TestAdaptProjectMountsKeepsToolStateOutOfTheBorrow drives the rule through the real adapter with
// this project's own Sandbox Configuration: the node_modules mount still arrives (remapped onto the
// worktree, as the LSP sandbox's workspace is the worktree), the data mount still arrives, and the
// build-cache mount is refused.
func TestAdaptProjectMountsKeepsToolStateOutOfTheBorrow(t *testing.T) {
	projectDir := t.TempDir()
	db, err := store.NewStore(filepath.Join(t.TempDir(), "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.CreateProject(&types.Project{
		ID:   "MIKE",
		Name: "MIKE",
		Path: projectDir,
		SandboxMounts: []types.SandboxMount{
			{HostPath: "/tmp/kanban-mount/go", ContainerPath: "/root/.cache/go-build", Mode: "rw"},
			{HostPath: "/tmp/kanban-mount/node_modules", ContainerPath: "/home/sandbox/MIKE/frontend/node_modules", Mode: "rw"},
			{HostPath: "/tmp/kanban-mount/projects", ContainerPath: "/root/projects", Mode: "rw"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	m := &ExtensionLSPManager{projects: db}
	wsContainer := "/mnt/frost-amber-quartz"
	// The workspace is a task worktree inside the project, which is what the adapter is
	// asked about in production.
	adapted := m.adaptProjectMounts(filepath.Join(projectDir, ".worktrees", "MIKE", "frost-amber-quartz"), wsContainer)

	got := map[string]string{}
	for _, mo := range adapted {
		got[mo.HostPath] = mo.ContainerPath
		if mo.Mode != "ro" {
			t.Errorf("borrowed mount %s -> %s has mode %q; borrowed mounts are always read-only", mo.HostPath, mo.ContainerPath, mo.Mode)
		}
	}
	if _, banned := got["/tmp/kanban-mount/go"]; banned {
		t.Error("the build-cache mount was borrowed: the language server inside this sandbox can no longer report anything")
	}
	if got["/tmp/kanban-mount/node_modules"] != wsContainer+"/frontend/node_modules" {
		t.Errorf("node_modules mount = %q, want it remapped under the workspace (%s/frontend/node_modules)",
			got["/tmp/kanban-mount/node_modules"], wsContainer)
	}
	if got["/tmp/kanban-mount/projects"] != "/root/projects" {
		t.Errorf("data mount = %q, want /root/projects to pass through", got["/tmp/kanban-mount/projects"])
	}
}
