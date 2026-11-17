package extensions

// A granted dependency directory becomes a mount root for exactly one extension, at exactly one moment:
// when that extension's container is built. Everywhere else — install, the settings form, another
// extension's container — the allowlist must be as narrow as it was before.

import (
	"os"
	"path/filepath"
	"testing"

	"mike/pkg/sandboxruntime"
)

// grantCandidate returns an existing absolute directory that is NOT inside any policy root. Not
// trivial: the policy always allows the system temp dir, and a test's own temp dir lives in it, so
// t.TempDir() would be accepted by every variant including the ones that must refuse.
func grantCandidate(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("no home directory to act as a path outside the app's roots: %v", err)
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		t.Fatalf("%s is not a usable directory: %v", home, err)
	}
	return filepath.Clean(home)
}

func dependencyExtension(id, hostPath string) *Extension {
	ext := &Extension{ID: id, Name: "gopls", Type: "lsp"}
	ext.SandboxConfig = SandboxConfig{
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		DependencyHostPath: hostPath,
	}
	return ext
}

func sandboxManagerForTest(t *testing.T) *ExtensionSandboxManager {
	t.Helper()
	store := testStore(t)
	return NewExtensionSandboxManager(NewRegistryClient(), store, sandboxruntime.Boot{})
}

func TestMountPolicyForExtension_GrantIsARootForItsOwnExtensionOnly(t *testing.T) {
	sm := sandboxManagerForTest(t)
	grant := grantCandidate(t)

	granted := sm.MountPolicyForExtension(dependencyExtension("ext-gopls", grant))
	resolved, err := granted.ValidateHostPath(grant)
	if err != nil {
		t.Fatalf("the granted directory must be mountable by the extension it was granted to: %v", err)
	}

	// Another extension of the same install gets the base allowlist, so its manifest cannot name
	// this directory and get it.
	other := sm.MountPolicyForExtension(dependencyExtension("ext-other", ""))
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
	if resolved == "" {
		t.Error("ValidateHostPath returned no resolved path")
	}
}

func TestMountPolicyForExtension_GrantIsAlwaysPinnedReadOnly(t *testing.T) {
	// The pinning is what makes "read-only" a property of the mount rather than a polite default:
	// a mount spec asking for rw against this root still comes out ro. There is no declaration that
	// can lift it — the containers that fill the cache mount it read-write from their own side.
	sm := sandboxManagerForTest(t)
	grant := grantCandidate(t)

	policy := sm.MountPolicyForExtension(dependencyExtension("ext-gopls", grant))
	resolved, err := policy.ValidateHostPath(grant)
	if err != nil {
		t.Fatalf("the grant is not a root for its own extension: %v", err)
	}
	if !policy.ForceReadOnly(resolved) {
		t.Error("the granted dependency directory is not pinned read-only")
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
	sm := NewExtensionSandboxManager(NewRegistryClient(), store, sandboxruntime.Boot{})
	h := NewExtensionHandler(store, sm, NewRegistryClient(), &ExtensionConfig{})
	grant := grantCandidate(t)

	ext := dependencyExtension("ext-gopls-narrow", grant)
	if err := store.Create(ext); err != nil {
		t.Fatal(err)
	}
	if _, err := h.mountPolicy().ValidateHostPath(grant); err == nil {
		t.Error("the install/settings policy accepts a granted dependency directory — a manifest could declare it as an ordinary mount")
	}
}

func TestDependencyGrantNeedsBothHalves(t *testing.T) {
	// Half a configuration binds nothing: a grant with no declaration has no container path and no
	// env to assign, and a declaration with no grant is the normal state before anyone is asked.
	if host := (&SandboxConfig{DependencyHostPath: "/some/dir"}).DependencyGrant(); host != "" {
		t.Errorf("grant without a declaration = %q, want nothing bound", host)
	}
	if host := (&SandboxConfig{Dependency: &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"}}).DependencyGrant(); host != "" {
		t.Errorf("declaration without a grant = %q, want nothing bound", host)
	}

	var none *SandboxConfig
	if host := none.DependencyGrant(); host != "" {
		t.Error("a missing sandbox config must bind nothing")
	}

	if host := (&SandboxConfig{
		DependencyHostPath: "/some/dir",
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
	}).DependencyGrant(); host != "/some/dir" {
		t.Errorf("both halves = %q, want the granted directory", host)
	}

}
