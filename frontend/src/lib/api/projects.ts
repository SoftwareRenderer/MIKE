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
import type { ReorderRequest } from '../types';

/** Fetch all projects from the server. */
export async function getProjects(): Promise<Project[]> {
    const res = await apiFetch(`${API_BASE}/projects`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch projects');
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
export async function updateProject(id: string, updates: { name?: string; sandbox_enabled?: boolean; sandbox_network_isolation?: boolean; sandbox_image?: string; sandbox_dockerfile_path?: string; sandbox_memory_limit?: number }): Promise<Project> {
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

/** Delete a project and all its tasks. Queued when offline. */
export async function deleteProject(id: string): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'deleteProject', params: { id } });
        return;
    }
    return deleteProjectRaw(id);
}

// -- Board endpoints --

/** Fetch the full board state (tasks + columns) for a project. */
export async function getBoard(projectId: string): Promise<Board> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/board`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch board');
    return res.json();
}

/** Reorder tasks within columns. Queued when offline. */
export async function reorderTasks(projectId: string, reorders: ReorderRequest[]): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'reorderTasks', params: { projectId, reorders } });
        return;
    }
    return reorderTasksRaw(projectId, reorders);
}
