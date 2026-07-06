import type { Board, Project, ProjectCounts, ProjectStats } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import {
    getOfflineStatus,
    enqueueAction,
    queueWrite,
    createProjectRaw,
    updateProjectRaw,
    buildSandboxImageRaw,
    deleteProjectRaw,
    resetProjectStatsRaw,
    reorderTasksRaw,
} from './queue';
import { setBoardCache, getBoardCache, boardCacheKey } from '../storage';
import type { ReorderRequest } from '../types';

/** Runtime marker on cached data to indicate it was served from the offline mirror. */
const CACHE_MARKER = Symbol('fromCache');
export function isFromCache(obj: unknown): boolean {
    return (obj as any)?.[CACHE_MARKER] === true;
}
function markCached<T>(data: T): T {
    return Object.assign(data, { [CACHE_MARKER]: true });
}

/** Fetch all projects from the server. Falls back to cached copy on failure or when offline. */
export async function getProjects(): Promise<Project[]> {
    // When offline, skip network and read cache directly
    if (getOfflineStatus()) {
        const cached = await getBoardCache<Project[]>('projects');
        if (cached) return markCached(cached);
        throw new Error('No cached projects available');
    }
    try {
        const res = await apiFetch(`${API_BASE}/projects`);
        if (!res.ok) throw await apiError(res, 'Failed to fetch projects');
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
    if (!res.ok) throw await apiError(res, 'Failed to fetch project');
    return res.json();
}

/** Fetch task counts per column for all projects. Returns a map of projectId → counts. */
export async function getProjectCounts(): Promise<Record<string, ProjectCounts>> {
    const res = await apiFetch(`${API_BASE}/projects/counts`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project counts');
    return res.json();
}

/** Fetch usage statistics (token counts, costs) for a specific project. */
export async function getProjectStats(projectId: string): Promise<ProjectStats> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project stats');
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

/** Create a new project at the given filesystem path. Queued when offline. */
export async function createProject(path: string, sandboxEnabled = false): Promise<Project> {
    if (getOfflineStatus()) {
        return await queueWrite('createProject', { path, sandboxEnabled }, {
            id: '', name: path, path, sandbox_enabled: sandboxEnabled,
            created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
        } as Project);
    }
    return createProjectRaw(path, sandboxEnabled);
}

/** Update project metadata (name, sandbox settings). Queued when offline. */
export async function updateProject(id: string, updates: { name?: string; sandbox_enabled?: boolean; sandbox_network_isolation?: boolean; sandbox_image?: string; sandbox_dockerfile_path?: string; sandbox_memory_limit?: number; sandbox_forward_ports?: { container_port: number }[] }): Promise<Project> {
    if (getOfflineStatus()) {
        return await queueWrite('updateProject', { id, updates }, { id, ...updates } as Project);
    }
    return updateProjectRaw(id, updates);
}

/** Trigger a sandbox image build for a project. Queued when offline. */
export async function buildSandboxImage(id: string): Promise<{ status: string; image: string; project: string }> {
    if (getOfflineStatus()) {
        return await queueWrite('buildSandboxImage', { id }, { status: 'queued', image: '', project: id });
    }
    return buildSandboxImageRaw(id);
}

/** Trigger a sandbox image build with live progress streaming. */
export { buildSandboxImageStream } from './queue';

/** @deprecated Port validation removed — host ports are auto-assigned at runtime. */
export async function validateForwardPorts(_projectId: string, _ports: { container_port: number }[]): Promise<number[]> {
    return [];
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

/** Fetch the full board state (tasks + columns) for a project. Falls back to cached copy on failure or when offline. */
export async function getBoard(projectId: string): Promise<Board> {
    // When offline, skip network and read cache directly
    if (getOfflineStatus()) {
        const cached = await getBoardCache<Board>(boardCacheKey(projectId));
        if (cached) return markCached(cached);
        throw new Error(`No cached board available for project ${projectId}`);
    }
    try {
        const res = await apiFetch(`${API_BASE}/projects/${projectId}/board`);
        if (!res.ok) throw await apiError(res, 'Failed to fetch board');
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

/** Reorder tasks within columns. Queued when offline. */
export async function reorderTasks(projectId: string, reorders: ReorderRequest[]): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'reorderTasks', params: { projectId, reorders } });
        return;
    }
    return reorderTasksRaw(projectId, reorders);
}
