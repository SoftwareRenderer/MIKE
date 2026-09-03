package exec

import (
	"testing"

	"kanban/backend/lsp"
	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/types"
)

// depCacheLSP answers only the dependency question; the rest of the interface is a nil embedded
// interface, which panics loudly if this test ever starts needing more of it.
type depCacheLSP struct {
	lspManager
	caches []lsp.DependencyCache
}

func (f depCacheLSP) DependencyCaches() []lsp.DependencyCache { return f.caches }

// The builder must carry every field the project configured — that is the failure it prevents, a
// field present in one create path and missing in another.
func TestSandboxCreateOpts_CarriesEveryConfiguredField(t *testing.T) {
	setup := &taskExecSetup{
		task:    &types.Task{ID: "brave-amber-quartz"},
		project: &types.Project{ID: "proj-1", Name: "example.com"},
		sandboxOpts: kanban_sandbox.SandboxOpts{
			NetworkIsolation: true,
			MemoryLimit:      2048,
			ForwardPorts:     []types.PortForward{{ContainerPort: 3000}},
			SandboxMounts:    []types.SandboxMount{{HostPath: "/tmp/data", ContainerPath: "/data", Mode: "ro"}},
			SandboxEnvs:      []types.SandboxEnv{{Key: "FOO", Value: "bar"}},
			Capabilities:     []string{"NET_RAW"},
		},
	}

	opts := (&TaskExecutor{}).sandboxCreateOpts(setup, "localhost/sandbox-example.com", true)

	if opts.BaseImage != "localhost/sandbox-example.com" {
		t.Errorf("BaseImage = %q, want the image the caller passed in", opts.BaseImage)
	}
	if !opts.PlanMode {
		t.Error("PlanMode = false, want the mode the caller passed in")
	}
	if !opts.NetworkIsolation {
		t.Error("NetworkIsolation = false, want the project's setting")
	}
	if opts.MemoryLimit != 2048 {
		t.Errorf("MemoryLimit = %d, want the project's limit", opts.MemoryLimit)
	}
	if len(opts.ForwardPorts) != 1 || opts.ForwardPorts[0].ContainerPort != 3000 {
		t.Errorf("ForwardPorts = %+v, want the project's forwarded ports", opts.ForwardPorts)
	}
	if len(opts.SandboxMounts) != 1 || opts.SandboxMounts[0].ContainerPath != "/data" {
		t.Errorf("SandboxMounts = %+v, want the project's mounts", opts.SandboxMounts)
	}
	if len(opts.SandboxEnvs) != 1 || opts.SandboxEnvs[0].Key != "FOO" {
		t.Errorf("SandboxEnvs = %+v, want the project's envs", opts.SandboxEnvs)
	}
	if len(opts.Capabilities) != 1 || opts.Capabilities[0] != "NET_RAW" {
		t.Errorf("Capabilities = %v, want the project's capabilities", opts.Capabilities)
	}
	// An executor with no agent registry mounts no agent sandbox — nil, not a panic.
	if got := (&TaskExecutor{}).sandboxCreateOpts(setup, "img", false).AgentSandbox; got != nil {
		t.Errorf("AgentSandbox = %+v, want nil without an agent registry", got)
	}
	// Nor does an executor with no LSP manager invent dependency mounts. Every create path goes
	// through this builder, so "nothing" here is what an install without LSP extensions gets.
	if opts := (&TaskExecutor{}).sandboxCreateOpts(setup, "img", false); len(opts.DependencyMounts) != 0 || len(opts.DependencyEnvs) != 0 {
		t.Errorf("without an LSP manager: mounts %+v envs %+v", opts.DependencyMounts, opts.DependencyEnvs)
	}
}

// TestSandboxCreateOpts_CarriesDependencyCaches is the wiring between the two sandboxes: what the
// enabled LSP extensions were granted reaches the task container, read-write, with the variable
// pointing at the container path rather than the host one. It lives at this level because the
// alternative failure is silent — a cache mounted in the extension's sandbox and never in the task's
// is a dependency directory that can only ever be read, and nothing but a cold toolchain notices.
func TestSandboxCreateOpts_CarriesDependencyCaches(t *testing.T) {
	setup := &taskExecSetup{
		task:    &types.Task{ID: "brave-amber-quartz"},
		project: &types.Project{ID: "proj-1", Name: "example.com"},
	}
	te := &TaskExecutor{lspMgr: depCacheLSP{caches: []lsp.DependencyCache{
		{HostPath: "/home/dev/go/pkg/mod", ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		{HostPath: "/opt/cargo", ContainerPath: "/usr/local/cargo"}, // lost the variable, keeps the mount
		{HostPath: "", ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
	}}}

	opts := te.sandboxCreateOpts(setup, "img", true)
	if len(opts.DependencyMounts) != 2 {
		t.Fatalf("DependencyMounts = %+v, want the two complete entries", opts.DependencyMounts)
	}
	// The mode is the task sandbox's decision, not the extension's: this is the container that fills
	// the cache. Anything else here would make the grant read-only for the toolchain that warms it.
	for _, m := range opts.DependencyMounts {
		if m.Mode != "rw" {
			t.Errorf("dependency mount %s has mode %q, want rw", m.ContainerPath, m.Mode)
		}
	}
	if opts.DependencyMounts[0].HostPath != "/home/dev/go/pkg/mod" || opts.DependencyMounts[0].ContainerPath != "/go/pkg/mod" {
		t.Errorf("first mount = %+v", opts.DependencyMounts[0])
	}
	if len(opts.DependencyEnvs) != 1 || opts.DependencyEnvs[0].Key != "GOMODCACHE" {
		t.Fatalf("DependencyEnvs = %+v, want only the assignment the extension won", opts.DependencyEnvs)
	}
	// The assignment names the container path: the host path means nothing to a process inside the
	// sandbox, and telling it where the host keeps things is information it has no use for.
	if len(opts.DependencyEnvs) != 1 || opts.DependencyEnvs[0].Value != "/go/pkg/mod" {
		t.Errorf("DependencyEnvs = %+v, want the value to be the container path", opts.DependencyEnvs)
	}
}
