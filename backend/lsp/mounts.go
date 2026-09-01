package lsp

import (
	"path/filepath"
	"strings"

	"kanban/backend/types"
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

// shadowsToolState reports whether a borrowed Sandbox Configuration mount would cover the
// language server's own writable state — its cache, or the container's tmpfs.
//
// Borrowed mounts are forced read-only, and a server with a read-only cache publishes nothing
// about a file it cannot load, so a broken file looks clean.
func shadowsToolState(containerPath string) bool {
	// Only the tmpfs roots themselves; a read-only mount over /tmp makes the whole scratch space
	// unusable, while a subtree of it is fine.
	switch containerPath {
	case "/tmp", "/var/tmp":
		return true
	}
	// A .cache directory of the container's own user (/root/.cache, /home/node/.cache) or
	// anything under it. Depth-bounded so a project's own .cache, deep under /mnt, stays borrowed.
	parts := strings.Split(strings.TrimPrefix(containerPath, "/"), "/")
	for i, part := range parts {
		if i > 2 {
			break
		}
		if part == ".cache" {
			return true
		}
	}
	return false
}
