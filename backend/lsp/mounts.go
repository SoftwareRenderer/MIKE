package lsp

import (
	"path/filepath"
	"strings"

	"mike/backend/extensions"
	"mike/backend/types"
)

// containerWorkspacePath maps a host workspace path to where it lives in the extension sandbox:
// a registered project's mount, or /mnt/<project-dirname>.
func containerWorkspacePath(workspacePath string, projectMounts map[string]string) string {
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		abs = workspacePath
	}
	if container, ok := projectMounts[abs]; ok {
		return container
	}
	return "/mnt/" + filepath.Base(abs)
}

// resolveWorkspaceMounts copies the user's sandbox mounts with the {workspace} placeholder
// replaced by the opened workspace's container path, so one configured mount (e.g. node_modules)
// targets the right worktree without naming it.
func resolveWorkspaceMounts(mounts []types.SandboxMount, workspacePath string, projectMounts map[string]string) []types.SandboxMount {
	if len(mounts) == 0 {
		return nil
	}
	ws := containerWorkspacePath(workspacePath, projectMounts)
	resolved := make([]types.SandboxMount, len(mounts))
	for i, mo := range mounts {
		mo.HostPath = strings.ReplaceAll(mo.HostPath, "{workspace}", ws)
		mo.ContainerPath = strings.ReplaceAll(mo.ContainerPath, "{workspace}", ws)
		resolved[i] = mo
	}
	return resolved
}

// isReservedContainerPath reports whether core already mounts this container path itself — today,
// the granted dependency directory. Borrowed mounts have to check it: two -v specs on one container
// path make podman refuse the whole create, so the cost of missing one is an extension that will
// not start at all.
func isReservedContainerPath(containerPath string, reserved []types.SandboxMount) bool {
	for _, r := range reserved {
		if r.ContainerPath == containerPath {
			return true
		}
	}
	return false
}

// shadowsToolState is the extensions rule of the same name, under its old local name — the rule
// has one home (backend/extensions/mount_policy.go) because both things that mount read-only,
// a borrowed Sandbox Configuration mount and a declared dependency_cache, have to agree on what
// counts as the server's own state.
var shadowsToolState = extensions.ShadowsToolState
