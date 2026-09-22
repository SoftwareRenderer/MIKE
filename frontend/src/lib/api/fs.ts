import { API_BASE, apiFetch, apiError } from './helpers';
import { t } from '$lib/i18n';

/** One row of a `GET /api/fs` listing. */
export interface FsEntry {
    name: string;
    isDir: boolean;
    path: string;
    isGitRepo: boolean;
}

/** What `GET /api/fs` answers for one directory. */
export interface FsListing {
    path: string;
    isGitRepo: boolean;
    entries: FsEntry[];
}

/**
 * List one directory, inside a project or a task's worktree when `project`/`task` name the scope; a
 * refusal carries the server's own sentence, which some callers compare against.
 */
export async function listFsDirectory(params: {
    path: string;
    /** Include files alongside directories; without it only directories are listed. */
    files?: boolean;
    project?: string;
    task?: string;
}): Promise<FsListing> {
    const query = new URLSearchParams({ path: params.path });
    if (params.files) query.set('files', '1');
    if (params.project) query.set('project', params.project);
    if (params.task) query.set('task', params.task);
    const res = await apiFetch(`${API_BASE}/fs?${query.toString()}`);
    if (!res.ok) throw await apiError(res, t('explorer.shared.errReadDir'));
    return await res.json();
}

/**
 * The URL `GET /api/fs/raw` answers with one image's bytes. It is a URL rather than a fetch because the
 * caller puts it in an `<img>`: the browser then loads it itself and offers "Open image in new tab".
 * `path` is relative to the repository root; `ref` names a commit, omit it for the file on disk.
 */
export function rawFileURL(params: { project: string; task?: string; path: string; ref?: string }): string {
    const query = new URLSearchParams({ project: params.project, path: params.path });
    if (params.task) query.set('task', params.task);
    if (params.ref) query.set('ref', params.ref);
    return `${API_BASE}/fs/raw?${query.toString()}`;
}
