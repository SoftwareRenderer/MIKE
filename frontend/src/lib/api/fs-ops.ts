import { apiError, apiFetch } from './helpers';
import { t } from '$lib/i18n';

// Paths are what the listing gave the browser: container paths for a sandboxed project, host paths
// otherwise. The server translates them and never returns a host path.

/** Which tree the operation runs in: a project, plus its task's worktree when there is one. */
export interface FsOpsScope {
    projectId: string;
    taskId?: string;
}

/** One path of the answer. `from` is the path as the caller sent it; `to` is where it landed. */
export interface FsOpsItem {
    from: string;
    to?: string;
    /** The name was taken, so the item landed under a different one. */
    renamed?: boolean;
    /** Set when this item failed, in the server's words. */
    reason?: string;
}

export interface FsOpsResult {
    done: FsOpsItem[];
    failed: FsOpsItem[];
}

// Written out because an archgate rule checks that a caller reaches each registered route.
const FS_OPS_ROUTES = {
    delete: '/api/fs/delete',
    copy: '/api/fs/copy',
    move: '/api/fs/move',
    rename: '/api/fs/rename',
} as const;

type FsOpsRoute = keyof typeof FS_OPS_ROUTES;

async function postFsOps<T>(route: FsOpsRoute, scope: FsOpsScope, body: Record<string, unknown>, fallback: string): Promise<T> {
    const params = new URLSearchParams({ project: scope.projectId });
    if (scope.taskId) params.set('task', scope.taskId);
    const res = await apiFetch(`${FS_OPS_ROUTES[route]}?${params.toString()}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, fallback);
    return res.json();
}

export function deleteFsPaths(scope: FsOpsScope, paths: string[]): Promise<FsOpsResult> {
    return postFsOps<FsOpsResult>('delete', scope, { paths }, t('explorer.shared.errDeleteFiles'));
}

/** Copy every path into `dest`; a name already there gets a free one. */
export function copyFsPaths(scope: FsOpsScope, paths: string[], dest: string): Promise<FsOpsResult> {
    return postFsOps<FsOpsResult>('copy', scope, { paths, dest }, t('explorer.shared.errCopyFiles'));
}

export function moveFsPaths(scope: FsOpsScope, paths: string[], dest: string): Promise<FsOpsResult> {
    return postFsOps<FsOpsResult>('move', scope, { paths, dest }, t('explorer.shared.errMoveFiles'));
}

export async function renameFsPath(scope: FsOpsScope, path: string, name: string): Promise<string> {
    const res = await postFsOps<{ path: string }>('rename', scope, { path, name }, t('explorer.shared.errRenameFile'));
    return res.path;
}
