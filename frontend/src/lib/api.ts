import { API_BASE, apiFetch, apiError, generateId, setCsrfToken, clearCsrfToken, ensureCsrfToken, registerSetOffline } from './api/helpers';

// Re-export helpers for direct imports from $lib/api
export { generateId, setCsrfToken, clearCsrfToken, ensureCsrfToken };

// Re-export stream and terminal modules
export { createAppStream } from './api/stream';
export type { AppStream, AppStreamCallbacks } from './api/stream';
export { createTerminal, createProjectTerminal } from './api/terminal';

// Register setOfflineStatus so helpers can mark app offline on fetch failures
registerSetOffline((offline: boolean) => setOfflineStatus(offline));

// =============================================================================
// Offline Action Queue
// =============================================================================
// Queues write operations when the WebSocket is disconnected.
// Persists to localStorage so actions survive page refresh.
// On reconnect, actions are replayed in order.
// =============================================================================

import { loadQueue as loadQueueFromStorage, saveQueue as saveQueueToStorage } from './storage';

export interface QueuedAction {
    id: string;
    type: string;
    params: Record<string, any>;
    timestamp: number;
}

let isOffline = false;
let actionQueue: QueuedAction[] = [];
let flushPromise: Promise<void> | null = null;
let queueChangeCallbacks: Array<() => void> = [];
let queueFlushedCallbacks: Array<(optimisticTaskIds: string[], resolvedTasks: Map<string, any>) => void> = [];

/** Check if the app is currently offline (WebSocket disconnected). */
export function getOfflineStatus(): boolean {
    return isOffline;
}

/** Set offline state. Triggers automatic flush when transitioning to online. */
export function setOfflineStatus(offline: boolean) {
    isOffline = offline;
    if (!offline && actionQueue.length > 0) {
        flushQueue();
    }
}

/** Register a callback fired when the queue changes (enqueue/dequeue/flush). */
export function onQueueChanged(callback: () => void) {
    queueChangeCallbacks.push(callback);
    return () => {
        queueChangeCallbacks = queueChangeCallbacks.filter(cb => cb !== callback);
    };
}

function notifyQueueChanged() {
    for (const cb of queueChangeCallbacks) {
        try { cb(); } catch { /* ignore */ }
    }
}

/** Register a callback fired when the queue finishes flushing. Receives map of optimistic ID → real Task. */
export function onQueueFlushed(callback: (optimisticTaskIds: string[], resolvedTasks: Map<string, any>) => void): () => void {
    queueFlushedCallbacks.push(callback);
    return () => {
        queueFlushedCallbacks = queueFlushedCallbacks.filter(cb => cb !== callback);
    };
}

function notifyQueueFlushed(optimisticTaskIds: string[], resolvedTasks: Map<string, any>) {
    for (const cb of queueFlushedCallbacks) {
        try { cb(optimisticTaskIds, resolvedTasks); } catch { /* ignore */ }
    }
}

/** Load queue from localStorage. */
export function loadQueue(): QueuedAction[] {
    actionQueue = loadQueueFromStorage();
    return actionQueue;
}

/** Save queue to localStorage. */
function saveQueue() {
    saveQueueToStorage(actionQueue);
    notifyQueueChanged();
}

/** Add an action to the queue and persist. */
export function enqueueAction(action: Omit<QueuedAction, 'id' | 'timestamp'>): QueuedAction {
    const item: QueuedAction = {
        ...action,
        id: generateId(),
        timestamp: Date.now(),
    };
    actionQueue.push(item);
    saveQueue();
    return item;
}

/** Remove a specific action from the queue. */
function removeAction(id: string) {
    actionQueue = actionQueue.filter(a => a.id !== id);
    saveQueue();
}

/** Get set of task IDs that have pending queued actions. */
export function getQueuedTaskIds(): Set<string> {
    const ids = new Set<string>();
    for (const action of actionQueue) {
        if (action.params.taskId) ids.add(action.params.taskId);
        // Also include optimistic task IDs from createTask actions
        if (action.type === 'createTask' && action.params.taskData?.id) {
            ids.add(action.params.taskData.id);
        }
    }
    return ids;
}

/** Get set of task IDs that have a pending delete action. */
export function getQueuedDeleteTaskIds(): Set<string> {
    const ids = new Set<string>();
    for (const action of actionQueue) {
        if (action.type === 'deleteTask' && action.params.taskId) {
            ids.add(action.params.taskId);
        }
    }
    return ids;
}

/** Get current queue length. */
export function getQueueLength(): number {
    return actionQueue.length;
}

/** Debug: expose offline status for testing. */
export function __debugOfflineStatus(): boolean {
    return isOffline;
}

/** Flush queued actions via HTTP immediately (e.g., on page load). Independent of WebSocket state. */
export async function flushQueueNow(): Promise<void> {
    if (flushPromise) return flushPromise;
    if (actionQueue.length === 0) return;

    flushPromise = (async () => {
        const idMap = new Map<string, string>();
        const resolvedTasks = new Map<string, any>();

        while (actionQueue.length > 0) {
            const action = actionQueue[0];
            try {
                const result = await executeQueuedAction(action, idMap);
                if (action.type === 'createTask' && result && action.params.taskData?.id) {
                    const optId = action.params.taskData.id;
                    idMap.set(optId, result.id);
                    resolvedTasks.set(optId, result);
                }
                if (result && result.id && (action.type === 'startTask' || action.type === 'moveTask' ||
                    action.type === 'stopTask' || action.type === 'retryTask' || action.type === 'resumeTask' ||
                    action.type === 'commitAndDone' || action.type === 'updateTask')) {
                    resolvedTasks.set(result.id, result);
                }
                actionQueue.shift();
                saveQueue();
            } catch (e) {
                console.error('[Queue] Failed to flush action, skipping:', action.type, e);
                actionQueue.shift();
                saveQueue();
            }
        }

        notifyQueueFlushed([], resolvedTasks);
        flushPromise = null;
    })();
    return flushPromise;
}

/** Replay all queued actions in order. Triggered on WebSocket reconnect. */
async function flushQueue() {
    if (flushPromise) return flushPromise;
    if (actionQueue.length === 0) return;

    flushPromise = (async () => {
        const wasOffline = isOffline;
        // Temporarily set online so raw functions execute directly
        isOffline = false;
        // Map optimistic task IDs → real Task objects (resolved during flush)
        const idMap = new Map<string, string>();
        const resolvedTasks = new Map<string, any>();

        while (actionQueue.length > 0) {
            const action = actionQueue[0];
            try {
                const result = await executeQueuedAction(action, idMap);
                // If createTask returned a real task, map optimistic ID → real ID
                if (action.type === 'createTask' && result && action.params.taskData?.id) {
                    const optId = action.params.taskData.id;
                    idMap.set(optId, result.id);
                    resolvedTasks.set(optId, result);
                }
                // Track latest state of tasks modified by other actions (start, move, etc.)
                if (result && result.id && (action.type === 'startTask' || action.type === 'moveTask' ||
                    action.type === 'stopTask' || action.type === 'retryTask' || action.type === 'resumeTask' ||
                    action.type === 'commitAndDone' || action.type === 'updateTask')) {
                    resolvedTasks.set(result.id, result);
                }
                actionQueue.shift();
                saveQueue();
            } catch (e) {
                console.error('[Queue] Failed to flush action, skipping:', action.type, e);
                // Skip failed action — don't block remaining actions or re-set offline
                actionQueue.shift();
                saveQueue();
            }
        }

        // If we had a failure, keep failed actions in queue for next retry.
        // Don't set isOffline=true — the WebSocket is connected, so new actions should go through.
        isOffline = wasOffline;
        // Notify listeners with resolved tasks so they can replace optimistic ones
        notifyQueueFlushed([], resolvedTasks);
        flushPromise = null;
    })();
    return flushPromise;
}

/** Execute a single queued action by type. Resolves optimistic task IDs via idMap. */
async function executeQueuedAction(action: QueuedAction, idMap: Map<string, string>): Promise<any> {
    const p = action.params;
    // Resolve optimistic task ID → real task ID
    const resolveId = (taskId: string) => idMap.get(taskId) || taskId;
    const taskId = resolveId(p.taskId);
    switch (action.type) {
        case 'sendTaskMessage':
            await sendTaskMessageRaw(p.projectId, taskId, p.content, p.modelName, p.serverUrl, p.attachments);
            break;
        case 'createTaskMessage':
            await createTaskMessageRaw(p.projectId, taskId, p.role, p.content);
            break;
        case 'deleteTaskMessagesAfter':
            await deleteTaskMessagesAfterRaw(p.projectId, taskId, p.messageId, p.regenerate);
            break;
        case 'deleteTaskMessage':
            await deleteTaskMessageRaw(p.projectId, taskId, p.messageId);
            break;
        case 'updateTaskMessage':
            await updateTaskMessageRaw(p.projectId, taskId, p.messageId, p.content);
            break;
        case 'createTask':
            return createTaskRaw(p.projectId, p.taskData);
        case 'updateTask':
            await updateTaskRaw(p.projectId, taskId, p.taskData);
            break;
        case 'deleteTask':
            await deleteTaskRaw(p.projectId, taskId);
            break;
        case 'moveTask':
            await moveTaskRaw(p.projectId, taskId, p.status);
            break;
        case 'commitAndDone':
            await commitAndDoneRaw(p.projectId, taskId);
            break;
        case 'clearDoneTasks':
            await clearDoneTasksRaw(p.projectId);
            break;
        case 'startTask':
            await startTaskRaw(p.projectId, taskId);
            break;
        case 'startSubtask':
            await startSubtaskRaw(p.projectId, taskId);
            break;
        case 'stopTask':
            await stopTaskRaw(p.projectId, taskId);
            break;
        case 'retryTask':
            await retryTaskRaw(p.projectId, taskId);
            break;
        case 'resumeTask':
            await resumeTaskRaw(p.projectId, taskId);
            break;
        case 'reorderTasks':
            await reorderTasksRaw(p.projectId, p.reorders);
            break;
        case 'createProject':
            await createProjectRaw(p.path, p.sandboxEnabled);
            break;
        case 'updateProject':
            await updateProjectRaw(p.id, p.updates);
            break;
        case 'deleteProject':
            await deleteProjectRaw(p.id);
            break;
        case 'switchTaskThread':
            await switchTaskThreadRaw(p.projectId, taskId, p.threadId);
            break;
        case 'forkTaskThread':
            await forkTaskThreadRaw(p.projectId, taskId, p.fromThreadId, p.fromMessageId, p.newContent);
            break;
        case 'deleteTaskThread':
            await deleteTaskThreadRaw(p.projectId, taskId, p.threadId);
            break;
        case 'resetWorktree':
            await resetWorktreeRaw(p.projectId, taskId, p.commitHash);
            break;
        case 'updateConfig':
            await updateConfigRaw(p.config);
            break;
        case 'resetProjectStats':
            await resetProjectStatsRaw(p.projectId, p.modelName, p.providerName);
            break;
        case 'buildSandboxImage':
            await buildSandboxImageRaw(p.id);
            break;
        case 'reloadPrompts':
            await reloadPromptsRaw();
            break;
        default:
            console.warn('[Queue] Unknown action type:', action.type);
    }
}

/**
 * Helper: enqueue an action and return an optimistic result.
 * Returns the queued action's ID for tracking.
 */
function queueWrite<T>(type: string, params: Record<string, any>, optimistic: T): T & { queued: true; actionId: string } {
    const queued = enqueueAction({ type, params });
    return { ...(optimistic as any), queued: true, actionId: queued.id } as T & { queued: true; actionId: string };
}

// -- Project endpoints --
export async function getProjects(): Promise<Project[]> {
    const res = await apiFetch(`${API_BASE}/projects`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch projects');
    return res.json();
}

export async function getProjectCounts(): Promise<Record<string, ProjectCounts>> {
    const res = await apiFetch(`${API_BASE}/projects/counts`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project counts');
    return res.json();
}

export async function getProjectStats(projectId: string): Promise<ProjectStats> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch project stats');
    return res.json();
}

export async function resetProjectStatsRaw(projectId: string, modelName: string, providerName?: string): Promise<void> {
    const params = new URLSearchParams({ model_name: modelName });
    if (providerName) params.set('provider_name', providerName);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats?${params}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to reset stats');
}
export async function resetProjectStats(projectId: string, modelName: string, providerName?: string): Promise<void> {
    if (isOffline) { enqueueAction({ type: 'resetProjectStats', params: { projectId, modelName, providerName } }); return; }
    return resetProjectStatsRaw(projectId, modelName, providerName);
}

async function createProjectRaw(path: string, sandboxEnabled = false): Promise<Project> {
    const body: Record<string, unknown> = { path, sandbox_enabled: sandboxEnabled };
    if (sandboxEnabled) {
        body.sandbox_network_isolation = true;
    }
    const res = await apiFetch(`${API_BASE}/projects`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create project');
    return res.json();
}
export async function createProject(path: string, sandboxEnabled = false): Promise<Project> {
    if (isOffline) { return queueWrite('createProject', { path, sandboxEnabled }, { id: '', name: path, path, sandbox_enabled: sandboxEnabled }); }
    return createProjectRaw(path, sandboxEnabled);
}

async function updateProjectRaw(id: string, updates: { name?: string; sandbox_enabled?: boolean; sandbox_network_isolation?: boolean; sandbox_image?: string; sandbox_dockerfile_path?: string; sandbox_memory_limit?: number }): Promise<Project> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updates),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update project');
    return res.json();
}
export async function updateProject(id: string, updates: { name?: string; sandbox_enabled?: boolean; sandbox_network_isolation?: boolean; sandbox_image?: string; sandbox_dockerfile_path?: string; sandbox_memory_limit?: number }): Promise<Project> {
    if (isOffline) { return queueWrite('updateProject', { id, updates }, { id, ...updates }); }
    return updateProjectRaw(id, updates);
}

async function buildSandboxImageRaw(id: string): Promise<{ status: string; image: string; project: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${id}/sandbox/build`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to build sandbox image');
    return res.json();
}
export async function buildSandboxImage(id: string): Promise<{ status: string; image: string; project: string }> {
    if (isOffline) { return queueWrite('buildSandboxImage', { id }, { status: 'queued', image: '', project: id }); }
    return buildSandboxImageRaw(id);
}

async function deleteProjectRaw(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete project');
}
export async function deleteProject(id: string): Promise<void> {
    if (isOffline) { enqueueAction({ type: 'deleteProject', params: { id } }); return; }
    return deleteProjectRaw(id);
}

// -- Board endpoints --
export async function getBoard(projectId: string): Promise<Board> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/board`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch board');
    return res.json();
}

async function reorderTasksRaw(projectId: string, reorders: ReorderRequest[]): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/reorder`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(reorders),
    });
    if (!res.ok) throw await apiError(res, 'Failed to reorder tasks');
}
export async function reorderTasks(projectId: string, reorders: ReorderRequest[]): Promise<void> {
    if (isOffline) { enqueueAction({ type: 'reorderTasks', params: { projectId, reorders } }); return; }
    return reorderTasksRaw(projectId, reorders);
}

// -- Task CRUD endpoints --
async function createTaskRaw(projectId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create task');
    return res.json();
}
export async function createTask(projectId: string, task: Partial<Task>): Promise<Task> {
    if (isOffline) { const id = generateId(); return queueWrite('createTask', { projectId, taskData: task }, { ...task, id, status: 'backlog', created_at: new Date().toISOString() }); }
    return createTaskRaw(projectId, task);
}

export async function getTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`);
    if (!res.ok) throw await apiError(res, 'Task not found');
    return res.json();
}

async function updateTaskRaw(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update task');
    return res.json();
}
export async function updateTask(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    if (isOffline) { return queueWrite('updateTask', { projectId, taskId, taskData: task }, { id: taskId, ...task }); }
    return updateTaskRaw(projectId, taskId, task);
}

async function deleteTaskRaw(projectId: string, taskId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete task');
}
export async function deleteTask(projectId: string, taskId: string): Promise<void> {
    if (isOffline) { enqueueAction({ type: 'deleteTask', params: { projectId, taskId } }); return; }
    return deleteTaskRaw(projectId, taskId);
}

async function moveTaskRaw(projectId: string, taskId: string, status: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to move task');
    return res.json();
}
export async function moveTask(projectId: string, taskId: string, status: string): Promise<Task> {
    if (isOffline) { return queueWrite('moveTask', { projectId, taskId, status }, { id: taskId, status }); }
    return moveTaskRaw(projectId, taskId, status);
}

async function commitAndDoneRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commit`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to commit and mark as done');
    return res.json();
}
export async function commitAndDone(projectId: string, taskId: string): Promise<Task> {
    if (isOffline) { return queueWrite('commitAndDone', { projectId, taskId }, { id: taskId, status: 'done' }); }
    return commitAndDoneRaw(projectId, taskId);
}

async function clearDoneTasksRaw(projectId: string): Promise<{ deleted: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/done`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to clear done tasks');
    return res.json();
}
export async function clearDoneTasks(projectId: string): Promise<{ deleted: number }> {
    if (isOffline) { enqueueAction({ type: 'clearDoneTasks', params: { projectId } }); return { deleted: 0 }; }
    return clearDoneTasksRaw(projectId);
}

// -- Task execution endpoints --
async function startTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to start task');
    return res.json();
}
export async function startTask(projectId: string, taskId: string): Promise<Task> {
    if (isOffline) { return queueWrite('startTask', { projectId, taskId }, { id: taskId, status: 'in_progress' }); }
    return startTaskRaw(projectId, taskId);
}

async function startSubtaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start-subtask`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to start subtask');
    return res.json();
}
export async function startSubtask(projectId: string, taskId: string): Promise<Task> {
    if (isOffline) { return queueWrite('startSubtask', { projectId, taskId }, { id: taskId, status: 'in_progress' }); }
    return startSubtaskRaw(projectId, taskId);
}

async function stopTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to stop task');
    return res.json();
}
export async function stopTask(projectId: string, taskId: string): Promise<Task> {
    if (isOffline) { return queueWrite('stopTask', { projectId, taskId }, { id: taskId, status_state: 'idle' }); }
    return stopTaskRaw(projectId, taskId);
}

async function retryTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/retry`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to retry task');
    return res.json();
}

async function resumeTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/resume`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to resume task');
    return res.json();
}
export async function resumeTask(projectId: string, taskId: string): Promise<Task> {
    if (isOffline) { return queueWrite('resumeTask', { projectId, taskId }, { id: taskId, status: 'in_progress' }); }
    return resumeTaskRaw(projectId, taskId);
}

/** Send a user message via HTTP POST. Triggers executor and returns the created message. */
async function sendTaskMessageRaw(
    projectId: string,
    taskId: string,
    content: string,
    modelName?: string,
    serverUrl?: string,
    attachments?: Array<{ mimeType: string; data: string }>
): Promise<{ id: string; role: string; content: string; thread_id?: string }> {
    const body: Record<string, any> = { content };
    if (modelName) body.model_name = modelName;
    if (serverUrl) body.llama_server_url = serverUrl;
    if (attachments && attachments.length > 0) {
        body.attachments = JSON.stringify(attachments);
    }
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/send`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to send message');
    return res.json();
}
export async function sendTaskMessage(
    projectId: string,
    taskId: string,
    content: string,
    modelName?: string,
    serverUrl?: string,
    attachments?: Array<{ mimeType: string; data: string }>
): Promise<{ id: string; role: string; content: string; thread_id?: string }> {
    if (isOffline) {
        const id = generateId();
        return queueWrite('sendTaskMessage', { projectId, taskId, content, modelName, serverUrl, attachments }, { id, role: 'user', content });
    }
    return sendTaskMessageRaw(projectId, taskId, content, modelName, serverUrl, attachments);
}

export async function getTaskMessages(projectId: string, taskId: string): Promise<TaskMessage[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch messages');
    return res.json();
}

async function createTaskMessageRaw(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create message');
    return res.json();
}
export async function createTaskMessage(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    if (isOffline) {
        const id = generateId();
        return queueWrite('createTaskMessage', { projectId, taskId, role, content }, { id, task_id: taskId, thread_id: '', role, content, created_at: new Date().toISOString() });
    }
    return createTaskMessageRaw(projectId, taskId, role, content);
}

async function deleteTaskMessagesAfterRaw(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, regenerate }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete messages');
}
export async function deleteTaskMessagesAfter(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    if (isOffline) { enqueueAction({ type: 'deleteTaskMessagesAfter', params: { projectId, taskId, messageId, regenerate } }); return; }
    return deleteTaskMessagesAfterRaw(projectId, taskId, messageId, regenerate);
}

/** Delete a single message by ID (e.g., dismissing an error). Does not delete messages after it. */
async function deleteTaskMessageRaw(projectId: string, taskId: string, messageId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, delete_single: true }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete message');
}
export async function deleteTaskMessage(projectId: string, taskId: string, messageId: string): Promise<void> {
    if (isOffline) { enqueueAction({ type: 'deleteTaskMessage', params: { projectId, taskId, messageId } }); return; }
    return deleteTaskMessageRaw(projectId, taskId, messageId);
}

/** Regenerate from a message: deletes it and all messages after it. Alias for deleteTaskMessagesAfter. */
export const regenerateTaskMessages = deleteTaskMessagesAfter;

/** Check if deleting a message would also delete a fork branch. */
export async function checkMessageDelete(projectId: string, taskId: string, messageId: string): Promise<{ will_delete_branch: boolean; branch_name: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`);
    if (!res.ok) throw await apiError(res, 'Failed to check message delete');
    return res.json();
}

async function updateTaskMessageRaw(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update message');
}
export async function updateTaskMessage(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    if (isOffline) { enqueueAction({ type: 'updateTaskMessage', params: { projectId, taskId, messageId, content } }); return; }
    return updateTaskMessageRaw(projectId, taskId, messageId, content);
}

export async function getTaskDiff(projectId: string, taskId: string, options?: { headRef?: string; baseRef?: string; full?: boolean }): Promise<{ diff: string; untracked: Array<{ path: string; diff: string; file_size?: number }>; short_stat: string; truncated_files?: string[]; original_file_stats?: Record<string, { added: number; removed: number }> }> {
    const params = new URLSearchParams();
    if (options?.headRef) params.set('head', options.headRef);
    if (options?.baseRef) params.set('base', options.baseRef);
    if (options?.full) params.set('full', '1');
    const url = params.toString() ? `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff?${params}` : `${API_BASE}/projects/${projectId}/tasks/${taskId}/diff`;
    const res = await apiFetch(url);
    if (!res.ok) throw await apiError(res, 'Failed to fetch diff');
    return res.json();
}

export async function getTaskCommits(projectId: string, taskId: string): Promise<{ base_commit?: TaskCommit; commits: TaskCommit[] }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commits`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch commits');
    return res.json();
}

async function resetWorktreeRaw(projectId: string, taskId: string, commitHash: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/reset-worktree`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ commit_hash: commitHash }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to reset worktree');
    return res.json();
}
export async function resetWorktree(projectId: string, taskId: string, commitHash: string): Promise<{ status: string }> {
    if (isOffline) { enqueueAction({ type: 'resetWorktree', params: { projectId, taskId, commitHash } }); return { status: 'queued' }; }
    return resetWorktreeRaw(projectId, taskId, commitHash);
}

// -- Prompt endpoints --
async function reloadPromptsRaw(): Promise<{ status: string; message: string; files: string[] }> {
    const res = await apiFetch(`${API_BASE}/prompts/reload`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to reload prompts');
    return res.json();
}

// -- Configuration endpoints --
export async function getConfig(): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch config');
    return res.json();
}

async function updateConfigRaw(config: Config): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update config');
    return res.json();
}
export async function updateConfig(config: Config): Promise<Config> {
    if (isOffline) { enqueueAction({ type: 'updateConfig', params: { config } }); return config; }
    return updateConfigRaw(config);
}

export async function testConnection(llamaServerUrl: string, modelName: string, apiKey?: string): Promise<{ success: boolean; message: string }> {
    const res = await apiFetch(`${API_BASE}/config/test-connection`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, model_name: modelName, api_key: apiKey }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to test connection');
    return res.json();
}

export async function fetchModels(llamaServerUrl: string, apiKey?: string): Promise<{ models: Array<{ name: string; context_window?: number }>; error?: string }> {
    const res = await apiFetch(`${API_BASE}/config/fetch-models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, api_key: apiKey }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to fetch models');
    return res.json();
}

/** Test Pi connection (runs `pi --version`). */
export async function testPiConnection(): Promise<{ success: boolean; message: string }> {
    const res = await apiFetch(`${API_BASE}/config/test-pi-connection`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
    });
    if (!res.ok) throw await apiError(res, 'Failed to test Pi connection');
    return res.json();
}

// -- Git endpoints --
/** Fetch git log with full commit details (hash, parents, date). */
export async function getGitLogFull(projectId: string, branch?: string): Promise<GitCommit[]> {
    const params = new URLSearchParams({ full: '1' });
    if (branch) params.set('branch', branch);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/log?${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch git log');
    return res.json();
}

/** List all branches for a project. */
export async function getGitBranches(projectId: string): Promise<string[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/git/branches`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch branches');
    return res.json();
}

/** Checkout a branch in a project. */
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

// -- Thread endpoints --
export async function getTaskThreads(projectId: string, taskId: string): Promise<Thread[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch threads');
    return res.json();
}

async function switchTaskThreadRaw(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/switch`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to switch thread');
    return res.json();
}
export async function switchTaskThread(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    if (isOffline) {
        return queueWrite('switchTaskThread', { projectId, taskId, threadId }, { id: threadId, task_id: taskId, is_active: true, created_at: new Date().toISOString() });
    }
    return switchTaskThreadRaw(projectId, taskId, threadId);
}

async function forkTaskThreadRaw(projectId: string, taskId: string, fromThreadId: string, fromMessageId: string, newContent?: string): Promise<Thread> {
    const body: Record<string, string> = { from_thread_id: fromThreadId, from_message_id: fromMessageId };
    if (newContent) body.new_content = newContent;
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/fork`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to fork thread');
    return res.json();
}
export async function forkTaskThread(projectId: string, taskId: string, fromThreadId: string, fromMessageId: string, newContent?: string): Promise<Thread> {
    if (isOffline) {
        const id = generateId();
        return queueWrite('forkTaskThread', { projectId, taskId, fromThreadId, fromMessageId, newContent }, { id, task_id: taskId, is_active: true, created_at: new Date().toISOString() });
    }
    return forkTaskThreadRaw(projectId, taskId, fromThreadId, fromMessageId, newContent);
}

async function deleteTaskThreadRaw(projectId: string, taskId: string, threadId: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete thread');
    return res.json();
}
export async function deleteTaskThread(projectId: string, taskId: string, threadId: string): Promise<{ status: string }> {
    if (isOffline) { enqueueAction({ type: 'deleteTaskThread', params: { projectId, taskId, threadId } }); return { status: 'queued' }; }
    return deleteTaskThreadRaw(projectId, taskId, threadId);
}

