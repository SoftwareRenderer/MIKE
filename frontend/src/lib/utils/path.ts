/**
 * Convert an absolute path to a `~/` prefixed display path.
 * Falls back to the basename if it doesn't match a home directory pattern.
 */
export function toHomeRelative(path: string): string {
    if (!path) return '';
    const match = path.match(/^\/home\/[^/]+\/(.+)$/);
    if (match) return '~/' + match[1];
    return baseName(path) || path;
}

/**
 * The folder containing `path`, or '' when there is none.
 *
 * The one place a path is cut at its last separator; the explorers' URL target, the picker's
 * "folder above" row and the paste destination all mean the same cut.
 */
export function parentDir(path: string): string {
    const cut = path.lastIndexOf('/');
    if (cut < 0) return '';
    return cut === 0 ? '/' : path.slice(0, cut);
}

/** The last segment of `path`, case kept. `''` for `''`; a bare name answers itself. */
export function baseName(path: string): string {
    const cut = path.lastIndexOf('/');
    return cut < 0 ? path : path.slice(cut + 1);
}

/** `baseName` as a folder label: a path with no last segment is the root, and the root is `/`. */
export function folderName(path: string): string {
    return baseName(path) || '/';
}

/**
 * The container-side root a sandboxed project's tree appears under — the mirror of
 * `sandbox.MountRoot` (`backend/sandbox/sandbox.go`), which is what the runtime mounts and what an
 * agent therefore reports in every path it prints. Anything here that guesses the mount from a name
 * must agree with that constant and with `MountRoot + "/" + project.Name`, or a path the agent
 * reported stops resolving in the explorer.
 */
const SANDBOX_MOUNT_ROOT = '/home/sandbox';

/** The mount a container shows for the tree named `name`; `''` when there is no name to mount. */
export function sandboxMountPath(name: string): string {
    return name ? `${SANDBOX_MOUNT_ROOT}/${name}` : '';
}

/** The mount of the project whose host checkout lives at `hostPath` — the container names it after that directory. */
export function sandboxMountFromPath(hostPath: string): string {
    return sandboxMountPath(hostPath.split('/').filter(Boolean).pop() ?? '');
}

/** Whether `path` is inside the mount root, so it names the container's view of a tree. */
export function isSandboxMountPath(path: string): boolean {
    return path.startsWith(SANDBOX_MOUNT_ROOT + '/');
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

/** Whether `path` is `root` or inside it. The separator matters: `/a/bc` is not under `/a/b`. */
export function isUnderPath(path: string, root: string): boolean {
    if (!path || !root) return false;
    return path === root || path.startsWith(root.endsWith('/') ? root : root + '/');
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
