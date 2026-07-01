import type { GitCommit, TaskCommit } from '../types';
import { API_BASE, apiFetch, apiError, ensureCsrfToken } from './helpers';
import {
    getOfflineStatus,
    enqueueAction,
    resetWorktreeRaw,
} from './queue';

// -- Git endpoints --

/** Fetch git log with full commit details (hash, parents, date). Optional branch filter. */
export async function getGitLogFull(projectId: string, branch?: string): Promise<GitCommit[]> {
    const params = new URLSearchParams({ full: '1' });
    if (branch) params.set('branch', branch);
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

// -- Task Git endpoints --

/** Fetch the unified diff for a task's worktree. Supports optional head/base refs and full file content. */
export async function getTaskDiff(projectId: string, taskId: string, options?: { headRef?: string; baseRef?: string; full?: boolean }): Promise<{ diff: string; untracked: Array<{ path: string; diff: string; file_size?: number; truncated?: boolean; orig_added?: number; lines?: string[]; line_count?: number }>; short_stat: string; truncated_files?: string[]; original_file_stats?: Record<string, { added: number; removed: number }>; files?: Record<string, { hash: string; type: string; added?: number; removed?: number; truncated?: boolean; origAdded?: number; origRemoved?: number; line_count?: number; file_size?: number }> }> {
    const params = new URLSearchParams();
    if (options?.headRef) params.set('head', options.headRef);
    if (options?.baseRef) params.set('base', options.baseRef);
    if (options?.full) params.set('full', '1');
    const url = params.toString() ? `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}` : `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`;
    const res = await apiFetch(url);
    if (!res.ok) throw await apiError(res, 'Failed to fetch diff');
    return res.json();
}

/** Fetch commit history for a task, including the base commit and all task commits. */
export async function getTaskCommits(projectId: string, taskId: string): Promise<{ base_commit?: TaskCommit; commits: TaskCommit[] }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commits`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch commits');
    return res.json();
}

/** Fetch the content of a single file (tracked or untracked) from the cached diff. */
export async function getDiffFile(projectId: string, taskId: string, path: string, options?: { full?: boolean; signal?: AbortSignal }): Promise<{ type?: string; lines?: string[]; line_count?: number; rows?: Array<{ key?: string; lineNumber: number | null; variant: string; text?: string; segments?: Array<{ key: string; text: string; tone: string }> }>; truncated?: boolean; origAdded?: number; origRemoved?: number }> {
    const params = new URLSearchParams({ path });
    if (options?.full) params.set('full', '1');
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/diff/file?${params}`, { signal: options?.signal });
    if (!res.ok) throw await apiError(res, 'Failed to fetch file content');
    return res.json();
}

/** Fetch the content of a single untracked file from the cached diff. (legacy) */
export async function getUntrackedFileContent(projectId: string, taskId: string, path: string): Promise<string[]> {
    const data = await getDiffFile(projectId, taskId, path);
    return data.lines || [];
}

/** Reset a task's worktree to a specific commit. Queued when offline. */
export async function resetWorktree(projectId: string, taskId: string, commitHash: string): Promise<{ status: string }> {
    if (getOfflineStatus()) {
        enqueueAction({ type: 'resetWorktree', params: { projectId, taskId, commitHash } });
        return { status: 'queued' };
    }
    return resetWorktreeRaw(projectId, taskId, commitHash);
}
