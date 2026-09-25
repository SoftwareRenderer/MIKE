import type { Board, Project, ProjectCounts, ProjectStats, ProjectUpdates } from '../types';
import { API_BASE, apiFetch, apiError, csrfHeaders, readEventStream } from './helpers';
import {
    getOfflineStatus,
    enqueueAction,
    queueWrite,
} from './queue';
import {
    createProjectRaw,
    updateProjectRaw,
    deleteProjectRaw,
    resetProjectStatsRaw,
} from './raw/projects';
import { setBoardCache, getBoardCache, boardCacheKey } from '../storage';
import { markCached } from './cache';
import { repoNameFromGitUrl } from '../utils/repo-name';
import { gitUrlCarriesCredentials } from '../utils/git-url';
import { t } from '$lib/i18n';

/** Fetch all projects from the server. Falls back to cached copy on failure.
 * Always tries the network first: a read is what releases the queue (`api/queue.ts`). */
export async function getProjects(): Promise<Project[]> {
    try {
        const res = await apiFetch(`${API_BASE}/projects`);
        if (!res.ok) throw await apiError(res, t('api.fetchProjects'));
        const data = await res.json();
        // Cache fire-and-forget
        setBoardCache('projects', data).catch(() => {});
        return data;
    } catch (e) {
        const cached = await getBoardCache<Project[]>('projects');
        if (cached) return markCached(cached);
        throw e;
    }
}

/** Fetch a single project by ID. */
export async function getProject(id: string): Promise<Project> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`);
    if (!res.ok) throw await apiError(res, t('api.fetchProject'));
    return res.json();
}

/** Fetch task counts per column for all projects. Returns a map of projectId → counts. */
export async function getProjectCounts(): Promise<Record<string, ProjectCounts>> {
    const res = await apiFetch(`${API_BASE}/projects/counts`);
    if (!res.ok) throw await apiError(res, t('api.fetchProjectCounts'));
    return res.json();
}

/** Fetch usage statistics (token counts, costs) for a specific project. */
export async function getProjectStats(projectId: string): Promise<ProjectStats> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats`);
    if (!res.ok) throw await apiError(res, t('api.fetchProjectStats'));
    return res.json();
}

/** Reset usage statistics for a specific model in a project. Queued when offline. */
export async function resetProjectStats(projectId: string, modelName: string, providerName?: string): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'resetProjectStats', params: { projectId, modelName, providerName } });
        return;
    }
    return resetProjectStatsRaw(projectId, modelName, providerName);
}

/** Create a new project at the given filesystem path (or from a git repo). Queued when offline. */
export async function createProject(path: string, sandboxEnabled = false, gitUrl?: string): Promise<Project> {
    if (getOfflineStatus()) {
        refuseQueuedGitCredentials(gitUrl);
        return await queueWrite('createProject', { path, sandboxEnabled, gitUrl }, {
            id: '', name: path, path, sandbox_enabled: sandboxEnabled,
            created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
        } as Project);
    }
    return createProjectRaw(path, sandboxEnabled, gitUrl);
}

// -- Clone with live progress (SSE) --

/**
 * A clone URL with a credential in it is refused while offline, never stripped.
 *
 * The queue is persisted, so a queued URL is an access token in IndexedDB after the
 * tab closes — the same reasoning that refuses to queue a provider key
 * (`updateConfig`). Stripping the userinfo instead would queue a URL that clones
 * something else (a public repo of the same name, or nothing), which is the quiet
 * failure the key refusal exists to avoid.
 */
function refuseQueuedGitCredentials(gitUrl?: string): void {
    if (gitUrl && gitUrlCarriesCredentials(gitUrl)) {
        throw new Error(t('api.queueGitCredentials'));
    }
}

/** Terminal events emitted by POST /api/projects/clone (one JSON object per SSE frame). */
export type CloneEvent =
    | { type: 'status'; message: string }
    | { type: 'progress'; percent: number }
    | { type: 'done'; project: Project }
    | { type: 'error'; message: string };

/** Parse a single SSE `data:` frame payload from the clone endpoint.
 * Returns null for non-JSON or unrecognized frames (defensive — the current
 * server only emits CloneEvent objects). */
export function parseCloneEvent(data: string): CloneEvent | null {
    if (!data.startsWith('{')) return null;
    try {
        const obj = JSON.parse(data) as Record<string, unknown>;
        switch (obj.type) {
            case 'status':
                return { type: 'status', message: String(obj.message ?? '') };
            case 'progress':
                return { type: 'progress', percent: Number(obj.percent ?? 0) };
            case 'done':
                return { type: 'done', project: obj.project as Project };
            case 'error':
                return { type: 'error', message: String(obj.message ?? t('api.cloneFailed')) };
            default:
                return null;
        }
    } catch {
        return null;
    }
}

/** Clone a git repository into a new project, streaming live clone progress
 * (0-100) via onProgress while online. Offline, the action is queued and
 * replayed through the synchronous create endpoint on reconnect. */
export async function cloneProject(
    targetDir: string,
    gitUrl: string,
    sandboxEnabled: boolean,
    onProgress?: (percent: number) => void,
): Promise<Project> {
    if (getOfflineStatus()) {
        refuseQueuedGitCredentials(gitUrl);
        return await queueWrite('createProject', { path: targetDir, sandboxEnabled, gitUrl }, {
            id: '', name: repoNameFromGitUrl(gitUrl), path: targetDir, sandbox_enabled: sandboxEnabled,
            created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
        } as Project);
    }
    return cloneProjectRaw(targetDir, gitUrl, sandboxEnabled, onProgress);
}

async function cloneProjectRaw(
    targetDir: string,
    gitUrl: string,
    sandboxEnabled: boolean,
    onProgress?: (percent: number) => void,
): Promise<Project> {
    const headers = await csrfHeaders({ 'Content-Type': 'application/json' });

    const body: Record<string, unknown> = {
        path: targetDir,
        git_url: gitUrl,
        sandbox_enabled: sandboxEnabled,
    };
    if (sandboxEnabled) {
        body.sandbox_network_isolation = true;
    }

    const res = await fetch(`${API_BASE}/projects/clone`, {
        method: 'POST',
        credentials: 'include',
        headers,
        body: JSON.stringify(body),
    });
    if (!res.ok) {
        // Early failures (bad body, missing git_url) arrive as plain JSON.
        throw await apiError(res, t('api.cloneProject'));
    }

    let project: Project | null = null;
    let errorMessage: string | null = null;

    for await (const frame of readEventStream(res)) {
        const event = parseCloneEvent(frame.data);
        if (!event) continue;
        if (event.type === 'progress') {
            onProgress?.(event.percent);
        } else if (event.type === 'done') {
            project = event.project;
        } else if (event.type === 'error') {
            errorMessage = event.message;
        }
    }

    if (errorMessage) throw new Error(errorMessage);
    if (!project) throw new Error(t('api.cloneCompletedWithoutResult'));
    return project;
}

/** Update project metadata (name, sandbox settings). Queued when offline. */
export async function updateProject(id: string, updates: ProjectUpdates): Promise<Project> {
    if (getOfflineStatus()) {
        return await queueWrite('updateProject', { id, updates }, { id, ...updates } as Project);
    }
    return updateProjectRaw(id, updates);
}

/** Delete a project and all its tasks. Queued when offline. */
export async function deleteProject(id: string): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'deleteProject', params: { id } });
        return;
    }
    return deleteProjectRaw(id);
}

// -- Board endpoints --

/** Fetch the full board state (tasks + columns) for a project. Falls back to cached copy on failure.
 * Always tries the network first: a read is what releases the queue (`api/queue.ts`). */
export async function getBoard(projectId: string): Promise<Board> {
    try {
        const res = await apiFetch(`${API_BASE}/projects/${projectId}/board`);
        if (!res.ok) throw await apiError(res, t('api.fetchBoard'));
        const data = await res.json();
        // Cache fire-and-forget
        setBoardCache(boardCacheKey(projectId), data).catch(() => {});
        return data;
    } catch (e) {
        const cached = await getBoardCache<Board>(boardCacheKey(projectId));
        if (cached) return markCached(cached);
        throw e;
    }
}
