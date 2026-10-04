package extensions

// Tests for the mount and assignment a granted directory contributes to a container. The two properties
// that matter: what a create produces is exactly what the compatibility check later asks for (or a
// grant never takes effect), and an unmountable grant is reported instead of silently dropped.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// envSpecs returns the value of every "-e" argument, the counterpart of mountSpecs. A dependency
// directory that is mounted but never named in the environment is a mount nothing reads.
func envSpecs(args []string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" {
			out = append(out, args[i+1])
		}
	}
	return out
}

func grantedExtension(hostPath string) *Extension {
	ext := &Extension{ID: "ext-dep-mount", Name: "gopls", Type: "lsp"}
	ext.SandboxConfig = SandboxConfig{
		Envs:               []types.SandboxEnv{{Key: "GOCACHE", Value: "/var/cache/gopls/go-build"}},
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		DependencyHostPath: hostPath,
	}
	return ext
}

func TestBuildExtensionCreateArgs_MountsTheGrantedDependencyDirectory(t *testing.T) {
	grant := t.TempDir()
	policy := newMountPolicyWithReadOnly([]string{grant}, []string{grant})

	args, _ := buildExtensionCreateArgs(grantedExtension(grant), "kanban-ext-ext-dep-mount", "ubuntu:latest", nil, policy)
	spec := grant + ":/go/pkg/mod:rprivate,ro"
	if !hasElem(mountSpecs(args), spec) {
		t.Errorf("the granted directory is not mounted read-only: %v", mountSpecs(args))
	}
	// The assignment names the CONTAINER path: the host path has no meaning inside the container,
	// and putting it there would tell a process something about the machine it must not learn.
	if !hasElem(envSpecs(args), "GOMODCACHE=/go/pkg/mod") {
		t.Errorf("the assignment is missing or points at the host path: %v", envSpecs(args))
	}
	// The extension's own envs are untouched by any of this.
	if !hasElem(envSpecs(args), "GOCACHE=/var/cache/gopls/go-build") {
		t.Errorf("the extension's declared envs were displaced: %v", envSpecs(args))
	}
}

func TestBuildExtensionCreateArgs_GrantIsReadOnlyEvenWhereTheAppWouldAllowWrite(t *testing.T) {
	// The extension's own bind is read-only because dependencyBind says so, not because the policy
	// happens to pin the root: a policy with no read-only roots at all still gets a read-only cache.
	// Warming it is the task container's job (lsp.DependencySandboxArgs), and one writer per cache is
	// the property that stops a language server editing dependencies every other project reads.
	grant := t.TempDir()

	free := newMountPolicy([]string{grant})
	args, _ := buildExtensionCreateArgs(grantedExtension(grant), "kanban-ext-ext-dep-rw", "ubuntu:latest", nil, free)
	if !hasElem(mountSpecs(args), grant+":/go/pkg/mod:rprivate,ro") {
		t.Errorf("the dependency mount must be read-only whatever the policy pins: %v", mountSpecs(args))
	}
	if hasElem(mountSpecs(args), grant+":/go/pkg/mod:rprivate,rw") {
		t.Errorf("the extension's sandbox was handed a writable dependency cache: %v", mountSpecs(args))
	}
}

func TestBuildExtensionCreateArgs_UnmountableGrantIsReportedAndSkipped(t *testing.T) {
	grant := t.TempDir()
	missing := filepath.Join(grant, "removed-after-granting")

	// The stored grant still points at it (the directory was deleted afterwards). The extension has
	// to start anyway: a missing cache costs it resolved dependencies, whereas refusing to build the
	// container would leave the editor with nothing at all.
	ext := grantedExtension(missing)
	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-dep-missing", "ubuntu:latest", nil, newMountPolicy([]string{grant}))
	if specs := mountSpecs(args); len(specs) != 0 {
		t.Errorf("an unmountable grant produced a mount: %v", specs)
	}
	// And crucially no assignment either: pointing GOMODCACHE at an empty container path would make
	// the server look somewhere nothing is, instead of where its image put its own copy.
	if envs := envSpecs(args); hasElem(envs, "GOMODCACHE=/go/pkg/mod") {
		t.Errorf("the assignment was applied without the mount: %v", envs)
	}

	// A grant that is not a root for this extension at all (it belongs to another one) binds nothing
	// either — the policy is the gate, not the stored string.
	other := t.TempDir()
	args, _ = buildExtensionCreateArgs(grantedExtension(other), "kanban-ext-ext-dep-other", "ubuntu:latest", nil, newMountPolicy([]string{grant}))
	if specs := mountSpecs(args); len(specs) != 0 {
		t.Errorf("a grant outside the policy still mounted: %v", specs)
	}

	// No policy at all must be the strictest case, not the loosest.
	args, _ = buildExtensionCreateArgs(grantedExtension(grant), "kanban-ext-ext-dep-nil", "ubuntu:latest", nil, nil)
	if specs := mountSpecs(args); len(specs) != 0 {
		t.Errorf("a nil policy mounted the granted directory: %v", specs)
	}
}

func TestDependencyMountIsTheSameDerivationBothWays(t *testing.T) {
	// The drift this guard exists for: the create appends a mount and an env to the snapshots a later
	// MountsCompatible compares against. If the two ever disagree, a running sandbox is judged
	// compatible with a grant it does not have — and the user's grant does nothing, silently, forever.
	grant := t.TempDir()
	policy := newMountPolicyWithReadOnly([]string{grant}, nil)
	ext := grantedExtension(grant)

	bind := dependencyBind(ext, policy)
	if !bind.OK {
		t.Fatalf("the bind should succeed: %q", bind.Reason)
	}
	if bind.Mount.ContainerPath != "/go/pkg/mod" || bind.Env.Value != "/go/pkg/mod" {
		t.Errorf("bind = %+v, want the declared container path on both halves", bind)
	}

	mounts := withDependency(ext.SandboxConfig.Mounts, bind, bind.Mount)
	if len(mounts) != 1 || mounts[0].ContainerPath != "/go/pkg/mod" {
		t.Fatalf("the mount list = %v", mounts)
	}
	snap := customMountSnapshot(mounts)
	if !hasElem(snap, bind.Mount.HostPath+":/go/pkg/mod:"+bind.Mount.Mode) {
		t.Errorf("the mount snapshot does not record the dependency bind: %v", snap)
	}
	envSnap := envSnapshot(withDependency(ext.SandboxConfig.Envs, bind, bind.Env))
	if !hasElem(envSnap, "GOMODCACHE=/go/pkg/mod") {
		t.Errorf("the env snapshot does not record the assignment: %v", envSnap)
	}

	// The addition is a no-op when nothing is bound, and never mutates the caller's list.
	none := dependencyBind(grantedExtension(""), policy)
	if len(withDependency(ext.SandboxConfig.Mounts, none, none.Mount)) != 0 || len(withDependency(ext.SandboxConfig.Envs, none, none.Env)) != 1 {
		t.Error("an empty bind must not add anything to either list")
	}
	if len(ext.SandboxConfig.Envs) != 1 {
		t.Error("the dependency bind mutated the caller's env list")
	}
}

func TestBuildExtensionCreateArgs_BorrowedMountCollidingWithTheCacheIsDropped(t *testing.T) {
	// Two -v specs on one container path make podman refuse the whole create, which would read as a
	// broken extension rather than as two claims on one path. The borrowed mount gives way because
	// the dependency path is declared and cannot be moved.
	grant := t.TempDir()
	policy := newMountPolicy([]string{grant})
	ext := grantedExtension(grant)
	ext.SandboxConfig.Mounts = []types.SandboxMount{
		{HostPath: grant, ContainerPath: "/go/pkg/mod", Mode: "ro"},
		{HostPath: grant, ContainerPath: "/workspace/share", Mode: "ro"},
	}

	args, _ := buildExtensionCreateArgs(ext, "kanban-ext-ext-dep-collide", "ubuntu:latest", nil, policy)
	specs := mountSpecs(args)
	if got := countPrefix(specs, ":"+"/go/pkg/mod:rprivate,"); got != 1 {
		t.Errorf("the colliding container path appears %d times in %v, want exactly core's mount", got, specs)
	}
	if !hasElem(specs, grant+":/workspace/share:rprivate,ro") {
		t.Errorf("an unrelated mount was dropped along with the collision: %v", specs)
	}
}

func countPrefix(hay []string, needle string) int {
	n := 0
	for _, h := range hay {
		if strings.Contains(h, needle) {
			n++
		}
	}
	return n
}

func TestDesiredDependencyMountNeedsTheManager(t *testing.T) {
	// The exported derivation is what the LSP manager asks for, and it must answer for a nil
	// extension or an ungranted one without touching the store.
	sm := sandboxManagerForTest(t)
	if got := sm.DesiredDependencyMount(nil); got.OK || got.Reason != "" {
		t.Errorf("nil extension = %+v, want nothing to bind and nothing to report", got)
	}
	if got := sm.DesiredDependencyMount(grantedExtension("")); got.OK || got.Reason != "" {
		t.Errorf("no grant = %+v, want nothing to bind and nothing to report", got)
	}

	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(home) == "/" {
		t.Skipf("no home directory outside the app's roots: %v", err)
	}
	home = filepath.Clean(home)
	got := sm.DesiredDependencyMount(grantedExtension(home))
	if !got.OK {
		t.Errorf("a granted directory outside the app's roots must be bindable by its own extension: %+v", got)
	}
}
