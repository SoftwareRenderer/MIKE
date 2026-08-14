import type { GitLogResponse, TaskCommit } from '../types';
import { API_BASE, apiFetch, apiError, ensureCsrfToken } from './helpers';
import {
    getOfflineStatus,
    queueWrite,
    resetWorktreeRaw,
} from './queue';
import { setBoardCache, getBoardCache, diffCacheKey } from '../storage';
import { markCached } from './cache';

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
    if (!res.ok) throw await apiError(res, 'Failed to fetch git log');
    return res.json();
}

/** Fetch the unified diff between two commits as raw text. */
export async function getCommitDiff(projectId: string, baseHash: string, headHash: string): Promise<string> {
    const params = new URLSearchParams({ base: baseHash, head: headHash });
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/diff?${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch commit diff');
    return res.text();
}

/** List all branches for a project. */
export async function getGitBranches(projectId: string): Promise<string[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/branches`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch branches');
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
    if (!res.ok) throw await apiError(res, 'Failed to checkout branch');
    return res.json();
}

/** Fetch git status (porcelain format) for a project.
 * Returns { clean: boolean, status: string, head: string, authorName: string, authorEmail: string }.
 * Status is the raw `git status --porcelain` output. Author fields are from project git config. */
export async function getGitStatus(projectId: string): Promise<{ clean: boolean; status: string; head: string; authorName: string; authorEmail: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/status`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch git status');
    return res.json();
}

/** Fetch the unified diff between two commits (or working tree vs ref) as raw text.
 * When cached=true, returns staged changes only (git diff --cached). */
export async function getProjectDiff(projectId: string, options?: { base?: string; head?: string; ref?: string; cached?: boolean }): Promise<string> {
    const params = new URLSearchParams();
    if (options?.base) params.set('base', options.base);
    if (options?.head) params.set('head', options.head);
    if (options?.ref) params.set('ref', options.ref);
    if (options?.cached) params.set('cached', '1');
    const url = params.toString() ? `${API_BASE}/projects/${projectId}/git/diff?${params}` : `${API_BASE}/projects/${projectId}/git/diff`;
    const res = await apiFetch(url);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project diff');
    return res.text();
}

/** Fetch the content (or metadata) of a single file in the project repo.
 * When meta=true, only line_count/file_size/isBinary are returned (cheap).
 * Otherwise returns the unified diff plus the full file lines.
 * Works for tracked and untracked files. */
export async function getProjectDiffFile(projectId: string, path: string, options?: { meta?: boolean; staged?: boolean }): Promise<{ diff?: string; lines?: string[]; line_count?: number; file_size?: number; isBinary?: boolean }> {
    const params = new URLSearchParams({ path });
    if (options?.meta) params.set('meta', '1');
    if (options?.staged) params.set('staged', 'true');
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/diff/file?${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch file');
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
    if (!res.ok) throw await apiError(res, stage ? 'Failed to stage file' : 'Failed to unstage file');
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
    if (!res.ok) throw await apiError(res, 'Failed to commit');
    return res.json();
}

// -- Task Git endpoints --

/** Fetch the unified diff for a task's worktree. Supports optional head/base refs and full file content.
 * The current worktree diff (no head/base/checkpoint options) is cached so it remains viewable offline;
 * on failure it falls back to the cached copy. Non-current diffs are not cached. */
export interface TaskDiff {
    diff: string;
    untracked: Array<{ path: string; diff: string; file_size?: number; truncated?: boolean; orig_added?: number; lines?: string[]; line_count?: number }>;
    short_stat: string;
    truncated_files?: string[];
    original_file_stats?: Record<string, { added: number; removed: number }>;
    files?: Record<string, { hash: string; type: string; added?: number; removed?: number; truncated?: boolean; origAdded?: number; origRemoved?: number; line_count?: number; file_size?: number }>;
    base_ref?: string;
}

export async function getTaskDiff(projectId: string, taskId: string, options?: { headRef?: string; baseRef?: string; full?: boolean; checkpointIndex?: number }): Promise<TaskDiff> {
    const params = new URLSearchParams();
    if (options?.headRef) params.set('head', options.headRef);
    if (options?.baseRef) params.set('base', options.baseRef);
    if (options?.full) params.set('full', '1');
    if (options?.checkpointIndex != null) params.set('checkpointIdx', String(options.checkpointIndex));
    const url = params.toString() ? `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}` : `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`;
    const isCurrentDiff = !options?.headRef && !options?.baseRef && options?.checkpointIndex == null;
    try {
        const res = await apiFetch(url);
        if (!res.ok) throw await apiError(res, 'Failed to fetch diff');
        const data = await res.json();
        // Cache only the current worktree diff (fire-and-forget)
        if (isCurrentDiff) {
            setBoardCache(diffCacheKey(projectId, taskId), data).catch(() => {});
        }
        return data;
    } catch (e) {
        // Fall back to cached copy only for the current diff request
        if (isCurrentDiff) {
            const cached = await getBoardCache<TaskDiff>(diffCacheKey(projectId, taskId));
            if (cached) return markCached(cached);
        }
        throw e;
    }
}

/** Fetch commit history for a task, including the base commit and all task commits. */
export async function getTaskCommits(projectId: string, taskId: string): Promise<{ base_commit?: TaskCommit; commits: TaskCommit[] }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commits`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch commits');
    return res.json();
}

/** Fetch the content of a single file (tracked or untracked) from the cached diff. */
export async function getDiffFile(projectId: string, taskId: string, path: string, options?: { full?: boolean; signal?: AbortSignal }): Promise<{ type?: string; lines?: string[]; line_count?: number; rows?: Array<{ key?: string; lineNumber: number | null; variant: string; text?: string; segments?: Array<{ key: string; text: string; tone: string }> }>; truncated?: boolean; origAdded?: number; origRemoved?: number; isBinary?: boolean; fileSize?: number; isNew?: boolean; isDeleted?: boolean }> {
    const params = new URLSearchParams({ path });
    if (options?.full) params.set('full', '1');
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff/file?${params}`, { signal: options?.signal });
    if (!res.ok) throw await apiError(res, 'Failed to fetch file content');
    return res.json();
}

/** Reset a task's worktree to a specific commit. Queued when offline. */
export async function resetWorktree(projectId: string, taskId: string, commitHash: string): Promise<{ status: string }> {
    if (getOfflineStatus()) {
        return await queueWrite('resetWorktree', { projectId, taskId, commitHash }, { status: 'queued' });
    }
    return resetWorktreeRaw(projectId, taskId, commitHash);
}

/** Stage or unstage a single file in the task's worktree. */
export async function stageFile(projectId: string, taskId: string, path: string, stage: boolean): Promise<{ status: string; action: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff/stage`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
        body: JSON.stringify({ path, stage }),
    });
    if (!res.ok) throw await apiError(res, stage ? 'Failed to stage file' : 'Failed to unstage file');
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
    if (!res.ok) throw await apiError(res, 'Failed to fetch task git log');
    return res.json();
}

/** List all branches in a task's worktree. */
export async function getTaskGitBranches(projectId: string, taskId: string): Promise<string[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/git/branches`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch task branches');
    return res.json();
}

/** Checkout a branch in a task's worktree. Requires CSRF token. */
export async function checkoutTaskBranch(projectId: string, taskId: string, branch: string): Promise<{ status: string; branch: string }> {
    const csrfToken = await ensureCsrfToken();
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/git/checkout`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}) },
        body: JSON.stringify({ branch }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to checkout branch');
    return res.json();
}

/** Fetch the unified diff between two commits in a task's worktree as raw text. */
export async function getTaskCommitDiff(projectId: string, taskId: string, baseHash: string, headHash: string): Promise<string> {
    const params = new URLSearchParams({ base: baseHash, head: headHash });
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch task commit diff');
    return res.text();
}
