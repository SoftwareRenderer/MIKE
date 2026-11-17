package extensions

// A manifest is registry text, so "rw" on the workspace dir or another project's checkout is a
// request to read, never a grant to write: the workspace holds every task's worktree.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/store"
	"mike/backend/types"

	"mike/pkg/sandboxruntime"
)

// jailManager builds a manager over a fresh database with one registered project, so the policy
// is the one a running install answers with.
func jailManager(t *testing.T, workspace, projectPath, extDir string) *ExtensionSandboxManager {
	t.Helper()
	return jailManagerWithProjectMounts(t, workspace, projectPath, extDir, nil)
}

// jailManagerWithProjectMounts is jailManager with the Sandbox Configuration mounts that project
// declares: the host paths it borrows into its own task container, which the shared LSP sandbox may
// read and never write.
func jailManagerWithProjectMounts(t *testing.T, workspace, projectPath, extDir string, projectMounts []types.SandboxMount) *ExtensionSandboxManager {
	t.Helper()
	db, err := store.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	es := NewExtensionStore(db, nil)
	if err := es.InitSchema(); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if err := db.CreateProject(&types.Project{
		ID: "proj-jail", Name: "proj-jail", Path: projectPath, SandboxEnabled: true,
		SandboxMounts: projectMounts,
	}); err != nil {
		t.Fatalf("register project: %v", err)
	}
	sm := NewExtensionSandboxManager(NewRegistryClient(), es, sandboxruntime.Boot{})
	sm.SetWorkspaceDir(workspace)
	sm.SetExtensionsDir(extDir)
	return sm
}

// threeDirs returns a workspace, a project checkout beside it, and an extensions directory.
func threeDirs(t *testing.T) (workspace, projectPath, extDir string) {
	t.Helper()
	root := t.TempDir()
	workspace = filepath.Join(root, "worktrees")
	projectPath = filepath.Join(root, "checkout")
	extDir = filepath.Join(root, "extensions")
	for _, d := range []string{workspace, projectPath, extDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return workspace, projectPath, extDir
}

func TestMountPolicy_WorkspaceAndProjectsAreReadOnly(t *testing.T) {
	workspace, projectPath, extDir := threeDirs(t)
	policy := jailManager(t, workspace, projectPath, extDir).MountPolicy()

	for _, borrowed := range []string{workspace, projectPath} {
		resolved, err := policy.ValidateHostPath(borrowed)
		if err != nil {
			t.Fatalf("%s must stay mountable — a language server has to see the tree it serves: %v", borrowed, err)
		}
		if !policy.ForceReadOnly(resolved) {
			t.Errorf("%s is not pinned read-only, so a manifest naming it gets a writable host directory", borrowed)
		}
		// A subtree of a borrowed root is the same root seen from below.
		sub := filepath.Join(borrowed, "proj-jail", "task-1", "src")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		subResolved, err := policy.ValidateHostPath(sub)
		if err != nil {
			t.Fatalf("a worktree under the workspace must be mountable: %v", err)
		}
		if !policy.ForceReadOnly(subResolved) {
			t.Errorf("%s is not pinned read-only through its root", sub)
		}
	}

	// The control: the extensions dir is writable, so the assertions above are a refusal rather
	// than everything being refused.
	own, err := policy.ValidateHostPath(extDir)
	if err != nil {
		t.Fatalf("the extensions dir must stay a root: %v", err)
	}
	if policy.ForceReadOnly(own) {
		t.Error("the extensions dir is pinned read-only, so an extension cannot write its own data")
	}
}

// TestBuildExtensionCreateArgs_SelfGrantedWorkspaceWriteIsRefused: a manifest is registry text, and
// the workspace holds every task's worktree, so "rw" on it — or on another project's checkout, or on
// the host path a project's own Sandbox Configuration declares for its task container — is a request
// to read, never a grant to write. The pin lives in the policy, not in the manifest's mode, so a
// stored row that predates the rule is mounted read-only too.
func TestBuildExtensionCreateArgs_SelfGrantedWorkspaceWriteIsRefused(t *testing.T) {
	workspace, projectPath, extDir := threeDirs(t)
	// A cache a project declares for its own task container, beside (not under) the borrowed roots,
	// so the refusal below is the borrow rule and not the workspace rule seen twice.
	projectCache := filepath.Join(filepath.Dir(workspace), "project-go-cache")
	if err := os.MkdirAll(projectCache, 0o755); err != nil {
		t.Fatal(err)
	}
	sm := jailManagerWithProjectMounts(t, workspace, projectPath, extDir, []types.SandboxMount{
		{HostPath: projectCache, ContainerPath: "/go/pkg/mod", Mode: "rw"},
	})

	specFor := func(host string) string {
		t.Helper()
		ext := &Extension{ID: "ext-selfgrant", Name: "gopls", Type: "lsp",
			SandboxConfig: SandboxConfig{Mounts: []types.SandboxMount{
				{HostPath: host, ContainerPath: "/borrowed", Mode: "rw"},
			}}}
		args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-selfgrant", "img:latest", nil,
			sm.MountPolicyForExtension(ext))
		mounts := mountSpecs(args)
		if len(mounts) != 1 {
			t.Fatalf("manifest mount for %s produced %v, want one spec", host, mounts)
		}
		return mounts[0]
	}

	for _, borrowed := range []string{workspace, projectPath, projectCache} {
		if spec := specFor(borrowed); !strings.HasSuffix(spec, ",ro") {
			t.Errorf("a manifest asking for rw on the borrowed %s got %q", borrowed, spec)
		}
	}
	// The control: the extensions dir belongs to the extension, so the mode it asked for survives and
	// the assertions above are a refusal rather than every mount being refused.
	if spec := specFor(extDir); !strings.HasSuffix(spec, ",rw") {
		t.Errorf("the extensions dir lost the mode it asked for: %q", spec)
	}

	// No policy at all is the strictest case, not the loosest: nothing is inside an empty allowlist.
	ext := &Extension{ID: "ext-none", Name: "gopls", Type: "lsp",
		SandboxConfig: SandboxConfig{Mounts: []types.SandboxMount{
			{HostPath: projectCache, ContainerPath: "/borrowed", Mode: "rw"},
		}}}
	if args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-none", "img:latest", nil, nil); len(mountSpecs(args)) != 0 {
		t.Errorf("a nil policy mounted %v", mountSpecs(args))
	}
}

func TestWithExtraRootsKeepsReadOnlyPins(t *testing.T) {
	workspace, _, extDir := threeDirs(t)
	policy := jailManager(t, workspace, filepath.Join(filepath.Dir(workspace), "unused"), extDir).MountPolicy()

	resolvedWorkspace, err := policy.ValidateHostPath(workspace)
	if err != nil {
		t.Fatal(err)
	}

	// Adding the handler's extensions dir must not un-pin the roots already pinned.
	extra, err := os.MkdirTemp("", "handler-extdir")
	if err != nil {
		t.Fatal(err)
	}
	widened := policy.withExtraRoots(extra)
	if !widened.ForceReadOnly(resolvedWorkspace) {
		t.Error("withExtraRoots dropped the read-only pins, so the widened policy hands out writes")
	}
	if _, err := widened.ValidateHostPath(extra); err != nil {
		t.Errorf("the added root is not mountable: %v", err)
	}
	// The receiver is untouched.
	if len(policy.Roots()) >= len(widened.Roots()) {
		t.Errorf("withExtraRoots changed the receiver: %v -> %v", policy.Roots(), widened.Roots())
	}
}
