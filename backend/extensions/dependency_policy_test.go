package extensions

// A granted dependency directory becomes a mount root for exactly one extension, at exactly one moment:
// when that extension's container is built. Everywhere else — install, the settings form, another
// extension's container — the allowlist must be as narrow as it was before.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"

	"kanban/pkg/sandboxruntime"
)

// grantCandidate returns an existing absolute directory that is NOT inside any policy root. Not
// trivial: the policy always allows the system temp dir, and a test's own temp dir lives in it, so
// t.TempDir() would be accepted by every variant including the ones that must refuse.
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
	// Inside the container-side argument of the same rule, nothing changed either.
	if err := ValidateContainerPath("/go/pkg/mod"); err != nil {
		t.Errorf("the container-path rule should be untouched: %v", err)
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

	for _, ext := range []*Extension{dependencyExtension("ext-gopls", grant), dependencyExtension("ext-rust", grant)} {
		policy := sm.MountPolicyForExtension(ext)
		resolved, err := policy.ValidateHostPath(grant)
		if err != nil {
			t.Fatal(err)
		}
		if !policy.ForceReadOnly(resolved) {
			t.Errorf("%s: the granted dependency directory is not pinned read-only", ext.ID)
		}
		if _, err := policy.ValidateHostPath(grant); err != nil {
			t.Errorf("the grant is not a root for its own extension: %v", err)
		}
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

	// A stale `read_only` in a stored record changes nothing: the answer is a path, and the mode is
	// the app's. Decoding the JSON a pre-removal record holds is the case that matters.
	var stored SandboxConfig
	if err := json.Unmarshal([]byte(`{"dependency_host_path":"/some/dir","dependency_cache":{"container_path":"/go/pkg/mod","env":"GOMODCACHE","read_only":false}}`), &stored); err != nil {
		t.Fatal(err)
	}
	if host := stored.DependencyGrant(); host != "/some/dir" {
		t.Errorf("a record carrying the removed field = %q, want the granted directory", host)
	}
}

func TestBuildExtensionCreateArgs_UsesNoGrantItself(t *testing.T) {
	// The argument builder takes its policy from the caller, so the per-extension behaviour lives
	// in one place (MountPolicyForExtension) and a test that passes nil still refuses everything.
	ext := dependencyExtension("ext-dep-mount", grantCandidate(t))
	ext.SandboxConfig.Mounts = []types.SandboxMount{{HostPath: grantCandidate(t), ContainerPath: "/go/pkg/mod", Mode: "rw"}}
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-dep-mount", "ubuntu:latest", nil, nil)
	for _, a := range args {
		if strings.HasPrefix(a, "/") && strings.Contains(a, ":/go/pkg/mod") {
			t.Errorf("a nil policy mounted %q — the policy is the gate, not the extension's word", a)
		}
	}
}

// TestGrantShapeNamesADirectoryNotAMount: the shape rule is about what the path IS, not how deep it
// sits. A NAS-style /tank/gomodcache is a perfectly good dependency directory and the old rule refused
// it for no reason now that the only container which ever sees the path is the extension's own,
// read-only. A bare top level is still refused: /tank is the whole mount, and a grant is meant to be
// the dependencies under it, not everything the machine holds.
func TestGrantShapeNamesADirectoryNotAMount(t *testing.T) {
	homes := []string{"/home/dev"}
	for _, good := range []string{"/tank/gomodcache", "/data/go/pkg/mod", "/home/dev/go/pkg/mod", "/opt/cargo/registry"} {
		if err := checkGrantShapeWithHomes(good, homes); err != nil {
			t.Errorf("%s refused: %v", good, err)
		}
	}
	for _, bad := range []string{"/", "/tank", "/data"} {
		if err := checkGrantShapeWithHomes(bad, homes); err == nil {
			t.Errorf("%s accepted, want it refused as a whole top level rather than a dependency directory", bad)
		}
	}
}
