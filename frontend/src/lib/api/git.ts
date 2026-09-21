import type { GitLogResponse, TaskCommit } from '../types';
import type { ApiDiffRow, DiffFile } from '../types/diff';
import { enrichRowsWithInlineSegments } from '../utils/diff-parser';
import { API_BASE, apiFetch, apiError, ensureCsrfToken } from './helpers';
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

/** List all branches for a project. */
export async function getGitBranches(projectId: string): Promise<string[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/branches`);
    if (!res.ok) throw await apiError(res, t('api.fetchBranches'));
    return res.json();
}

/** Checkout a branch in a project's worktree. Requires CSRF token. */
export async function checkoutBranch(projectId: string, branch: string): Promise<{ branch: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/checkout`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
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

/** Stage or unstage a single file in the project repo. */
export async function stageProjectFile(projectId: string, path: string, stage: boolean): Promise<{ status: string; action: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/stage`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
        body: JSON.stringify({ path, stage }),
    });
    if (!res.ok) throw await apiError(res, stage ? t('api.stageFile') : t('api.unstageFile'));
    return res.json();
}

/** Discard one file's changes in the project repo: back to HEAD, or removed when HEAD never held it. */
export async function discardProjectFile(projectId: string, path: string): Promise<{ status: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/discard`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
        body: JSON.stringify({ path }),
    });
    if (!res.ok) throw await apiError(res, t('api.discardFile'));
    return res.json();
}

/** Commit staged changes in the project repo. */
export async function commitProject(projectId: string, message: string): Promise<{ status: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/commit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
        body: JSON.stringify({ message }),
    });
    if (!res.ok) throw await apiError(res, t('api.commit'));
    return res.json();
}

/** Pull from the remote in the project repo. */
export async function pullProject(projectId: string): Promise<{ status: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/pull`, {
        method: 'POST',
        headers: { ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
    });
    if (!res.ok) throw await apiError(res, t('api.pull'));
    return res.json();
}

/** Push to the remote in the project repo. */
export async function pushProject(projectId: string): Promise<{ status: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/push`, {
        method: 'POST',
        headers: { ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
    });
    if (!res.ok) throw await apiError(res, t('api.push'));
    return res.json();
}

// -- Task Git endpoints --

/** Fetch the unified diff for a task's worktree. Supports optional head/base refs and full file
 * content. The current worktree diff (no head/base/checkpoint) is cached so it stays viewable when
 * the server cannot be reached; non-current diffs are not cached. */
export interface TaskDiff {
    diff: string;
    untracked: Array<{ path: string; diff: string; file_size?: number; truncated?: boolean; orig_added?: number; lines?: string[]; line_count?: number }>;
    short_stat: string;
    truncated_files?: string[];
    original_file_stats?: Record<string, { added: number; removed: number }>;
    files?: Record<string, { hash: string; type: string; added?: number; removed?: number; truncated?: boolean; origAdded?: number; origRemoved?: number; line_count?: number; file_size?: number }>;
    /** Present only for ?parsed=1 — the same diff the server already parsed, per file. Named apart from
     * `files` above because that key is the manifest's keyed hash map. */
    parsed_files?: ParsedDiffFile[];
    base_ref?: string;
}

export async function getTaskDiff(projectId: string, taskId: string, options?: { headRef?: string; baseRef?: string; full?: boolean; checkpointIndex?: number; parsed?: boolean }): Promise<TaskDiff> {
    const params = new URLSearchParams();
    if (options?.headRef) params.set('head', options.headRef);
    if (options?.baseRef) params.set('base', options.baseRef);
    if (options?.full) params.set('full', '1');
    if (options?.checkpointIndex != null) params.set('checkpointIdx', String(options.checkpointIndex));
    if (options?.parsed) params.set('parsed', '1');
    const url = params.toString() ? `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}` : `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`;
    // Cacheability is the ref shape, never the parsed flag: the diff panel sends parsed=1 on every load.
    const isCurrentDiff = !options?.headRef && !options?.baseRef && options?.checkpointIndex == null;
    // The canonical done-task diff (pinned BaseCommit..BranchTip, requested by
    // head ref only) is immutable — serve it straight from IndexedDB when we
    // have it (zero network), and persist it after the first fetch. Checkpoint
    // diffs and custom-base comparisons share the headRef param but are NOT
    // the canonical done diff, so they must bypass this cache.
    const isDoneDiff = !!options?.headRef && !options?.baseRef && options?.checkpointIndex == null && !options?.full;
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

/** Fetch the content of a single file (tracked or untracked) from the cached diff. */
export async function getDiffFile(projectId: string, taskId: string, path: string, options?: { full?: boolean; signal?: AbortSignal }): Promise<{ type?: string; lines?: string[]; line_count?: number; rows?: Array<{ key?: string; lineNumber: number | null; variant: string; text?: string; segments?: Array<{ key: string; text: string; tone: string }> }>; truncated?: boolean; origAdded?: number; origRemoved?: number; isBinary?: boolean; fileSize?: number; isNew?: boolean; isDeleted?: boolean }> {
    const params = new URLSearchParams({ path });
    if (options?.full) params.set('full', '1');
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff/file?${params}`, { signal: options?.signal });
    if (!res.ok) throw await apiError(res, t('api.fetchFileContent'));
    return res.json();
}

/** Stage or unstage a single file in the task's worktree. */
export async function stageFile(projectId: string, taskId: string, path: string, stage: boolean): Promise<{ status: string; action: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff/stage`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
        body: JSON.stringify({ path, stage }),
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
    const params = new URLSearchParams({ base: baseHash, head: headHash, parsed: '1' });
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}`);
    if (!res.ok) throw await apiError(res, t('api.fetchTaskCommitDiff'));
    const body = await res.json();
    return toDiffFiles(body?.parsed_files ?? []);
}
