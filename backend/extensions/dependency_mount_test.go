package extensions

// The mount and the assignment a granted dependency directory contributes to a container, and the
// two properties that make it safe rather than merely convenient: what a create produces is exactly
// what the compatibility check will later ask for (or a grant never takes effect), and an
// unmountable grant is reported and skipped instead of becoming a container that will not build.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
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

func grantedExtension(hostPath string, writable bool) *Extension {
	readOnly := !writable
	ext := &Extension{ID: "ext-dep-mount", Name: "gopls", Type: "lsp"}
	ext.SandboxConfig = SandboxConfig{
		Envs:               []types.SandboxEnv{{Key: "GOCACHE", Value: "/var/cache/gopls/go-build"}},
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE", ReadOnly: &readOnly},
		DependencyHostPath: hostPath,
	}
	return ext
}

func TestBuildExtensionCreateArgs_MountsTheGrantedDependencyDirectory(t *testing.T) {
	grant := t.TempDir()
	policy := newMountPolicyWithReadOnly([]string{grant}, []string{grant})

	args, _ := buildExtensionCreateArgs(grantedExtension(grant, false), "kanban-ext-ext-dep-mount", "ubuntu:latest", nil, policy)
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

func TestBuildExtensionCreateArgs_WritableDeclarationOnlyWhenTheAppAllowsIt(t *testing.T) {
	// Two conditions, both required: the manifest asked to write, and the path is not one the app
	// pins read-only. Either alone leaves the mount read-only.
	grant := t.TempDir()

	free := newMountPolicy([]string{grant})
	args, _ := buildExtensionCreateArgs(grantedExtension(grant, true), "kanban-ext-ext-dep-rw", "ubuntu:latest", nil, free)
	if !hasElem(mountSpecs(args), grant+":/go/pkg/mod:rprivate,rw") {
		t.Errorf("a writable declaration on an app-owned path should mount read-write: %v", mountSpecs(args))
	}

	pinned := newMountPolicyWithReadOnly([]string{grant}, []string{grant})
	args, _ = buildExtensionCreateArgs(grantedExtension(grant, true), "kanban-ext-ext-dep-rw", "ubuntu:latest", nil, pinned)
	if !hasElem(mountSpecs(args), grant+":/go/pkg/mod:rprivate,ro") {
		t.Errorf("a pinned root must stay read-only whatever the declaration asks: %v", mountSpecs(args))
	}
}

func TestBuildExtensionCreateArgs_UnmountableGrantIsReportedAndSkipped(t *testing.T) {
	grant := t.TempDir()
	missing := filepath.Join(grant, "removed-after-granting")

	// The stored grant still points at it (the directory was deleted afterwards). The extension has
	// to start anyway: its own environment check will say the project cannot be resolved, which is
	// true, whereas refusing to build the container would leave the editor with nothing at all.
	ext := grantedExtension(missing, false)
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
	args, _ = buildExtensionCreateArgs(grantedExtension(other, false), "kanban-ext-ext-dep-other", "ubuntu:latest", nil, newMountPolicy([]string{grant}))
	if specs := mountSpecs(args); len(specs) != 0 {
		t.Errorf("a grant outside the policy still mounted: %v", specs)
	}

	// No policy at all must be the strictest case, not the loosest.
	args, _ = buildExtensionCreateArgs(grantedExtension(grant, false), "kanban-ext-ext-dep-nil", "ubuntu:latest", nil, nil)
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
	ext := grantedExtension(grant, false)

	bind := dependencyBind(ext, policy)
	if !bind.OK {
		t.Fatalf("the bind should succeed: %q", bind.Reason)
	}
	if bind.Mount.ContainerPath != "/go/pkg/mod" || bind.Env.Value != "/go/pkg/mod" {
		t.Errorf("bind = %+v, want the declared container path on both halves", bind)
	}

	mounts := withDependencyMount(ext.SandboxConfig.Mounts, bind)
	if len(mounts) != 1 || mounts[0].ContainerPath != "/go/pkg/mod" {
		t.Fatalf("withDependencyMount = %v", mounts)
	}
	snap := customMountSnapshot(mounts)
	if !hasElem(snap, bind.Mount.HostPath+":/go/pkg/mod:"+bind.Mount.Mode) {
		t.Errorf("the mount snapshot does not record the dependency bind: %v", snap)
	}
	envSnap := envSnapshot(withDependencyEnv(ext.SandboxConfig.Envs, bind))
	if !hasElem(envSnap, "GOMODCACHE=/go/pkg/mod") {
		t.Errorf("the env snapshot does not record the assignment: %v", envSnap)
	}

	// Both helpers are no-ops when nothing is bound, and never mutate the caller's list.
	none := dependencyBind(grantedExtension("", false), policy)
	if len(withDependencyMount(ext.SandboxConfig.Mounts, none)) != 0 || len(withDependencyEnv(ext.SandboxConfig.Envs, none)) != 1 {
		t.Error("an empty bind must not add anything to either list")
	}
	if len(ext.SandboxConfig.Envs) != 1 {
		t.Error("withDependencyEnv mutated the caller's env list")
	}
}

func TestBuildExtensionCreateArgs_BorrowedMountCollidingWithTheCacheIsDropped(t *testing.T) {
	// Two -v specs on one container path make podman refuse the whole create, which would read as a
	// broken extension rather than as two claims on one path. The borrowed mount gives way because
	// the dependency path is declared and cannot be moved.
	grant := t.TempDir()
	policy := newMountPolicy([]string{grant})
	ext := grantedExtension(grant, false)
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
	if got := sm.DesiredDependencyMount(grantedExtension("", false)); got.OK || got.Reason != "" {
		t.Errorf("no grant = %+v, want nothing to bind and nothing to report", got)
	}

	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(home) == "/" {
		t.Skipf("no home directory outside the app's roots: %v", err)
	}
	home = filepath.Clean(home)
	got := sm.DesiredDependencyMount(grantedExtension(home, false))
	if !got.OK {
		t.Errorf("a granted directory outside the app's roots must be bindable by its own extension: %+v", got)
	}
}
