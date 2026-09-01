package extensions

import (
	"testing"

	"kanban/backend/types"
)

// TestMergeManifestSandboxAdditionsAppliesANewerManifestsDeclaration is the reason the merge
// exists: an installed extension's SandboxConfig is derived from the manifest at install, and the
// update path swaps in the new manifest without re-reading its sandbox block. A gopls updated to a
// commit that moves GOCACHE out of /root/.cache — the path a project's Sandbox Configuration mount
// can shadow read-only, which is what made gopls report nothing at all about a file that does not
// parse — kept running with the cache path stored when it was first installed.
func TestMergeManifestSandboxAdditionsAppliesANewerManifestsDeclaration(t *testing.T) {
	stored := SandboxConfig{NetworkIsolation: true, MemoryLimit: 512}
	declared := &SandboxConfig{Envs: []types.SandboxEnv{
		{Key: "XDG_CACHE_HOME", Value: "/var/cache/gopls"},
		{Key: "GOCACHE", Value: "/var/cache/gopls/go-build"},
	}}

	got := mergeManifestSandboxAdditions(stored, declared)

	if len(got.Envs) != 2 {
		t.Fatalf("envs = %v, want both declarations from the newer manifest", got.Envs)
	}
	if got.Envs[0].Key != "XDG_CACHE_HOME" || got.Envs[1].Key != "GOCACHE" {
		t.Errorf("envs = %v, want them in the order the manifest declares", got.Envs)
	}
	// The memory limit and network isolation belong to whoever configured the extension in
	// Settings, not to the upstream package.
	if got.MemoryLimit != 512 {
		t.Errorf("memory limit = %d, want the stored 512 preserved", got.MemoryLimit)
	}
	if !got.NetworkIsolation {
		t.Error("network isolation = false, want the stored value preserved")
	}
}

// TestMergeManifestSandboxAdditionsKeepsUserEdits: everything merged here is also editable through
// PUT /api/extensions/{id}, so a stored value outranks the manifest and nothing is duplicated —
// podman would take the last -e and quietly let the manifest overrule the user.
func TestMergeManifestSandboxAdditionsKeepsUserEdits(t *testing.T) {
	stored := SandboxConfig{
		Envs:         []types.SandboxEnv{{Key: "GOCACHE", Value: "/srv/my-gocache"}},
		Mounts:       []types.SandboxMount{{HostPath: "/srv/mine", ContainerPath: "/data", Mode: "ro"}},
		ForwardPorts: []types.PortForward{{ContainerPort: 8080}},
		Capabilities: []string{"CAP_NET_RAW"},
	}
	declared := &SandboxConfig{
		Envs: []types.SandboxEnv{
			{Key: "GOCACHE", Value: "/var/cache/gopls/go-build"},
			{Key: "XDG_CACHE_HOME", Value: "/var/cache/gopls"},
		},
		Mounts: []types.SandboxMount{
			{HostPath: "/upstream", ContainerPath: "/data", Mode: "rw"},
			{HostPath: "/upstream/other", ContainerPath: "/other", Mode: "ro"},
		},
		ForwardPorts: []types.PortForward{{ContainerPort: 8080}, {ContainerPort: 9000}},
		Capabilities: []string{"CAP_NET_RAW", "CAP_CHOWN"},
	}

	got := mergeManifestSandboxAdditions(stored, declared)

	if len(got.Envs) != 2 || got.Envs[0].Value != "/srv/my-gocache" {
		t.Errorf("envs = %v, want the stored GOCACHE value kept and only the new key added", got.Envs)
	}
	if len(got.Mounts) != 2 || got.Mounts[0].HostPath != "/srv/mine" {
		t.Errorf("mounts = %v, want the stored /data mount kept and only /other added", got.Mounts)
	}
	if len(got.ForwardPorts) != 2 || got.ForwardPorts[0].ContainerPort != 8080 || got.ForwardPorts[1].ContainerPort != 9000 {
		t.Errorf("ports = %v, want the stored 8080 once and 9000 added", got.ForwardPorts)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "CAP_NET_RAW" {
		t.Errorf("capabilities = %v, want CAP_NET_RAW once and CAP_CHOWN added", got.Capabilities)
	}
}

// TestMergeManifestSandboxAdditionsIsIdempotentAndNilSafe: the update path can run against the same
// manifest repeatedly (a user pressing Update again), and non-LSP extensions declare no sandbox at
// all. Neither may grow the config or panic.
func TestMergeManifestSandboxAdditionsIsIdempotentAndNilSafe(t *testing.T) {
	declared := &SandboxConfig{
		Envs:         []types.SandboxEnv{{Key: "GOCACHE", Value: "/var/cache/gopls/go-build"}},
		Mounts:       []types.SandboxMount{{HostPath: "/upstream", ContainerPath: "/data", Mode: "ro"}},
		Capabilities: []string{"CAP_CHOWN"},
	}
	once := mergeManifestSandboxAdditions(SandboxConfig{}, declared)
	twice := mergeManifestSandboxAdditions(once, declared)
	if len(twice.Envs) != 1 || len(twice.Mounts) != 1 || len(twice.Capabilities) != 1 {
		t.Errorf("second merge grew the config: envs=%v mounts=%v caps=%v", twice.Envs, twice.Mounts, twice.Capabilities)
	}

	if got := mergeManifestSandboxAdditions(once, nil); len(got.Envs) != 1 {
		t.Errorf("nil declaration changed the stored config: %v", got.Envs)
	}
}
