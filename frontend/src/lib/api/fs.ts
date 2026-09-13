import { API_BASE } from './helpers';

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
