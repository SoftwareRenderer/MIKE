import { API_BASE, apiFetch, apiError } from './helpers';
import { t } from '$lib/i18n';

/** One row of a `GET /api/fs` listing. */
export interface FsEntry {
    name: string;
    isDir: boolean;
    path: string;
    isGitRepo: boolean;
    /** git excludes this path; the explorer dims the row rather than leaving it out. */
    ignored?: boolean;
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
 * The URL `GET /api/fs/read?kind=image` answers with one picture's bytes. It is a URL rather than a fetch
 * because the caller puts it in an `<img>`: the browser then loads it itself and offers "Open image in new
 * tab". `path` is relative to the repository root; `ref` names a commit, omit it for the file on disk.
 */
export function imageFileURL(params: { project: string; task?: string; path: string; ref?: string }): string {
    const query = fileScopeQuery(params);
    query.set('kind', 'image');
    return `${API_BASE}/fs/read?${query.toString()}`;
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

/**
 * The URL `GET /api/fs/download` answers with the named paths as files the browser saves: one file's
 * own bytes, a folder or a selection as one tar.gz, sent one `path` key each.
 *
 * A URL, not a fetch, so the browser streams bytes this app never holds — and its response names the
 * saved file, so callers must not set the `download` attribute.
 */
export function downloadURL(params: { project: string; task?: string; paths: string[] }): string {
    const query = new URLSearchParams({ project: params.project });
    if (params.task) query.set('task', params.task);
    for (const path of params.paths) query.append('path', path);
    return `${API_BASE}/fs/download?${query.toString()}`;
}

/**
 * One path of `GET /api/fs/stat`, in the order the request named them. `path` repeats the spelling the
 * caller sent, never the host file it resolved to. A row the server measured nothing about leaves `bytes`
 * and `items` absent — which is not zero — and the rest empty.
 */
export interface FsStatRow {
    name: string;
    path: string;
    /** `missing` is a name gone since the listing was read, `unknown` one the server could not state. */
    kind: 'file' | 'dir' | 'link' | 'other' | 'missing' | 'unknown';
    /** A folder's total over the files below it. */
    bytes?: number;
    /** A folder's entries below it, the folder itself excluded. */
    items?: number;
    /** The folder's walk ran out of budget, so its figures are a floor. */
    partial?: boolean;
    /** Four octal digits. */
    mode?: string;
    /** Fixed-width UTC. */
    modified?: string;
    /** From the suffix, not from the bytes. */
    mime?: string;
}

/**
 * What a listing leaves out for each named path. One `path` key each; a refusal arrives as the server's
 * own sentence.
 */
export async function statFsPaths(params: { project: string; task?: string; paths: string[] }): Promise<FsStatRow[]> {
    const query = scopeQuery(params);
    for (const path of params.paths) query.append('path', path);
    const res = await apiFetch(`${API_BASE}/fs/stat?${query.toString()}`);
    if (!res.ok) throw await apiError(res, t('explorer.fileInfo.failed'));
    const answer = await res.json();
    return Array.isArray(answer?.entries) ? answer.entries : [];
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
 * Read one file as text. `path` is relative to the repository root — the name git and the diff panels use
 * — and the tree it is read from comes from `task`, never from the path. What a caller keys its own
 * records by (an editor buffer, say) is the caller's business and stays out of this request.
 *
 * The size cap is the server's editor buffer, not the tunable file limit, and 413 comes back from the stat:
 * a 4 GB file costs one probe, and a caller that shows files must treat that refusal as a state.
 */
export async function readFsFile(params: {
    path: string;
    project: string;
    /** Task whose worktree holds the file; omit for the project's own checkout. */
    task?: string;
    /** Read what a commit holds instead of what the working tree holds. */
    ref?: string;
}): Promise<FsFileRead> {
    const res = await apiFetch(`${API_BASE}/fs/read?${fileScopeQuery(params).toString()}`);
    if (!res.ok) throw await readRefusal(res);
    return await res.json();
}

/**
 * Which file one read means: the project, the task's tree, the path, and the commit when the working tree
 * is not what is being read. Every file-reading route spells the scope this way.
 */
function fileScopeQuery(params: { path: string; project: string; task?: string; ref?: string }): URLSearchParams {
    const query = new URLSearchParams({ path: params.path, project: params.project });
    if (params.task) query.set('task', params.task);
    if (params.ref) query.set('ref', params.ref);
    return query;
}

/** A file read that answered a status rather than content, as the error carrying the server's sentence. */
async function readRefusal(res: Response): Promise<FsReadRefused> {
    return new FsReadRefused(res.status, (await apiError(res, t('editor.readFailed'))).message);
}

/** One member of an archive, as the listing shows it. */
export interface FsArchiveEntry {
    /** Its name inside the archive — also the name that reads it back. */
    name: string;
    size: number;
    /** Fixed-width UTC; empty when the archive recorded no time. */
    mtime: string;
    isDir: boolean;
}

/** What `GET /api/fs/archive` answers for one archive. */
export interface FsArchiveListing {
    /** The container the bytes turned out to be, not the clicked name. */
    format: string;
    /** Archive order, never re-sorted. */
    entries: FsArchiveEntry[];
    /** True when there are more rows than these. */
    truncated?: boolean;
}

/**
 * What one archive holds. Listing means walking the whole file, so the server may run out of its budget:
 * that answer is `truncated` rows, or an `FsReadRefused` status when it read nothing.
 */
export async function readFsArchive(params: {
    path: string;
    project: string;
    task?: string;
    /** List what a commit holds instead of the file on disk. */
    ref?: string;
}): Promise<FsArchiveListing> {
    const res = await apiFetch(`${API_BASE}/fs/archive?${fileScopeQuery(params).toString()}`);
    if (!res.ok) throw await readRefusal(res);
    return await res.json();
}

/** The text of one member, by the name the listing showed. 413 and 415 are states, not faults. */
export async function readFsArchiveEntry(params: {
    path: string;
    project: string;
    entry: string;
    task?: string;
    ref?: string;
}): Promise<string> {
    const query = fileScopeQuery(params);
    query.set('entry', params.entry);
    const res = await apiFetch(`${API_BASE}/fs/archive/entry?${query.toString()}`);
    if (!res.ok) throw await readRefusal(res);
    return (await res.json()).content ?? '';
}

/**
 * Write one file. `fallback` is the caller's own sentence for a refusal that carries none, so a save and
 * a file creation say what the user was doing rather than what the code was calling.
 *
 * `exclusive` marks the second of those: the server refuses the write when the name is already taken.
 */
export async function writeFsFile(params: {
    project: string;
    task?: string;
    path: string;
    content: string;
    fallback: string;
    exclusive?: boolean;
}): Promise<void> {
    const body = { path: params.path, content: params.content, ...(params.exclusive ? { exclusive: true } : {}) };
    await postFsJSON(`${API_BASE}/fs/write`, scopeQuery(params), body, params.fallback);
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
