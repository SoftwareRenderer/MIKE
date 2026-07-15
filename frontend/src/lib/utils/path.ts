/**
 * Convert an absolute path to a `~/` prefixed display path.
 * Falls back to the basename if it doesn't match a home directory pattern.
 */
export function toHomeRelative(path: string): string {
    if (!path) return '';
    const match = path.match(/^\/home\/[^/]+\/(.+)$/);
    if (match) return '~/' + match[1];
    return path.split('/').pop() || path;
}

/**
 * Compute a display path for a task's worktree directory.
 * The backend sends an absolute workspaceDir (e.g. `/home/projects/kanban/worktrees`).
 * Appends projectId/taskId and converts to a `~/` prefixed display path.
 * e.g. workspaceDir=`/home/projects/kanban/worktrees`, projectId=`my-proj`, taskId=`abc`
 *   → `~/projects/kanban/worktrees/my-proj/abc`
 */
export function getWorktreeDisplayPath(workspaceDir: string, projectId: string, taskId: string): string {
    if (!workspaceDir || !projectId || !taskId) return '';
    const worktreePath = workspaceDir.replace(/\/+$/, '') + '/' + projectId + '/' + taskId;
    return toHomeRelative(worktreePath);
}
