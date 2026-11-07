package exec

import (
	"testing"

	"mike/backend/sandbox"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// The builder must carry every field the project configured — that is the failure it prevents, a
// field present in one create path and missing in another.
//
// What it must NOT carry is a host directory the project never asked for. A dependency directory
// granted to an LSP extension has no place in SandboxOpts at all: it is bound read-only into the
// extension's own sandbox and nowhere else (backend/extensions/dependency_mount_test.go pins that),
// and a project that wants a task to fill such a directory declares it in its own Sandbox
// Configuration, where the mount and the toolchain variable are both visible per project.
func TestSandboxCreateOpts_CarriesEveryConfiguredField(t *testing.T) {
	t.Parallel()
	// The builder reads the submodule set off the worktree, so the fixture is a real repository.
	worktree := t.TempDir()
	testgit.Git(t, worktree, "init", "-q")
	testgit.Git(t, worktree, "commit", "-q", "--allow-empty", "-m", "one")

	setup := &taskExecSetup{
		task:         &types.Task{ID: "brave-amber-quartz"},
		project:      &types.Project{ID: "proj-1", Name: "example.com"},
		worktreePath: worktree,
		sandboxOpts: sandbox.SandboxOpts{
			NetworkIsolation: true,
			MemoryLimit:      2048,
			ForwardPorts:     []types.PortForward{{ContainerPort: 3000}},
			SandboxMounts:    []types.SandboxMount{{HostPath: "/tmp/data", ContainerPath: "/data", Mode: "ro"}},
			SandboxEnvs:      []types.SandboxEnv{{Key: "FOO", Value: "bar"}},
			Capabilities:     []string{"NET_RAW"},
		},
	}

	opts, err := newBareTestExecutor(t).sandboxCreateOpts(setup, "localhost/sandbox-example.com", true)
	if err != nil {
		t.Fatalf("sandboxCreateOpts: %v", err)
	}

	if opts.BaseImage != "localhost/sandbox-example.com" {
		t.Errorf("BaseImage = %q, want the image the caller passed in", opts.BaseImage)
	}
	if !opts.WorktreeReadOnly {
		t.Error("WorktreeReadOnly = false, want the value the caller passed in")
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
	// An executor with no agent registry mounts no agent sandbox — nil, not a panic. Every create path
	// goes through this builder, so "nil" here is what an install without agent extensions gets.
	bare, err := newBareTestExecutor(t).sandboxCreateOpts(setup, "img", false)
	if err != nil {
		t.Fatalf("sandboxCreateOpts: %v", err)
	}
	if bare.AgentSandbox != nil {
		t.Errorf("AgentSandbox = %+v, want nil without an agent registry", bare.AgentSandbox)
	}
	// A worktree with no submodules asks for no pins, and that is not an error: it is the ordinary case.
	if len(opts.Submodules) != 0 {
		t.Errorf("Submodules = %v, want none for a worktree with no gitlinks", opts.Submodules)
	}
}
