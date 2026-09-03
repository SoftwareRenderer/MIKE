package lsp

// Tests for DependencyCaches, the task sandbox's view of granted directories: the same derivation the
// extension's own sandbox uses decides what is bindable, and a contested path or variable is claimed in
// extension-ID order with the loser logged.

import (
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/extensions"
)

// grantedPath resolves a directory the way the grant machinery stores it — symlinks and all, so a
// test that compares what comes back does not fail on a machine whose temp directory is a link
// (/var → /private/var on macOS) rather than on a change in the code.
func grantedPath(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func grantLSPExt(t *testing.T, es *extensions.ExtensionStore, id, hostPath, containerPath, env string, enabled bool) {
	t.Helper()
	ext := makeTestLSPExt(id, id)
	ext.ID = id
	ext.Enabled = enabled
	if hostPath != "" {
		ext.SandboxConfig = extensions.SandboxConfig{
			Dependency:         &extensions.DependencyCache{ContainerPath: containerPath, Env: env},
			DependencyHostPath: hostPath,
		}
	}
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create(%s): %v", id, err)
	}
}

func dependencyManager(t *testing.T, es *extensions.ExtensionStore) *ExtensionLSPManager {
	t.Helper()
	return NewExtensionLSPManager(es, extensions.NewExtensionSandboxManager(extensions.NewRegistryClient(), es), nil)
}

func cacheByPath(caches []DependencyCache, containerPath string) (DependencyCache, bool) {
	for _, c := range caches {
		if c.ContainerPath == containerPath {
			return c, true
		}
	}
	return DependencyCache{}, false
}

func TestDependencyCaches_UnionsEveryEnabledExtension(t *testing.T) {
	es := testExtensionStore(t)
	mod, cargo := t.TempDir(), t.TempDir()
	wantMod, wantCargo := grantedPath(t, mod), grantedPath(t, cargo)
	grantLSPExt(t, es, "go-gopls", mod, "/go/pkg/mod", "GOMODCACHE", true)
	grantLSPExt(t, es, "rust-analyzer", cargo, "/usr/local/cargo", "CARGO_HOME", true)
	// Neither of these may appear: one is switched off, the other declares where it looks but has
	// been granted nothing. Both would otherwise put an unmounted path in front of every task.
	grantLSPExt(t, es, "off-gopls", t.TempDir(), "/go/pkg/mod", "GOMODCACHE", false)
	grantLSPExt(t, es, "ungranted-tsserver", "", "/srv/node_modules", "NODE_PATH", true)

	caches := dependencyManager(t, es).DependencyCaches()
	if len(caches) != 2 {
		t.Fatalf("DependencyCaches() = %+v, want the two enabled and granted extensions", caches)
	}
	for _, want := range []DependencyCache{
		{HostPath: wantMod, ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		{HostPath: wantCargo, ContainerPath: "/usr/local/cargo", Env: "CARGO_HOME"},
	} {
		if got, ok := cacheByPath(caches, want.ContainerPath); !ok || got.HostPath != want.HostPath || got.Env != want.Env {
			t.Errorf("cache for %s = %+v, want host %s assigned to the container path", want.ContainerPath, got, want.HostPath)
		}
	}

	// A directory deleted after being granted is absent here exactly as it is absent from the
	// extension's own container: the two consumers cannot disagree about whether it exists.
	if err := os.RemoveAll(cargo); err != nil {
		t.Fatal(err)
	}
	if _, ok := cacheByPath(dependencyManager(t, es).DependencyCaches(), "/usr/local/cargo"); ok {
		t.Error("a deleted dependency directory is still offered to the task sandbox")
	}
}

func TestDependencyCaches_ClaimsAreWonInTheSameOrderEveryTime(t *testing.T) {
	es := testExtensionStore(t)
	first, second := t.TempDir(), t.TempDir()
	wantFirst := grantedPath(t, first)
	// Created in the losing order on purpose: the walk is by ID, so which extension's directory lands
	// at /go/pkg/mod must not depend on which was installed first.
	grantLSPExt(t, es, "z-second", second, "/go/pkg/mod", "GOMODCACHE", true)
	grantLSPExt(t, es, "a-first", first, "/go/pkg/mod", "GOMODCACHE", true)

	caches := dependencyManager(t, es).DependencyCaches()
	if len(caches) != 1 {
		t.Fatalf("two extensions claimed one container path and both were passed on: %+v", caches)
	}
	if caches[0].HostPath != wantFirst {
		t.Errorf("the granted directory is %q, want the lowest extension ID's (%q) so the answer survives a restart", caches[0].HostPath, wantFirst)
	}
}

func TestDependencyCaches_AVariableIsAssignedOnce(t *testing.T) {
	es := testExtensionStore(t)
	first, second := t.TempDir(), t.TempDir()
	// Different container paths, same variable: only one of them can be what GOMODCACHE means. The
	// loser still gets its directory mounted — a server that reads it by another route gets it — but
	// a variable never points at a directory that is not the one the winner brought.
	grantLSPExt(t, es, "a-first", first, "/go/pkg/mod", "GOMODCACHE", true)
	grantLSPExt(t, es, "z-second", second, "/opt/gomod", "GOMODCACHE", true)

	caches := dependencyManager(t, es).DependencyCaches()
	if len(caches) != 2 {
		t.Fatalf("DependencyCaches() = %+v, want both directories", caches)
	}
	loser, ok := cacheByPath(caches, "/opt/gomod")
	if !ok {
		t.Fatalf("the second directory is missing: %+v", caches)
	}
	if loser.Env != "" {
		t.Errorf("two assignments for one variable: %+v", caches)
	}
	if winner, _ := cacheByPath(caches, "/go/pkg/mod"); winner.Env != "GOMODCACHE" {
		t.Errorf("the winner lost its assignment: %+v", caches)
	}
}

func TestDependencyCaches_WithoutTheSandboxManagerThereIsNothingToSay(t *testing.T) {
	es := testExtensionStore(t)
	grantLSPExt(t, es, "go-gopls", t.TempDir(), "/go/pkg/mod", "GOMODCACHE", true)
	if got := NewExtensionLSPManager(es, nil, nil).DependencyCaches(); got != nil {
		t.Errorf("an extension sandbox manager is what resolves a grant; without one DependencyCaches = %v", got)
	}
	var nilMgr *ExtensionLSPManager
	if got := nilMgr.DependencyCaches(); got != nil {
		t.Errorf("a nil manager must answer nothing, got %v", got)
	}
}
