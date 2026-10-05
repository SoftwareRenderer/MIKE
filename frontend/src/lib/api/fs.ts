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

/** The `project`/`task` pair a scoped file request carries; with neither the host jail decides. */
function scopeQuery(scope: { project?: string; task?: string }): URLSearchParams {
    const query = new URLSearchParams();
    if (scope.project) query.set('project', scope.project);
    if (scope.task) query.set('task', scope.task);
    return query;
}

/** One POST to one `/api/fs` route, answered by the server's own sentence when it refuses. */
async function postFsJSON(route: string, query: URLSearchParams, body: Record<string, unknown>, fallback: string): Promise<void> {
    const res = await apiFetch(`${route}?${query.toString()}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, fallback);
}

/** One file's bytes, as `GET /api/fs/read` answers them. */
export interface FsFileRead {
    content: string;
    /** Diff-vs-HEAD markers keyed by working-tree line number (1-based). Absent for a `ref` read, an
     *  untracked file and a repository with no commits, which all mean "no markers", not "no changes". */
    gutter_markers?: Record<string, string[]>;
}

/**
 * A read the server refused, carrying its status, because two refusals are answers rather than faults:
 * 413 is "larger than an editor shows" and 404 is "gone", and only the caller knows which of its states
 * either is. Any other status arrives as the server's own sentence.
 */
export class FsReadRefused extends Error {
    constructor(readonly status: number, message: string) {
        super(message);
    }
}

/**
 * Read one file. The size cap is the server's (`maxFSReadBytes`) and it answers 413 from the stat, so a
 * 4 GB file costs one probe rather than a read; a caller that shows files has to treat that as a state.
 */
export async function readFsFile(params: {
    path: string;
    project: string;
    /** Task whose worktree holds the file; omit for the project's own checkout. */
    task?: string;
    /** Read what a commit holds instead of what the working tree holds. */
    ref?: string;
}): Promise<FsFileRead> {
    const query = new URLSearchParams({ path: params.path, project: params.project });
    if (params.task) query.set('task', params.task);
    if (params.ref) query.set('ref', params.ref);
    const res = await apiFetch(`${API_BASE}/fs/read?${query.toString()}`);
    if (!res.ok) throw new FsReadRefused(res.status, (await apiError(res, t('editor.readFailed'))).message);
    return await res.json();
}

/**
 * Write one file. `fallback` is the caller's own sentence for a refusal that carries none, so a save and
 * a file creation say what the user was doing rather than what the code was calling.
 */
export async function writeFsFile(params: {
    project: string;
    task?: string;
    path: string;
    content: string;
    fallback: string;
}): Promise<void> {
    await postFsJSON(`${API_BASE}/fs/write`, scopeQuery(params), { path: params.path, content: params.content }, params.fallback);
}

/**
 * Create one directory. With `project` it is jailed to that tree; with none this is the picker's
 * host-side flow, where the directories declared at startup are the jail.
 */
export async function createFsDirectory(params: {
    path: string;
    project?: string;
    task?: string;
}): Promise<void> {
    await postFsJSON(`${API_BASE}/fs/mkdir`, scopeQuery(params), { path: params.path }, t('explorer.shared.errCreateFolder'));
}

/** Run `git init` in one directory, under the same two jails as `createFsDirectory`. */
export async function initGitRepository(params: { path: string; project?: string }): Promise<void> {
    await postFsJSON(`${API_BASE}/fs/init-git`, scopeQuery(params), { path: params.path }, t('explorer.shared.errInitGit'));
}

/** One hit of `GET /api/fs/search`; `line` and `text` are set only for a content search. */
export interface FsSearchResult {
    path: string;
    line?: number;
    text?: string;
}

/** What `GET /api/fs/search` answers: the tree it searched, and the hits inside it. */
export interface FsSearchAnswer {
    root: string;
    results: FsSearchResult[];
    /** True when matches exist beyond what this answer holds. */
    truncated?: boolean;
}

/**
 * Search filenames, or file contents with `content`. The tree searched is the project's checkout, or the
 * task's worktree when `task` names one, and the answer's `root` is that directory.
 */
export async function searchFsPaths(params: {
    query: string;
    project: string;
    task?: string;
    /** Search file contents rather than filenames. */
    content?: boolean;
    matchCase?: boolean;
    wholeWord?: boolean;
    regex?: boolean;
    /** Cancels a search that further typing has made irrelevant. */
    signal?: AbortSignal;
}): Promise<FsSearchAnswer> {
    const query = new URLSearchParams({ q: params.query, project: params.project });
    if (params.task) query.set('task', params.task);
    if (params.content) query.set('content', '1');
    if (params.matchCase) query.set('case', '1');
    if (params.wholeWord) query.set('word', '1');
    if (params.regex) query.set('regex', '1');
    const res = await apiFetch(`${API_BASE}/fs/search?${query.toString()}`, { signal: params.signal });
    if (!res.ok) throw await apiError(res, t('editor.searchFailed', { status: res.status }));
    return await res.json();
}

/**
 * The server's own home directory: the project picker opens on it, and Settings → Extensions suggests it
 * for a dependency directory.
 */
export async function getHomeDirectory(): Promise<string> {
    const res = await apiFetch(`${API_BASE}/home`);
    if (!res.ok) throw await apiError(res, t('api.readHomeDirectory'));
    const data = await res.json();
    return typeof data?.home === 'string' ? data.home : '';
}

/** The directory a host browser opens on when the server's home cannot be read. */
const HOST_HOME_FALLBACK = '/home';

/**
 * The directory a host browser opens on: the server's home, which is also the root the no-project
 * file endpoints are jailed to, so a picker that opened anywhere else would open on a refusal.
 * Never rejects — a browser that cannot ask the server still has to open somewhere.
 */
export async function resolveHomeDirectory(): Promise<string> {
    try {
        return (await getHomeDirectory()) || HOST_HOME_FALLBACK;
    } catch {
        return HOST_HOME_FALLBACK;
    }
}
