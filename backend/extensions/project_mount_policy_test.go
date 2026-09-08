package extensions

// The project-mount map a create receives is not always operator-chosen: the LSP workspace arrives
// in it, from a client message. These hold the source to the same policy as the extension's own
// mounts, and hold the compatibility snapshot stable when a source is refused.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildExtensionCreateArgs_ProjectMountOutsidePolicyIsSkipped(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(allowed, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("plant symlink inside the root: %v", err)
	}
	ext := &Extension{ID: "ext-proj-mount", Name: "gopls", Type: "lsp"}
	policy := newMountPolicy([]string{allowed})

	args, mounted := buildExtensionCreateArgs(ext, "kanban-ext-ext-proj-mount", "ubuntu:latest",
		map[string]string{link: "/mnt/link"}, policy)

	if hasElem(mountSpecs(args), link+":/mnt/link:rprivate,ro") {
		t.Errorf("a workspace resolving to %q was mounted: %v", outside, mountSpecs(args))
	}
	// Refused, but recorded: the record is what MountsCompatible compares the desired set
	// against, and a set that never matches recreates the sandbox on every request.
	if !hasElem(mounted, link) {
		t.Errorf("the refused source is missing from the compatibility snapshot %v, which makes every later request look like a mount change", mounted)
	}
}

func TestBuildExtensionCreateArgs_ProjectMountInsidePolicyIsMounted(t *testing.T) {
	// The control case: a workspace that really is under a root still gets its mount, so the
	// check above is a refusal and not every mount being skipped.
	allowed := t.TempDir()
	worktree := filepath.Join(allowed, "worktrees", "task-1")
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}
	ext := &Extension{ID: "ext-proj-mount-ok", Name: "gopls", Type: "lsp"}

	args, mounted := buildExtensionCreateArgs(ext, "kanban-ext-ext-proj-mount-ok", "ubuntu:latest",
		map[string]string{worktree: "/mnt/task-1"}, newMountPolicy([]string{allowed}))

	spec := worktree + ":/mnt/task-1:rprivate,ro"
	if !hasElem(mountSpecs(args), spec) {
		t.Errorf("a workspace inside the policy roots is not mounted: %v", mountSpecs(args))
	}
	if !hasElem(mounted, worktree) {
		t.Errorf("the mounted source is missing from the compatibility snapshot: %v", mounted)
	}
}
