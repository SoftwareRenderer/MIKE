package lsp

// What ensureSandbox hands to a container create, and what it later asks a running container about,
// are two different lists — and the bug this pins is what happens when they are treated as one.

import (
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/extensions"
	"kanban/backend/store"
	"kanban/backend/types"
)

func planFixture(t *testing.T, grant string, projectMounts []types.SandboxMount) (*ExtensionLSPManager, *extensions.Extension, string) {
	t.Helper()
	root := t.TempDir()
	projPath := filepath.Join(root, "MIKE")
	wsPath := filepath.Join(projPath, "worktrees", "MIKE", "flint")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}

	projDB, err := store.NewStore(filepath.Join(t.TempDir(), "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projDB.Close() })
	if err := projDB.CreateProject(&types.Project{ID: "MIKE", Name: "MIKE", Path: projPath, SandboxMounts: projectMounts}); err != nil {
		t.Fatal(err)
	}

	extStore := testExtensionStore(t)
	ext := makeTestLSPExt("go-gopls", "go")
	ext.SandboxConfig = extensions.SandboxConfig{
		Envs:               []types.SandboxEnv{{Key: "GOCACHE", Value: "/var/cache/gopls/go-build"}},
		Dependency:         &extensions.DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		DependencyHostPath: grant,
	}
	if err := extStore.Create(ext); err != nil {
		t.Fatal(err)
	}

	mgr := NewExtensionLSPManager(extStore, extensions.NewExtensionSandboxManager(extensions.NewRegistryClient(), extStore), projDB)
	stored, err := extStore.GetByID("go-gopls")
	if err != nil {
		t.Fatal(err)
	}
	return mgr, stored, wsPath
}

func countContainerPath(mounts []types.SandboxMount, containerPath string) int {
	n := 0
	for _, m := range mounts {
		if m.ContainerPath == containerPath {
			n++
		}
	}
	return n
}

// TestSandboxMountPlan_ComparesWhatCreateWillRecord is the invariant behind "your grant does nothing"
// and "the language server restarts every time you open a file" — the two failures this split prevents.
// Create appends the granted dependency directory itself, so it must not already be in the list handed
// to it: the recorded snapshot is built from that list plus the same append, and a snapshot holding the
// mount twice is one no comparison can match, which makes every subsequent workspace open destroy a
// running language server and start a new one.
func TestSandboxMountPlan_ComparesWhatCreateWillRecord(t *testing.T) {
	grant := t.TempDir()
	mgr, ext, wsPath := planFixture(t, grant, nil)
	// No project is mounted into this sandbox: the point of the fixture is the dependency directory,
	// and a project mount would only add a second reason for a container path to appear.
	var projectMounts map[string]string

	create, compare, envs := mgr.sandboxMountPlan(ext, wsPath, projectMounts, containerWorkspacePath(wsPath, projectMounts))

	if n := countContainerPath(create, "/go/pkg/mod"); n != 0 {
		t.Errorf("the create list carries the dependency directory (%d×) — Create appends it, so the snapshot would hold it twice", n)
	}
	if n := countContainerPath(compare, "/go/pkg/mod"); n != 1 {
		t.Errorf("the comparison list carries the dependency directory %d×, want exactly once: %+v", n, compare)
	}
	if len(compare) != len(create)+1 {
		t.Errorf("comparison has %d mounts, create has %d — the two lists must differ by the dependency bind alone", len(compare), len(create))
	}
	if last := compare[len(compare)-1]; last.ContainerPath != "/go/pkg/mod" || last.Mode != "ro" {
		t.Errorf("the dependency bind is %+v, want the declared path mounted read-only", last)
	}

	// The assignment travels with it, or the comparison asks about a container that was created with
	// the variable set and the check says "envs changed" on every open.
	if len(envs) != len(ext.SandboxConfig.Envs)+1 || envs[len(envs)-1].Key != "GOMODCACHE" || envs[len(envs)-1].Value != "/go/pkg/mod" {
		t.Errorf("compareEnvs = %v, want the extension's envs plus GOMODCACHE at the container path", envs)
	}
	// The extension's own record is never mutated by planning.
	if len(ext.SandboxConfig.Envs) != 1 {
		t.Error("sandboxMountPlan mutated the extension's declared envs")
	}
}

func TestSandboxMountPlan_ProjectMountWantingTheCachePathGivesWay(t *testing.T) {
	grant := t.TempDir()
	// The project grafts something else onto the container path the extension declared for its
	// dependencies. Two -v specs on one path make podman refuse the whole create, and the declared
	// path cannot move, so the borrowed mount is the one that goes — out of both lists, so the
	// comparison never expects a mount the create refused.
	mgr, ext, wsPath := planFixture(t, grant, []types.SandboxMount{{HostPath: "/tmp/graft", ContainerPath: "/go/pkg/mod", Mode: "rw"}})
	var projectMounts map[string]string

	create, compare, _ := mgr.sandboxMountPlan(ext, wsPath, projectMounts, containerWorkspacePath(wsPath, projectMounts))
	if n := countContainerPath(create, "/go/pkg/mod"); n != 0 {
		t.Errorf("a borrowed mount reached the create at core's container path: %+v", create)
	}
	if n := countContainerPath(compare, "/go/pkg/mod"); n != 1 {
		t.Errorf("the comparison expects %d mounts at the dependency path, want only core's", n)
	}

	// With nothing granted, the same project mount is just a mount: nothing reserves the path.
	noGrant, ext2, ws2 := planFixture(t, "", []types.SandboxMount{{HostPath: "/tmp/graft", ContainerPath: "/go/pkg/mod", Mode: "rw"}})
	create2, compare2, envs2 := noGrant.sandboxMountPlan(ext2, ws2, nil, containerWorkspacePath(ws2, nil))
	if n := countContainerPath(create2, "/go/pkg/mod"); n != 1 {
		t.Errorf("without a grant the project mount should survive: %+v", create2)
	}
	if len(compare2) != len(create2) || len(envs2) != len(ext2.SandboxConfig.Envs) {
		t.Errorf("without a grant the two lists must be the same list: %d vs %d (envs %d vs %d)", len(compare2), len(create2), len(envs2), len(ext2.SandboxConfig.Envs))
	}
}
