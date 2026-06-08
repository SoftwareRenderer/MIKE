/**
 * Convert an absolute path to a home-relative path with `~/` prefix.
 * e.g. `/home/mwren/projects/hasc` → `~/projects/hasc`
 * Falls back to the basename if the path doesn't match a home directory pattern.
 */
export function getHomeRelativePath(absolutePath: string): string {
    if (!absolutePath) return '';
    const match = absolutePath.match(/^\/home\/[^/]+\/(.+)$/);
    if (match) return '~/' + match[1];
    return absolutePath.split('/').pop() || absolutePath;
}

/**
 * Compute a display path for a task's worktree directory.
 * The backend sends an absolute workspaceDir (e.g. `/home/mwren/projects/kanban/worktrees`).
 * Appends the taskId and converts to a `~/` prefixed display path.
 * e.g. workspaceDir=`/home/mwren/projects/kanban/worktrees`, taskId=`abc` → `~/projects/kanban/worktrees/abc`
 */
export function getWorktreeDisplayPath(workspaceDir: string, taskId: string): string {
    if (!workspaceDir || !taskId) return '';
    const worktreePath = workspaceDir.replace(/\/+$/, '') + '/' + taskId;
    return toHomeRelative(worktreePath);
}

/**
 * Convert an absolute path to a `~/` prefixed display path.
 * e.g. `/home/mwren/projects/kanban/worktrees/abc` → `~/projects/kanban/worktrees/abc`
 * Falls back to the raw path if it doesn't match a home directory pattern.
 */
function toHomeRelative(path: string): string {
    const match = path.match(/^\/home\/[^/]+\/(.+)$/);
    if (match) return '~/' + match[1];
    return path;
}
