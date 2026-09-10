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
 * Resolve a tool call's file path into the path the explorer addresses that file by.
 *
 * A tool argument names its file relative to the task root, while the explorer's editor
 * opens files by the absolute path of its own root — the sandbox mount when the project
 * is sandboxed, the task worktree otherwise. An absolute argument is already in that
 * space (a sandboxed agent writes `/home/sandbox/<project>/…`, and that is exactly what
 * the explorer lists), so it passes through untouched.
 */
export function toExplorerPath(path: string, rootPath: string): string {
    if (!path || path.startsWith('/') || !rootPath) return path;
    const rel = path.startsWith('./') ? path.slice(2) : path;
    return rootPath.replace(/\/+$/, '') + '/' + rel;
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
