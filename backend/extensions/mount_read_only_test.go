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

func TestBuildExtensionCreateArgs_SelfGrantedWorkspaceWriteIsRefused(t *testing.T) {
	workspace, projectPath, extDir := threeDirs(t)
	sm := jailManager(t, workspace, projectPath, extDir)

	manifestMounts := func(host string) SandboxConfig {
		return SandboxConfig{Mounts: []types.SandboxMount{
			{HostPath: host, ContainerPath: "/borrowed", Mode: "rw"},
		}}
	}
	specFor := func(host string) string {
		t.Helper()
		ext := &Extension{ID: "ext-selfgrant", Name: "gopls", Type: "lsp",
			SandboxConfig: manifestMounts(host)}
		args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-selfgrant", "img:latest", nil,
			sm.MountPolicyForExtension(ext))
		mounts := mountSpecs(args)
		if len(mounts) != 1 {
			t.Fatalf("manifest mount for %s produced %v, want one spec", host, mounts)
		}
		return mounts[0]
	}

	for _, borrowed := range []string{workspace, projectPath} {
		if spec := specFor(borrowed); !strings.HasSuffix(spec, ",ro") {
			t.Errorf("a manifest asking for rw on %s got %q", borrowed, spec)
		}
	}
	if spec := specFor(extDir); !strings.HasSuffix(spec, ",rw") {
		t.Errorf("the extensions dir lost the mode it asked for: %q", spec)
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
