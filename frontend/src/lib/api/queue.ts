import { API_BASE, apiFetch, apiError, ensureCsrfToken, generateId, registerSetOffline } from './helpers';
import { loadQueue as loadQueueFromStorage, saveQueue as saveQueueToStorage } from '../storage';
import type { Config, Project, ReorderRequest, Task, TaskMessage, Thread } from '../types';

// Re-export helpers for direct imports
export { generateId };

// Register setOfflineStatus so helpers can mark app offline on fetch failures
registerSetOffline((offline: boolean) => setOfflineStatus(offline));

// =============================================================================
// Offline Action Queue
// =============================================================================
// Queues write operations when the WebSocket is disconnected.
// Persists to IndexedDB so actions survive page refresh.
// On reconnect, actions are replayed in order.
// =============================================================================

export interface QueuedAction {
    id: string;
    type: string;
    params: Record<string, any>;
    timestamp: number;
}

let isOffline = false;
let actionQueue: QueuedAction[] = [];
let flushPromise: Promise<void> | null = null;

/** Wait for any in-flight flush to complete. Resolves immediately if none. */
export async function waitForFlush(): Promise<void> {
    if (flushPromise) await flushPromise;
}

/** Safe access to actionQueue length (defensive against null/undefined). */
function queueLength(): number {
    return Array.isArray(actionQueue) ? actionQueue.length : 0;
}
let queueChangeCallbacks: Array<() => void> = [];
let queueFlushedCallbacks: Array<(optimisticTaskIds: string[], resolvedTasks: Map<string, any>) => void> = [];

/** Check if the app is currently offline (WebSocket disconnected). */
export function getOfflineStatus(): boolean {
    return isOffline;
}

/** Set offline state. Triggers automatic flush when transitioning to online. */
export function setOfflineStatus(offline: boolean) {
    isOffline = offline;
    if (!offline && queueLength() > 0) {
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

/** Load queue from IndexedDB. */
export async function loadQueue(): Promise<QueuedAction[]> {
    const loaded = await loadQueueFromStorage();
    actionQueue = Array.isArray(loaded) ? loaded : [];
    return actionQueue;
}

/** Save queue to IndexedDB. */
async function saveQueue() {
    await saveQueueToStorage(actionQueue);
    notifyQueueChanged();
}

/** Add an action to the queue and persist. */
export async function enqueueAction(action: Omit<QueuedAction, 'id' | 'timestamp'>): Promise<QueuedAction> {
    const item: QueuedAction = {
        ...action,
        id: generateId(),
        timestamp: Date.now(),
    };
    actionQueue.push(item);
    await saveQueue();
    return item;
}

/** Remove a specific action from the queue. */
async function removeAction(id: string) {
    actionQueue = actionQueue.filter(a => a.id !== id);
    await saveQueue();
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
    return queueLength();
}

/** Debug: expose offline status for testing. */
export function __debugOfflineStatus(): boolean {
    return isOffline;
}

/** Flush queued actions via HTTP immediately (e.g., on page load). Independent of WebSocket state. */
export async function flushQueueNow(): Promise<void> {
    if (flushPromise) return flushPromise;
    if (queueLength() === 0) return;

    flushPromise = (async () => {
        const idMap = new Map<string, string>();
        const resolvedTasks = new Map<string, any>();

        while (queueLength() > 0) {
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
                await saveQueue();
            } catch (e: any) {
                console.error('[Queue] Failed to flush action:', action.type, e);
                // Transient errors (network unreachable, abort) — stop flushing, keep remaining actions in queue
                if (e?.name === 'TypeError' || e?.name === 'AbortError') {
                    break;
                }
                // Permanent errors (404, 403, etc.) — skip this action, don't block the rest
                actionQueue.shift();
                await saveQueue();
            }
        }

        notifyQueueFlushed([], resolvedTasks);
        flushPromise = null;
    })();
    return flushPromise;
}

/** Check if an error is a transient network failure (retryable). */
function isTransientError(e: unknown): boolean {
    return (e as any)?.name === 'TypeError' || (e as any)?.name === 'AbortError';
}

/** Replay all queued actions in order. Triggered on WebSocket reconnect. */
async function flushQueue() {
    if (flushPromise) return flushPromise;
    if (queueLength() === 0) return;

    flushPromise = (async () => {
        const wasOffline = isOffline;
        // Temporarily set online so raw functions execute directly
        isOffline = false;
        // Map optimistic task IDs → real Task objects (resolved during flush)
        const idMap = new Map<string, string>();
        const resolvedTasks = new Map<string, any>();

        while (queueLength() > 0) {
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
                await saveQueue();
            } catch (e: any) {
                console.error('[Queue] Failed to flush action:', action.type, e);
                // Transient errors — stop flushing, keep remaining actions for next reconnect
                if (isTransientError(e)) {
                    isOffline = true;
                    break;
                }
                // Permanent errors (404, 403, etc.) — skip this action, don't block the rest
                actionQueue.shift();
                await saveQueue();
            }
        }

        isOffline = wasOffline;
        // Notify listeners with resolved tasks so they can replace optimistic ones
        notifyQueueFlushed([], resolvedTasks);
        flushPromise = null;
    })();
    return flushPromise;
}

// =============================================================================
// Replay queued actions onto cached board data (read-only transform)
// =============================================================================
// The cache mirrors server state. This function applies pending queue actions
// on top so the UI reflects what the user last did before going offline.
// Does NOT mutate the cache — returns a new tasks array for UI display.
// =============================================================================

/**
 * Apply pending queue actions to a copy of the cached board data.
 * Returns a new tasks array with optimistic updates applied.
 *
 * @param tasks  Cached tasks from the board (server state)
 * @returns      New tasks array with queued actions applied
 */
export function applyQueuedActionsToBoard(
    tasks: Task[],
): Task[] {
    // Map optimistic task IDs → real task IDs (resolved during flush)
    const optIdMap = new Map<string, string>();
    const result = tasks.map(t => ({ ...t })); // shallow copy

    for (const action of actionQueue) {
        const p = action.params;
        const taskId = p.taskId;
        const projectId = p.projectId;

        switch (action.type) {
            case 'createTask': {
                const taskData = p.taskData || {};
                const task: Task = {
                    id: taskData.id || generateId(),
                    project_id: projectId,
                    title: taskData.title || '',
                    prompt: taskData.prompt || '',
                    status: taskData.status || 'backlog',
                    column_order: taskData.column_order ?? 0,
                    execution_state: taskData.execution_state || 'idle',
                    model_name: taskData.model_name || '',
                    llama_server_url: taskData.llama_server_url || '',
                    reasoning_effort: taskData.reasoning_effort || '',
                    max_consecutive_failed_turns: taskData.max_consecutive_failed_turns ?? 5,
                    auto_commit: taskData.auto_commit ?? false,
                    auto_merge: taskData.auto_merge ?? false,
                    subtasks_auto_merge: taskData.subtasks_auto_merge ?? false,
                    plan_mode: taskData.plan_mode ?? false,
                    parent_task_id: taskData.parent_task_id || undefined,
                    title_generating: taskData.title_generating ?? false,
                    current_thread_id: taskData.current_thread_id || undefined,
                    created_at: new Date().toISOString(),
                    updated_at: new Date().toISOString(),
                };
                result.push(task);
                optIdMap.set(task.id, task.id);
                break;
            }

            case 'deleteTask': {
                const resolvedId = optIdMap.get(taskId) || taskId;
                const idx = result.findIndex(t => t.id === resolvedId);
                if (idx !== -1) result.splice(idx, 1);
                break;
            }

            case 'moveTask': {
                const resolvedId = optIdMap.get(taskId) || taskId;
                const task = result.find(t => t.id === resolvedId);
                if (task) task.status = p.status;
                break;
            }

            case 'startTask':
            case 'startSubtask':
            case 'retryTask':
            case 'resumeTask': {
                const resolvedId = optIdMap.get(taskId) || taskId;
                const task = result.find(t => t.id === resolvedId);
                if (task) {
                    task.status = 'in_progress';
                    task.execution_state = 'running';
                }
                break;
            }

            case 'stopTask': {
                const resolvedId = optIdMap.get(taskId) || taskId;
                const task = result.find(t => t.id === resolvedId);
                if (task) task.execution_state = 'idle';
                break;
            }

            case 'commitAndDone': {
                const resolvedId = optIdMap.get(taskId) || taskId;
                const task = result.find(t => t.id === resolvedId);
                if (task) task.status = 'done';
                break;
            }

            case 'clearDoneTasks': {
                for (let i = result.length - 1; i >= 0; i--) {
                    if (result[i].status === 'done') result.splice(i, 1);
                }
                break;
            }

            case 'updateTask': {
                const resolvedId = optIdMap.get(taskId) || taskId;
                if (p.taskData) {
                    const task = result.find(t => t.id === resolvedId);
                    if (task) Object.assign(task, p.taskData);
                }
                break;
            }

            case 'reorderTasks': {
                if (p.reorders) {
                    for (const reorder of p.reorders) {
                        const task = result.find(t => t.id === reorder.id);
                        if (task) task.column_order = reorder.column_order;
                    }
                }
                break;
            }

            // Message/project/config actions don't affect board task list
        }
    }

    return result;
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
export async function queueWrite<T>(type: string, params: Record<string, any>, optimistic: T): Promise<T> {
    const queued = await enqueueAction({ type, params });
    return { ...optimistic, queued: true, actionId: queued.id } as T;
}

// =============================================================================
// Raw HTTP functions (imported by domain modules)
// =============================================================================

// -- Project endpoints --
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

async function updateProjectRaw(id: string, updates: { name?: string; sandbox_enabled?: boolean; sandbox_network_isolation?: boolean; sandbox_image?: string; sandbox_dockerfile_path?: string; sandbox_memory_limit?: number }): Promise<Project> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updates),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update project');
    return res.json();
}

async function buildSandboxImageRaw(id: string): Promise<{ status: string; image: string; project: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${id}/sandbox/build`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to build sandbox image');
    return res.json();
}

/** Stream build progress via SSE. onLine is called for each progress line. */
export async function buildSandboxImageStream(id: string, onLine: (line: string) => void): Promise<{ status: string; image: string; project: string }> {
    const token = await ensureCsrfToken();
    const headers = new Headers();
    if (token) headers.set('X-CSRF-Token', token);

    const res = await fetch(`${API_BASE}/projects/${id}/sandbox/build`, {
        method: 'POST',
        credentials: 'include',
        headers,
    });
    if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || 'Failed to build sandbox image');
    }

    const reader = res.body?.getReader();
    if (!reader) throw new Error('Streaming not supported');

    const decoder = new TextDecoder();
    let buffer = '';
    let result: { status: string; image: string; project: string } | null = null;

    while (true) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop() || '';

        for (const line of lines) {
            if (line.startsWith('data: ')) {
                const data = line.slice(6);
                // Only parse JSON objects (final result), treat everything else as text
                if (data.startsWith('{')) {
                    try {
                        const parsed = JSON.parse(data);
                        if (parsed && typeof parsed === 'object' && 'status' in parsed) {
                            result = parsed;
                            continue;
                        }
                    } catch {}
                }
                onLine(data);
            }
        }
    }

    if (!result) throw new Error('Build completed without result');
    return result;
}

async function deleteProjectRaw(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete project');
}

export async function resetProjectStatsRaw(projectId: string, modelName: string, providerName?: string): Promise<void> {
    const params = new URLSearchParams({ model_name: modelName });
    if (providerName) params.set('provider_name', providerName);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats?${params}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to reset stats');
}

// -- Board endpoints --
async function reorderTasksRaw(projectId: string, reorders: ReorderRequest[]): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/reorder`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(reorders),
    });
    if (!res.ok) throw await apiError(res, 'Failed to reorder tasks');
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

async function updateTaskRaw(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update task');
    return res.json();
}

async function deleteTaskRaw(projectId: string, taskId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to delete task');
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

async function commitAndDoneRaw(projectId: string, taskId: string, commitMessage?: string): Promise<Task> {
    const body: Record<string, string> = {};
    if (commitMessage) body.commit_message = commitMessage;
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, 'Failed to commit and mark as done');
    return res.json();
}

async function getCommitPreviewRaw(projectId: string, taskId: string): Promise<{ commit_message: string; author_name: string; author_email: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commit-preview`, {
        method: 'GET',
    });
    if (!res.ok) throw await apiError(res, 'Failed to get commit preview');
    return res.json();
}

async function clearDoneTasksRaw(projectId: string): Promise<{ deleted: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/done`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, 'Failed to clear done tasks');
    return res.json();
}

// -- Task execution endpoints --
async function startTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to start task');
    return res.json();
}

async function startSubtaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start-subtask`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to start subtask');
    return res.json();
}

async function stopTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to stop task');
    return res.json();
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

/** Send a user message via HTTP POST. Triggers executor and returns the created message. */
async function sendTaskMessageRaw(
    projectId: string,
    taskId: string,
    content: string,
    modelName?: string,
    serverUrl?: string,
    attachments?: Array<{ mimeType: string; data: string }>,
    reasoningEffort?: string
): Promise<{ id: string; role: string; content: string; thread_id?: string }> {
    const body: Record<string, any> = { content };
    if (modelName) body.model_name = modelName;
    if (serverUrl) body.llama_server_url = serverUrl;
    if (reasoningEffort) body.reasoning_effort = reasoningEffort;
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

async function createTaskMessageRaw(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create message');
    return res.json();
}

async function deleteTaskMessagesAfterRaw(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, regenerate }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete messages');
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

async function updateTaskMessageRaw(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update message');
}

// -- Git endpoints --
async function resetWorktreeRaw(projectId: string, taskId: string, commitHash: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/reset-worktree`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ commit_hash: commitHash }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to reset worktree');
    return res.json();
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
async function updateConfigRaw(config: Config): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update config');
    return res.json();
}

// -- Thread endpoints --
async function switchTaskThreadRaw(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/switch`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to switch thread');
    return res.json();
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

async function deleteTaskThreadRaw(projectId: string, taskId: string, threadId: string): Promise<{ status: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete thread');
    return res.json();
}

// Export raw functions for queue execution
export {
    createProjectRaw,
    updateProjectRaw,
    buildSandboxImageRaw,
    deleteProjectRaw,
    reorderTasksRaw,
    createTaskRaw,
    updateTaskRaw,
    deleteTaskRaw,
    moveTaskRaw,
    commitAndDoneRaw,
    getCommitPreviewRaw,
    clearDoneTasksRaw,
    startTaskRaw,
    startSubtaskRaw,
    stopTaskRaw,
    retryTaskRaw,
    resumeTaskRaw,
    sendTaskMessageRaw,
    createTaskMessageRaw,
    deleteTaskMessagesAfterRaw,
    deleteTaskMessageRaw,
    updateTaskMessageRaw,
    resetWorktreeRaw,
    reloadPromptsRaw,
    updateConfigRaw,
    switchTaskThreadRaw,
    forkTaskThreadRaw,
    deleteTaskThreadRaw,
};
