import type { GitLogResponse, TaskCommit } from '../types';
import type { ApiDiffRow, DiffFile, DiffFileResponse, DiffManifest } from '../types/diff';
import { enrichRowsWithInlineSegments } from '../utils/diff-parser';
import { API_BASE, apiFetch, apiError } from './helpers';
import { setBoardCache, getBoardCache, diffCacheKey, doneDiffCacheKey } from '../storage';
import { markCached, isDisconnectedError } from './cache';
import { t } from '$lib/i18n';

// -- Git endpoints --

/** Fetch git log with full commit details (hash, parents, date, branches). Optional branch filter.
 * Returns commits array plus current_branch, origin_tip, and has_more metadata. */
export async function getGitLogFull(projectId: string, options?: { branch?: string; limit?: number; offset?: number; path?: string }): Promise<GitLogResponse> {
    const params = new URLSearchParams({ full: '1' });
    if (options?.branch) params.set('branch', options.branch);
    if (options?.limit != null) params.set('limit', String(options.limit));
    if (options?.offset != null) params.set('offset', String(options.offset));
    if (options?.path) params.set('path', options.path);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/log?${params}`);
    if (!res.ok) throw await apiError(res, t('api.fetchGitLog'));
    return res.json();
}


/**
 * A file as the server parsed it. The field names are backend/git.TrackedDiffFile's JSON tags, which
 * are the same names the task-diff manifest already uses — note `isNew` on the wire vs the client's
 * `DiffFile.isNewFile`, a mapping diff-cache.svelte.ts already performs.
 */
export interface ParsedDiffFile {
    path: string;
    origPath?: string;
    rows?: ApiDiffRow[];
    committedRows?: ApiDiffRow[];
    added: number;
    removed: number;
    fileSize?: number;
    isBinary?: boolean;
    isNew?: boolean;
    isDeleted?: boolean;
    truncated?: boolean;
    origAdded?: number;
    origRemoved?: number;
}

/**
 * Turn server-parsed entries into the DiffFile list the diff components bind to.
 *
 * Two steps that used to be inside parseUnifiedDiff and stay a VIEW concern:
 *  - the display filter (a file with no rows, no size, and no add/delete has nothing to show — e.g. a
 *    mode-only change), and
 *  - word-level inline segments, which need the `diff` package and pair removed/added rows by eye.
 * Parsing is the server's; presenting stays here.
 */
export function toDiffFiles(parsed: ParsedDiffFile[]): DiffFile[] {
    return parsed
        .map((f): DiffFile => ({
            path: f.path,
            origPath: f.origPath,
            rows: (f.rows ?? []) as unknown as DiffFile['rows'],
            committedRows: f.committedRows ? (f.committedRows as unknown as DiffFile['rows']) : undefined,
            added: f.added ?? 0,
            removed: f.removed ?? 0,
            fileSize: f.fileSize,
            isBinary: f.isBinary,
            isNewFile: f.isNew,
            isDeleted: f.isDeleted,
            truncated: f.truncated,
            origAdded: f.origAdded,
            origRemoved: f.origRemoved,
        }))
        .filter(f => f.rows.length > 0 || f.fileSize || f.isDeleted || f.isNewFile)
        .map(f => ({ ...f, rows: enrichRowsWithInlineSegments(f.rows as unknown as ApiDiffRow[]) as unknown as DiffFile['rows'] }));
}

/** Fetch a project diff. The route answers `git.ParseUnifiedDiff`'s output unconditionally. */
export async function getProjectDiffFiles(projectId: string, options?: { base?: string; head?: string; cached?: boolean }): Promise<DiffFile[]> {
    const params = new URLSearchParams();
    if (options?.base) params.set('base', options.base);
    if (options?.head) params.set('head', options.head);
    if (options?.cached) params.set('cached', '1');
    const query = params.toString();
    const url = `${API_BASE}/projects/${projectId}/git/diff${query ? `?${query}` : ''}`;
    const res = await apiFetch(url);
    if (!res.ok) throw await apiError(res, t('api.fetchProjectDiff'));
    return toDiffFiles(await res.json());
}

/** Fetch the diff between two commits, already parsed by the server. */
export async function getCommitDiffFiles(projectId: string, baseHash: string, headHash: string): Promise<DiffFile[]> {
    return getProjectDiffFiles(projectId, { base: baseHash, head: headHash });
}

/** BranchRef is one row of the branch route: a short ref, the worktree holding it, and the task behind it. */
interface BranchRef {
    name: string;
    /** The route omits an empty holder, so a ref this checkout may take carries no `worktree` key at all. */
    worktree?: string;
    /** task_id names the task whose worktree holds this ref. */
    task_id?: string;
}

/** getGitBranches returns every ref the server calls a branch, remote-tracking refs and the detached `HEAD`
 * line included. */
async function getGitBranches(projectId: string): Promise<BranchRef[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/branches`);
    if (!res.ok) throw await apiError(res, t('api.fetchBranches'));
    return res.json();
}

/** isLocalRef tells whether a row is a branch of this repository rather than a memory of a remote one. */
function isLocalRef(ref: BranchRef): boolean {
    return !ref.name.startsWith('origin/') && ref.name !== 'HEAD';
}

/** getLocalBranches returns the branches a person may be offered: the local ones, in the server's order. */
export async function getLocalBranches(projectId: string): Promise<string[]> {
    const all = await getGitBranches(projectId);
    return all.filter(isLocalRef).map(b => b.name);
}

/**
 * getSwitchableBranches returns what the project explorer may offer as a switch, plus the refs it withheld
 * because another worktree holds them and the task behind each of those.
 */
export async function getSwitchableBranches(projectId: string): Promise<{
    branches: string[];
    held: HeldBranch[];
    withheld: number;
}> {
    const all = await getGitBranches(projectId);
    const branches: string[] = [];
    const held: HeldBranch[] = [];
    let withheld = 0;
    for (const ref of all) {
        if (!isLocalRef(ref)) continue;
        if (!ref.worktree) {
            branches.push(ref.name);
            continue;
        }
        withheld++;
        if (ref.task_id) held.push({ name: ref.name, taskId: ref.task_id });
    }
    return { branches, held, withheld };
}

/** HeldBranch is a ref a task's worktree holds; the row for it links to that task. */
export interface HeldBranch {
    name: string;
    taskId: string;
}

/** Checkout a branch in a project's worktree. */
export async function checkoutBranch(projectId: string, branch: string): Promise<{ branch: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/checkout`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ branch }),
    });
    if (!res.ok) throw await apiError(res, t('api.checkoutBranch'));
    return res.json();
}

/** Fetch git status (porcelain format) for a project.
 * Returns { clean: boolean, status: string, head: string, authorName: string, authorEmail: string,
 * ahead: number, behind: number }. Status is the raw `git status --porcelain` output. Author fields
 * are from project git config. `ahead`/`behind` are counted against the upstream's remote-tracking
 * ref, so a fetch moves them, not the remote moving. */
export async function getGitStatus(projectId: string): Promise<{ clean: boolean; status: string; head: string; authorName: string; authorEmail: string; ahead: number; behind: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/status`);
    if (!res.ok) throw await apiError(res, t('api.fetchGitStatus'));
    return res.json();
}


/** Fetch the content (or metadata) of a single file in the project repo.
 * When meta=true, only line_count/file_size/isBinary are returned (cheap).
 * Otherwise returns the unified diff plus the full file lines.
 * Works for tracked and untracked files. */
export async function getProjectDiffFile(projectId: string, path: string, options?: { meta?: boolean }): Promise<{ diff?: string; lines?: string[]; line_count?: number; file_size?: number; isBinary?: boolean }> {
    const params = new URLSearchParams({ path });
    if (options?.meta) params.set('meta', '1');
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/diff/file?${params}`);
    if (!res.ok) throw await apiError(res, t('api.fetchFile'));
    return res.json();
}

/**
 * What a batch git write answers: the paths that landed, and the ones it refused, each with MIKE's own
 * sentence. A batch that refused some paths is still an HTTP success — docs/git-operations.md → A git write
 * names every path it moved.
 */
export interface GitBatchResult {
    status: string;
    action: string;
    applied: string[];
    failed: { path: string; error: string }[];
}

/** Stage or unstage every named file in the project repo. */
export async function stageProjectPaths(projectId: string, paths: string[], stage: boolean): Promise<GitBatchResult> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/stage`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ paths, stage }),
    });
    if (!res.ok) throw await apiError(res, stage ? t('api.stageFile') : t('api.unstageFile'));
    return res.json();
}

/** Discard every named file's changes in the project repo: back to HEAD, or removed when HEAD never held it. */
export async function discardProjectPaths(projectId: string, paths: string[]): Promise<GitBatchResult> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/discard`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ paths }),
    });
    if (!res.ok) throw await apiError(res, t('api.discardFile'));
    return res.json();
}

/** Commit staged changes in the project repo. */
export async function commitProject(projectId: string, message: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/commit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message }),
    });
    if (!res.ok) throw await apiError(res, t('api.commit'));
    return res.json();
}

/** Pull from the remote in the project repo. */
export async function pullProject(projectId: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/pull`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.pull'));
    return res.json();
}

/** Push to the remote in the project repo. */
export async function pushProject(projectId: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/push`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.push'));
    return res.json();
}

// -- Task Git endpoints --

/**
 * What `/tasks/{id}/diff` answers: the manifest plus, for the calls that asked for it, the patch text and
 * the server's own parse of it.
 */
export interface TaskDiff extends DiffManifest {
    /** The patch text, present when the caller asked for the diff itself rather than the manifest. */
    diff?: string;
    untracked?: Array<{ path: string; diff: string; file_size?: number; truncated?: boolean; orig_added?: number; lines?: string[]; line_count?: number }>;
    truncated_files?: string[];
    original_file_stats?: Record<string, { added: number; removed: number }>;
    /** Present only for ?parsed=1 — the same diff the server already parsed, per file. Named apart from
     * `files` above because that key is the manifest's keyed hash map. */
    parsed_files?: ParsedDiffFile[];
}

/** How a caller narrows `/diff`: which two refs to compare, whether to send full file content, and
 * whether the server should hand back the diff it already parsed. */
export interface TaskDiffOptions {
    headRef?: string;
    baseRef?: string;
    full?: boolean;
    parsed?: boolean;
}

/**
 * Fetch the unified diff for a task's worktree. Supports optional head/base refs and full file content.
 * The current worktree diff (no head/base) is cached so it stays viewable when the server
 * cannot be reached; non-current diffs are not cached.
 */
export async function getTaskDiff(projectId: string, taskId: string, options?: TaskDiffOptions): Promise<TaskDiff> {
    const params = new URLSearchParams();
    if (options?.headRef) params.set('head', options.headRef);
    if (options?.baseRef) params.set('base', options.baseRef);
    if (options?.full) params.set('full', '1');
    if (options?.parsed) params.set('parsed', '1');
    const url = params.toString() ? `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}` : `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`;
    // Cacheability is the ref shape, never the parsed flag: the diff panel sends parsed=1 on every load.
    const isCurrentDiff = !options?.headRef && !options?.baseRef;
    // The canonical done-task diff (pinned BaseCommit..BranchTip, requested by
    // head ref only) is immutable — serve it straight from IndexedDB when we
    // have it (zero network), and persist it after the first fetch. Checkpoint
    // diffs and custom-base comparisons share the headRef param but are NOT
    // the canonical done diff, so they must bypass this cache.
    const isDoneDiff = !!options?.headRef && !options?.baseRef && !options?.full;
    // A stored body that carries only patch text is a miss for a parsed request: it renders empty.
    const usableCached = (cached: TaskDiff | null): cached is TaskDiff =>
        !!cached && (!options?.parsed || !!cached.parsed_files);
    if (isDoneDiff) {
        const cached = await getBoardCache<TaskDiff>(doneDiffCacheKey(projectId, taskId, options!.headRef!));
        if (usableCached(cached)) return markCached(cached);
    }
    try {
        const res = await apiFetch(url);
        if (!res.ok) throw await apiError(res, t('api.fetchDiff'));
        const data = await res.json();
        // Cache only the current worktree diff (fire-and-forget)
        if (isCurrentDiff) {
            setBoardCache(diffCacheKey(projectId, taskId), data).catch(() => {});
        }
        if (isDoneDiff) {
            setBoardCache(doneDiffCacheKey(projectId, taskId, options!.headRef!), data).catch(() => {});
        }
        return data;
    } catch (e) {
        if (isCurrentDiff && isDisconnectedError(e)) {
            const cached = await getBoardCache<TaskDiff>(diffCacheKey(projectId, taskId));
            if (cached) return markCached(cached);
        }
        // Unlike the live worktree diff, this one is immutable: a stored copy is right whatever failed.
        if (isDoneDiff) {
            const cached = await getBoardCache<TaskDiff>(doneDiffCacheKey(projectId, taskId, options!.headRef!));
            if (usableCached(cached)) return markCached(cached);
        }
        throw e;
    }
}

/** Fetch commit history for a task, including the base commit and all task commits. */
export async function getTaskCommits(projectId: string, taskId: string): Promise<{ base_commit?: TaskCommit; commits: TaskCommit[] }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commits`);
    if (!res.ok) throw await apiError(res, t('api.fetchCommits'));
    return res.json();
}

/** Fetch the content of a single file (tracked or untracked) from the cached diff. With `headRef` the answer
 * is that file between `headRef` and `baseRef` (default: the task's diff baseline) — the comparison a
 * checkpoint panel shows, not the working tree. */
export async function getDiffFile(projectId: string, taskId: string, path: string, options?: { full?: boolean; signal?: AbortSignal; headRef?: string; baseRef?: string }): Promise<DiffFileResponse> {
    const params = new URLSearchParams({ path });
    if (options?.full) params.set('full', '1');
    if (options?.headRef) params.set('head', options.headRef);
    if (options?.baseRef) params.set('base', options.baseRef);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff/file?${params}`, { signal: options?.signal });
    if (!res.ok) throw await apiError(res, t('api.fetchFileContent'));
    return res.json();
}

/** Stage or unstage every named file in the task's worktree. */
export async function stageTaskPaths(projectId: string, taskId: string, paths: string[], stage: boolean): Promise<GitBatchResult> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff/stage`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ paths, stage }),
    });
    if (!res.ok) throw await apiError(res, stage ? t('api.stageFile') : t('api.unstageFile'));
    return res.json();
}

// -- Task Explorer Git endpoints --

/** Fetch full git log for a task's worktree branch (like project git log but task-scoped).
 * Returns commits array plus current_branch, origin_tip, and has_more metadata. */
export async function getTaskGitLog(projectId: string, taskId: string, options?: { branch?: string; limit?: number; offset?: number }): Promise<GitLogResponse> {
    const params = new URLSearchParams({ full: '1' });
    if (options?.branch) params.set('branch', options.branch);
    if (options?.limit != null) params.set('limit', String(options.limit));
    if (options?.offset != null) params.set('offset', String(options.offset));
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/git/log?${params}`);
    if (!res.ok) throw await apiError(res, t('api.fetchTaskGitLog'));
    return res.json();
}


/**
 * The diff for one commit in a task's log, already parsed by the server.
 *
 * The old path read this route's body with `res.text()` and fed it to the browser's diff parser — but
 * `/tasks/{id}/diff` answers with a JSON TaskDiff, and a JSON document on one line yields zero files,
 * so the overlay had nothing to draw. Taking `files` off the parsed response is both the fix and the
 * point: the server already parsed this diff, so the client should not try to.
 */
export async function getTaskCommitDiffFiles(projectId: string, taskId: string, baseHash: string, headHash: string): Promise<DiffFile[]> {
    const body = await getTaskDiff(projectId, taskId, { baseRef: baseHash, headRef: headHash, parsed: true });
    return toDiffFiles(body.parsed_files ?? []);
}
