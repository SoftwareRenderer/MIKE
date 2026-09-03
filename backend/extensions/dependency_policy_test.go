package extensions

// A granted dependency directory becomes a mount root at exactly one moment and for exactly one
// extension: when that extension's container is being built. Everywhere else — install, the
// settings form, another extension's container — the allowlist must stay as narrow as it was
// before this feature existed, or "the user gave gopls a folder" would quietly become "any
// extension may ask for that folder".

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// grantCandidate returns an existing absolute directory that is NOT inside any policy root.
//
// The requirement sounds trivial and is not: the policy always allows the system temp dir, and a
// test's own temp directory lives in it, so a t.TempDir() would be accepted by every variant
// including the ones that are supposed to refuse. The home directory is outside the workspace,
// the project paths, the extensions dir and the temp dir alike, which is also exactly what a real
// Go module cache or Cargo home looks like.
func grantCandidate(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory to act as a path outside the app's roots: %v", err)
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		t.Skipf("%s is not a usable directory: %v", home, err)
	}
	return filepath.Clean(home)
}

func dependencyExtension(id, hostPath string, writable bool) *Extension {
	readOnly := !writable
	ext := &Extension{ID: id, Name: "gopls", Type: "lsp"}
	ext.SandboxConfig = SandboxConfig{
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE", ReadOnly: &readOnly},
		DependencyHostPath: hostPath,
	}
	return ext
}

func sandboxManagerForTest(t *testing.T) *ExtensionSandboxManager {
	t.Helper()
	store := testStore(t)
	return NewExtensionSandboxManager(NewRegistryClient(), store)
}

func TestMountPolicyForExtension_GrantIsARootForItsOwnExtensionOnly(t *testing.T) {
	sm := sandboxManagerForTest(t)
	grant := grantCandidate(t)

	granted := sm.MountPolicyForExtension(dependencyExtension("ext-gopls", grant, false))
	resolved, err := granted.ValidateHostPath(grant)
	if err != nil {
		t.Fatalf("the granted directory must be mountable by the extension it was granted to: %v", err)
	}

	// Another extension of the same install gets the base allowlist, so its manifest cannot name
	// this directory and get it.
	other := sm.MountPolicyForExtension(dependencyExtension("ext-other", "", false))
	if _, err := other.ValidateHostPath(grant); err == nil {
		t.Error("a second extension was handed another extension's granted directory")
	}

	// And the base policy still refuses it: this is the one the install-time and settings-time
	// validators use, so an extension still cannot self-grant anything.
	if _, err := sm.MountPolicy().ValidateHostPath(grant); err == nil {
		t.Error("the narrow policy accepted a granted dependency directory — an extension could self-grant it")
	}

	// A path nobody granted is still rejected, grant or no grant.
	foreign := filepath.Join(filepath.Dir(grant), "definitely-not-granted-"+t.Name())
	if _, err := granted.ValidateHostPath(foreign); err == nil {
		t.Errorf("a path outside every root was accepted even with a grant: %s", foreign)
	}
	// Inside the container-side argument of the same rule, nothing changed either.
	if err := ValidateContainerPath("/go/pkg/mod"); err != nil {
		t.Errorf("the container-path rule should be untouched: %v", err)
	}
	if resolved == "" {
		t.Error("ValidateHostPath returned no resolved path")
	}
}

func TestMountPolicyForExtension_ReadOnlyDeclarationIsPinned(t *testing.T) {
	// The pinning is what makes "read-only" a property of the mount rather than a polite default:
	// a mount spec asking for rw against this root still comes out ro.
	sm := sandboxManagerForTest(t)
	grant := grantCandidate(t)

	ro := sm.MountPolicyForExtension(dependencyExtension("ext-gopls", grant, false))
	resolved, err := ro.ValidateHostPath(grant)
	if err != nil {
		t.Fatal(err)
	}
	if !ro.ForceReadOnly(resolved) {
		t.Error("a read-only dependency declaration must be pinned read-only, whatever the mount asks for")
	}

	rw := sm.MountPolicyForExtension(dependencyExtension("ext-gopls", grant, true))
	resolvedRW, err := rw.ValidateHostPath(grant)
	if err != nil {
		t.Fatal(err)
	}
	if rw.ForceReadOnly(resolvedRW) {
		t.Error("a declaration that asks to write (a toolchain module that must be downloadable) was pinned read-only")
	}

	// The base policy's own pins are untouched by any of this — a project-configured path stays
	// read-only for everyone.
	if base := sm.MountPolicy(); base == nil || len(base.Roots()) == 0 {
		t.Error("the base policy lost its roots")
	}
}

func TestMountPolicyForExtension_NarrowAtInstallAndSettings(t *testing.T) {
	// The gate the plan cares about most: the validator behind install, update and
	// PUT /api/extensions/{id} must not see anyone's grant, so a manifest cannot reach it.
	store := testStore(t)
	sm := NewExtensionSandboxManager(NewRegistryClient(), store)
	h := NewExtensionHandler(store, sm, NewRegistryClient(), &ExtensionConfig{})
	grant := grantCandidate(t)

	ext := dependencyExtension("ext-gopls-narrow", grant, false)
	if err := store.Create(ext); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mountPolicy().ValidateHostPath(grant); err == nil {
		t.Error("the install/settings policy accepts a granted dependency directory — a manifest could declare it as an ordinary mount")
	}

	// The grant itself is refused there on its own terms too (task 2), independently of the roots.
	c := &LSPConfig{FileTypes: []LSPFileType{{Extension: "go", LanguageID: "go"}}}
	c.Sandbox = &SandboxConfig{DependencyHostPath: grant}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "may not come from a manifest") {
		t.Errorf("a manifest supplying the grant was not refused with the right reason: %v", err)
	}
}

func TestDependencyGrantNeedsBothHalves(t *testing.T) {
	// Half a configuration binds nothing: a grant with no declaration has no container path and no
	// env to assign, and a declaration with no grant is the normal state before anyone is asked.
	host, writable := (&SandboxConfig{DependencyHostPath: "/some/dir"}).DependencyGrant()
	if host != "" || writable {
		t.Errorf("grant without a declaration = (%q, %v), want nothing bound", host, writable)
	}
	host, writable = (&SandboxConfig{Dependency: &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"}}).DependencyGrant()
	if host != "" || writable {
		t.Errorf("declaration without a grant = (%q, %v), want nothing bound", host, writable)
	}

	var none *SandboxConfig
	if host, writable := none.DependencyGrant(); host != "" || writable {
		t.Error("a missing sandbox config must bind nothing")
	}

	// Writable follows the declaration, never the grant: the user choosing a directory cannot make
	// an extension write into it.
	host, writable = (&SandboxConfig{
		DependencyHostPath: "/some/dir",
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
	}).DependencyGrant()
	if host != "/some/dir" || writable {
		t.Errorf("default declaration = (%q, %v), want the granted path read-only", host, writable)
	}
	readWrite := false
	host, writable = (&SandboxConfig{
		DependencyHostPath: "/some/dir",
		Dependency:         &DependencyCache{ContainerPath: "/usr/local/cargo", Env: "CARGO_HOME", ReadOnly: &readWrite},
	}).DependencyGrant()
	if host != "/some/dir" || !writable {
		t.Errorf("read_only:false declaration = (%q, %v), want the granted path writable", host, writable)
	}
}

func TestBuildExtensionCreateArgs_UsesNoGrantItself(t *testing.T) {
	// The argument builder takes its policy from the caller, so the per-extension behaviour lives
	// in one place (MountPolicyForExtension) and a test that passes nil still refuses everything.
	ext := dependencyExtension("ext-dep-mount", grantCandidate(t), false)
	ext.SandboxConfig.Mounts = []types.SandboxMount{{HostPath: grantCandidate(t), ContainerPath: "/go/pkg/mod", Mode: "rw"}}
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-dep-mount", "ubuntu:latest", nil, nil)
	for _, a := range args {
		if strings.HasPrefix(a, "/") && strings.Contains(a, ":/go/pkg/mod") {
			t.Errorf("a nil policy mounted %q — the policy is the gate, not the extension's word", a)
		}
	}
}
