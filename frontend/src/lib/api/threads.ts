import type { Thread } from '../types';
import { API_BASE, apiFetch, apiError, generateId } from './helpers';
import {
    getOfflineStatus,
    queueWrite,
    switchTaskThreadRaw,
    forkTaskThreadRaw,
} from './queue';

// -- Thread endpoints --

/** Fetch all conversation threads for a task. */
export async function getTaskThreads(projectId: string, taskId: string): Promise<Thread[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch threads');
    return res.json();
}

/** Switch the active conversation thread for a task. Queued when offline. */
export async function switchTaskThread(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    if (getOfflineStatus()) {
        return await queueWrite('switchTaskThread', { projectId, taskId, threadId }, {
            id: threadId, task_id: taskId, name: '', is_active: true, created_at: new Date().toISOString(),
        } as Thread);
    }
    return switchTaskThreadRaw(projectId, taskId, threadId);
}

/** Fork a conversation thread from a specific message, creating a branch point. Optionally includes new content. Queued when offline. */
export async function forkTaskThread(projectId: string, taskId: string, fromThreadId: string, fromMessageId: string, newContent?: string): Promise<Thread> {
    if (getOfflineStatus()) {
        const id = generateId();
        return await queueWrite('forkTaskThread', { projectId, taskId, fromThreadId, fromMessageId, newContent }, {
            id, task_id: taskId, name: '', is_active: true, created_at: new Date().toISOString(),
        } as Thread);
    }
    return forkTaskThreadRaw(projectId, taskId, fromThreadId, fromMessageId, newContent);
}
