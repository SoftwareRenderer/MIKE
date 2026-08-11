package lsp

import (
	"path/filepath"
	"strings"

	"kanban/backend/types"
)

// containerWorkspacePath converts a host workspace path to its path inside the
// extension sandbox. Mounted projects live under /mnt/<project-dirname>.
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

// resolveWorkspaceMounts returns a copy of the user-configured sandbox mounts with
// the {workspace} placeholder replaced by the container path of the workspace being
// opened. This lets a single mount configured via Sandbox Configuration (e.g. a
// node_modules mount) target the correct per-worktree location without hardcoding
// the workspace name or a project subpath in the extension.
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
