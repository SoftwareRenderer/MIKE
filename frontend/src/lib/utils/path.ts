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
 * The worktree lives at `workspaceDir/taskId`. Returns it with `~/` prefix if possible.
 * e.g. workspaceDir=`./worktrees`, taskId=`abc` → `~/projects/kanban/worktrees/abc`
 *        workspaceDir=`/home/mwren/projects/kanban/worktrees`, taskId=`abc` → `~/projects/kanban/worktrees/abc`
 */
export function getWorktreeDisplayPath(workspaceDir: string, taskId: string): string {
    if (!workspaceDir || !taskId) return '';
    const normalized = normalizeWorkspaceDir(workspaceDir);
    const worktreePath = normalized.endsWith('/') ? normalized + taskId : normalized + '/' + taskId;
    return ensureTildePrefix(worktreePath);
}

/**
 * Normalize a workspace directory path.
 * - `./worktrees` → `worktrees`
 * - `/home/mwren/projects/kanban/worktrees` → `~/projects/kanban/worktrees`
 * - `worktrees` → `worktrees`
 */
function normalizeWorkspaceDir(path: string): string {
    // Strip leading ./
    let p = path.replace(/^\.\//, '');
    // If absolute under /home/user/, convert to ~/
    const match = p.match(/^\/home\/[^/]+\/(.+)$/);
    if (match) return '~/' + match[1];
    return p;
}

/**
 * Ensure a path has a ~/ prefix. If it already starts with ~/, return as-is.
 * If it's a relative path (no leading /), prepend ~/ to make it cd-navigable.
 */
function ensureTildePrefix(path: string): string {
    if (path.startsWith('~/')) return path;
    if (!path.startsWith('/')) return '~/kanban/' + path;
    const match = path.match(/^\/home\/[^/]+\/(.+)$/);
    if (match) return '~/' + match[1];
    return path;
}
