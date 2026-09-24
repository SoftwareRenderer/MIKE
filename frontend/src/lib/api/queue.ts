import { API_BASE, apiFetch, apiError, ensureCsrfToken, generateId, isAborted, registerSetOffline } from './helpers';
import { loadQueue as loadQueueFromStorage, saveQueue as saveQueueToStorage, type QueuedAction as StoredQueuedAction } from '../storage';
import { getBoardCache, refusedSendsCacheKey, removeBoardCache, setBoardCache } from '../storage';
import type { PendingSend } from '../utils/pending-sends';
import type { Config, MemoryAlertAckPayload, Project, ProjectUpdates, Task, TaskMessage, Thread } from '../types';
import { t } from '$lib/i18n';

// Re-export QueuedAction from storage (canonical definition)
export type { StoredQueuedAction as QueuedAction };

// Register setOfflineStatus so helpers can mark app offline on fetch failures
registerSetOffline((offline: boolean) => setOfflineStatus(offline));

// =============================================================================
// Offline Action Queue
// =============================================================================
// Queues write operations when the WebSocket is disconnected.
// Persists to IndexedDB so actions survive page refresh.
// On reconnect, actions are replayed in order.
// =============================================================================

type QueuedAction = StoredQueuedAction;

/** All recognized action types for the offline queue. */
export type ActionType =
    // Task CRUD
    | 'createTask' | 'updateTask' | 'deleteTask' | 'moveTask'
    | 'commitAndDone' | 'clearDoneTasks' | 'dismissMemoryAlert'
    // Task execution
    | 'startTask' | 'startSubtask' | 'stopTask' | 'resumeTask'
    // Messages
    | 'sendTaskMessage' | 'createTaskMessage' | 'deleteTaskMessagesAfter'
    | 'deleteTaskMessage' | 'updateTaskMessage'
    // Threads
    | 'switchTaskThread' | 'forkTaskThread'
    // Projects
    | 'createProject' | 'updateProject' | 'deleteProject'
    // Config
    | 'updateConfig' | 'resetProjectStats'


let isOffline = false;
let actionQueue: QueuedAction[] = [];
let flushPromise: Promise<void> | null = null;
// Actions the server refused outright (404, 403, …) during the most recent flush. They are out of
// the queue, so "is this task still queued?" answers no for them exactly as for an applied action.
let droppedActions: QueuedAction[] = [];

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

/**
 * Drop the in-memory queue without touching storage. Sign-out pairs it with
 * clearAll(): a leftover array would be written back into the emptied database by
 * the next enqueue.
 */
export function clearQueue(): void {
    actionQueue = [];
    droppedActions = [];
    notifyQueueChanged();
}

/** Add an action to the queue and persist. */
export async function enqueueAction(action: { type: ActionType; params: Record<string, any> }): Promise<QueuedAction> {
    const item: QueuedAction = {
        ...action,
        id: generateId(),
        timestamp: Date.now(),
    };
    // A fresh write about a task supersedes the last flush's verdict on it.
    if (action.params.taskId) {
        droppedActions = droppedActions.filter(a => a.params.taskId !== action.params.taskId);
    }
    // Otherwise a refused row outlives the retry that replaced it.
    if (SEND_ACTIONS.has(action.type) && action.params.projectId && action.params.taskId) {
        void clearRefusedSends(action.params.projectId, action.params.taskId);
    }
    actionQueue.push(item);
    await saveQueue();
    return item;
}

/** Task IDs whose queued write the server refused in the most recent flush. A UI that clears a
 * "queued" marker on "no longer queued" has to ask this first. */
export function getDroppedTaskIds(): Set<string> {
    const ids = new Set<string>();
    for (const action of droppedActions) {
        if (action.params.taskId) ids.add(action.params.taskId);
    }
    return ids;
}

/** Every write the server refused — the count a report needs. */
export function getDroppedActions(): QueuedAction[] {
    return [...droppedActions];
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

// The sends the queue still owes, or the server refused. `pendingSendEntries` owns how they read.

const SEND_ACTIONS: Set<string> = new Set(['sendTaskMessage', 'createTaskMessage']);

function isSendAction(action: QueuedAction): boolean {
    return SEND_ACTIONS.has(action.type) && !!action.params?.projectId && !!action.params?.taskId;
}

function toPendingSend(action: QueuedAction): PendingSend {
    return {
        actionId: action.id,
        content: String(action.params?.content ?? ''),
        attachments: action.params?.attachments,
        timestamp: action.timestamp,
    };
}

/** The sends still waiting to be applied for one task, in the order they were written. */
export function getQueuedSends(projectId: string, taskId: string): PendingSend[] {
    return actionQueue
        .filter(a => isSendAction(a) && a.params.projectId === projectId && a.params.taskId === taskId)
        .map(toPendingSend);
}

/** The sends the server refused for one task; they have left the queue, so the verdict is stored. */
export async function getRefusedSends(projectId: string, taskId: string): Promise<PendingSend[]> {
    const stored = await getBoardCache<PendingSend[]>(refusedSendsCacheKey(projectId, taskId));
    return Array.isArray(stored) ? stored : [];
}

async function recordRefusedSend(action: QueuedAction): Promise<void> {
    const key = refusedSendsCacheKey(action.params.projectId, action.params.taskId);
    const stored = await getBoardCache<PendingSend[]>(key);
    const known = Array.isArray(stored) ? stored : [];
    if (known.some(s => s.actionId === action.id)) return;
    await setBoardCache(key, [...known, toPendingSend(action)]);
}

async function clearRefusedSends(projectId: string, taskId: string): Promise<void> {
    await removeBoardCache(refusedSendsCacheKey(projectId, taskId));
}



/** Check if an error is a transient network failure (retryable). */
export function isTransientError(e: unknown): boolean {
    // A cancellation is not a transient failure. The user stopped this request; queueing it replays
    // something they asked to stop — later, from IndexedDB, possibly after the tab is closed.
    if (isAborted(e)) return false;
    return (e as any)?.name === 'TypeError';
}

/**
 * Record what one flushed action settled: a created task's real id, and the newest state of a task the
 * action returned.
 */
function trackResult(action: QueuedAction, result: any, idMap: Map<string, string>, resolvedTasks: Map<string, any>) {
    if (action.type === 'createTask' && result && action.params.taskData?.id) {
        const optId = action.params.taskData.id;
        idMap.set(optId, result.id);
        resolvedTasks.set(optId, result);
    }
    if (result && result.id && (action.type === 'startTask' || action.type === 'moveTask' ||
        action.type === 'stopTask' || action.type === 'resumeTask' ||
        action.type === 'commitAndDone' || action.type === 'updateTask')) {
        resolvedTasks.set(result.id, result);
    }
}

/** Flush every queued action, in order.
 * @param onTransientError  Called on a retryable error; return true to stop flushing.
 */
async function doFlush(onTransientError?: (e: unknown) => boolean): Promise<Map<string, any>> {
    const idMap = new Map<string, string>();
    const resolvedTasks = new Map<string, any>();
    droppedActions = [];

    while (queueLength() > 0) {
        const action = actionQueue[0];
        try {
            const result = await executeQueuedAction(action, idMap);
            trackResult(action, result, idMap, resolvedTasks);
            actionQueue.shift();
            await saveQueue();
        } catch (e: any) {
            console.error('[Queue] Failed to flush action:', action.type, e);
            if (isTransientError(e)) {
                if (onTransientError?.(e) !== false) {
                    break; // stop flushing on transient error
                }
            } else {
                // Permanent errors (404, 403, etc.) — skip this action, don't block the rest.
                // Recorded before it leaves the queue, so the UI can say "refused" rather than
                // reading its absence as "applied".
                droppedActions.push(action);
                if (isSendAction(action)) {
                    // The verdict has to outlive the queue entry it came from.
                    await recordRefusedSend(action).catch(() => {});
                }
                actionQueue.shift();
                await saveQueue();
            }
        }
    }

    return resolvedTasks;
}

/** Flush queued actions via HTTP immediately (e.g., on page load). Independent of WebSocket state. */
export async function flushQueueNow(): Promise<void> {
    if (flushPromise) return flushPromise;
    if (queueLength() === 0) return;

    flushPromise = (async () => {
        const resolvedTasks = await doFlush();
        notifyQueueFlushed([], resolvedTasks);
        flushPromise = null;
    })();
    return flushPromise;
}

/** Replay all queued actions in order. Triggered on WebSocket reconnect. */
async function flushQueue() {
    if (flushPromise) return flushPromise;
    if (queueLength() === 0) return;

    flushPromise = (async () => {
        const wasOffline = isOffline;
        // Temporarily set online so raw functions execute directly
        isOffline = false;
        try {
            // doFlush only invokes the callback for transient errors — mark
            // offline and stop flushing; the rest stays queued.
            const resolvedTasks = await doFlush(() => {
                isOffline = true;
                return true; // stop flushing
            });
            notifyQueueFlushed([], resolvedTasks);
        } finally {
            isOffline = wasOffline;
        }
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
 * Replay queued actions onto a copy of the cached board data.
 *
 * @param forProject  The project the list belongs to. Every board-shaped action names its own project, and
 *                    one from another project belongs to that project's board — replaying it here would add a
 *                    card that is not this project's.
 */
export function applyQueuedActionsToBoard(
    tasks: Task[],
    forProject?: string,
): Task[] {
    // Map optimistic task IDs → real task IDs (resolved during flush)
    const optIdMap = new Map<string, string>();
    const result = tasks.map(t => ({ ...t })); // shallow copy

    for (const action of actionQueue) {
        const p = action.params;
        const taskId = p.taskId;
        const projectId = p.projectId;
        if (forProject && projectId && projectId !== forProject) continue;

        switch (action.type) {
            case 'createTask': {
                const taskData = p.taskData || {};
                const now = new Date().toISOString();
                const task: Task = {
                    id: taskData.id || generateId(),
                    project_id: projectId,
                    title: taskData.title || '',
                    prompt: taskData.prompt || '',
                    status: taskData.status || 'backlog',
                    execution_state: taskData.execution_state || 'idle',
                    model_name: taskData.model_name || '',
                    llama_server_url: taskData.llama_server_url || '',
                    reasoning_effort: taskData.reasoning_effort || '',
                    max_consecutive_failed_turns: taskData.max_consecutive_failed_turns ?? 5,
                    auto_commit: taskData.auto_commit ?? false,
                    auto_merge: taskData.auto_merge ?? false,
                    subtasks_auto_merge: taskData.subtasks_auto_merge ?? false,
                    operating_mode: taskData.operating_mode ?? 'act',
                    parent_task_id: taskData.parent_task_id || undefined,
                    title_generating: taskData.title_generating ?? false,
                    summarizing: taskData.summarizing ?? false,
                    current_thread_id: taskData.current_thread_id || undefined,
                    has_merge_conflict: false,
                    created_at: now,
                    updated_at: now,
                    // Never moved yet, so the new task starts at the top of its column.
                    moved_at: now,
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
                if (task) {
                    task.status = p.status;
                    task.moved_at = new Date().toISOString();
                }
                break;
            }

            case 'dismissMemoryAlert': {
                const resolvedId = optIdMap.get(taskId) || taskId;
                const task = result.find(t => t.id === resolvedId);
                if (task) task.memory_alert_ack = p.ack;
                break;
            }

            case 'startTask':
            case 'startSubtask':
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
            await sendTaskMessageRaw({ ...p, taskId } as SendTaskMessagePayload);
            break;
        // No longer written — an edited message is one send carrying `replaceMessageId` — but kept so a queue
        // an older build left on disk still lands its row.
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
        case 'dismissMemoryAlert':
            await dismissMemoryAlertRaw(p.projectId, taskId, p.ack);
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
        case 'resumeTask':
            await resumeTaskRaw(p.projectId, taskId);
            break;
        case 'createProject':
            await createProjectRaw(p.path, p.sandboxEnabled, p.gitUrl);
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
        case 'updateConfig':
            await updateConfigRaw(p.config);
            break;
        case 'resetProjectStats':
            await resetProjectStatsRaw(p.projectId, p.modelName, p.providerName);
            break;
        default: {
            // The queue is persisted, so a type deleted from ActionType in a later build can still be
            // read from disk. It leaves the queue unexecuted rather than blocking the flush.
            const _exhaustive: never = action.type as never;
            console.warn('[Queue] Unknown action type:', _exhaustive);
        }
    }
}

/**
 * Enqueue an action and return the optimistic result, tagged with the queued action's id.
 */
export async function queueWrite<T>(type: ActionType, params: Record<string, any>, optimistic: T): Promise<T> {
    const queued = await enqueueAction({ type, params });
    return { ...optimistic, queued: true, actionId: queued.id } as T;
}

// =============================================================================
// Raw HTTP functions (imported by domain modules)
// =============================================================================

// -- Project endpoints --
async function createProjectRaw(path: string, sandboxEnabled = false, gitUrl?: string): Promise<Project> {
    const body: Record<string, unknown> = { path, sandbox_enabled: sandboxEnabled };
    if (gitUrl) {
        body.git_url = gitUrl;
    }
    if (sandboxEnabled) {
        body.sandbox_network_isolation = true;
    }
    const res = await apiFetch(`${API_BASE}/projects`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, t('api.createProject'));
    return res.json();
}

async function updateProjectRaw(id: string, updates: ProjectUpdates): Promise<Project> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updates),
    });
    if (!res.ok) throw await apiError(res, t('api.updateProject'));
    return res.json();
}

/** Stream build progress via SSE. onLine is called for each progress line.
 *  opts.noCache skips the container build layer cache for this build only. */
export async function buildSandboxImageStream(
    id: string,
    onLine: (line: string) => void,
    opts?: { noCache?: boolean },
): Promise<{ status: string; image: string; project: string }> {
    const token = await ensureCsrfToken();
    const headers = new Headers();
    if (token) headers.set('X-CSRF-Token', token);

    const url = `${API_BASE}/projects/${id}/sandbox/build${opts?.noCache ? '?no_cache=1' : ''}`;
    const res = await fetch(url, {
        method: 'POST',
        credentials: 'include',
        headers,
    });
    if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || t('api.buildSandboxImage'));
    }

    const reader = res.body?.getReader();
    if (!reader) throw new Error(t('api.streamingNotSupported'));

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

    if (!result) throw new Error(t('api.buildCompletedWithoutResult'));
    return result;
}

async function deleteProjectRaw(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.deleteProject'));
}

export async function resetProjectStatsRaw(projectId: string, modelName: string, providerName?: string): Promise<void> {
    const params = new URLSearchParams({ model_name: modelName });
    if (providerName) params.set('provider_name', providerName);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats?${params}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.resetStats'));
}

// -- Task CRUD endpoints --
async function createTaskRaw(projectId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, t('api.createTask'));
    return res.json();
}

async function updateTaskRaw(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, t('api.updateTask'));
    return res.json();
}

async function deleteTaskRaw(projectId: string, taskId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.deleteTask'));
}

async function moveTaskRaw(projectId: string, taskId: string, status: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
    });
    if (!res.ok) throw await apiError(res, t('api.moveTask'));
    return res.json();
}

/** Record the dismissal of a task's out-of-memory alert on its row, where every device reads it. */
async function dismissMemoryAlertRaw(projectId: string, taskId: string, ack: MemoryAlertAckPayload): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/memory-alert-ack`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(ack),
    });
    if (!res.ok) throw await apiError(res, t('api.dismissMemoryAlert'));
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
    if (!res.ok) throw await apiError(res, t('api.commitAndDone'));
    return res.json();
}

async function clearDoneTasksRaw(projectId: string): Promise<{ deleted: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/done`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.clearDoneTasks'));
    return res.json();
}

// -- Task execution endpoints --
async function initTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/init`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.initializeTask'));
    return res.json();
}

async function startTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.startTask'));
    return res.json();
}

async function startSubtaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start-subtask`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.startSubtask'));
    return res.json();
}

async function stopTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.stopTask'));
    return res.json();
}

async function resumeTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/resume`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.resumeTask'));
    return res.json();
}

/**
 * What one queued or live `sendTaskMessage` carries.
 *
 * The key names are the persisted IndexedDB payload, not a call signature: an action written by an older
 * build is replayed against this code, so a rename or a nesting change strands that send in the queue.
 */
export interface SendTaskMessagePayload {
    projectId: string;
    taskId: string;
    content: string;
    modelName?: string;
    serverUrl?: string;
    attachments?: Array<{ mimeType: string; data: string }>;
    reasoningEffort?: string;
    regenerate?: boolean;
    /** Take the thread back to this row before the message lands, so an edit replaces the turns that answered
     * the old text. The server refuses (409) unless it is its thread's last user row, which is what makes a
     * queued edit safe to replay late. */
    replaceMessageId?: string;
}

/** What a live or replayed send answers with. A queued send names the queue action holding it. */
export interface SendResult {
    id: string;
    role: string;
    content: string;
    thread_id?: string;
    queued?: boolean;
    actionId?: string;
}

/** Send a user message via HTTP POST. Triggers executor and returns the created message. */
async function sendTaskMessageRaw(payload: SendTaskMessagePayload): Promise<SendResult> {
    const { projectId, taskId, content, modelName, serverUrl, attachments, reasoningEffort, regenerate, replaceMessageId } = payload;
    const body: Record<string, any> = { content };
    if (modelName) body.model_name = modelName;
    if (serverUrl) body.llama_server_url = serverUrl;
    if (reasoningEffort) body.reasoning_effort = reasoningEffort;
    if (regenerate) body.regenerate = regenerate;
    if (replaceMessageId) body.replace_message_id = replaceMessageId;
    if (attachments && attachments.length > 0) {
        body.attachments = JSON.stringify(attachments);
    }
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/send`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, t('api.sendMessage'));
    return res.json();
}

async function createTaskMessageRaw(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, content }),
    });
    if (!res.ok) throw await apiError(res, t('api.createMessage'));
    return res.json();
}

async function deleteTaskMessagesAfterRaw(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, regenerate }),
    });
    if (!res.ok) throw await apiError(res, t('api.deleteMessages'));
}

/** Delete a single message by ID (e.g., dismissing an error). Does not delete messages after it. */
async function deleteTaskMessageRaw(projectId: string, taskId: string, messageId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, delete_single: true }),
    });
    if (!res.ok) throw await apiError(res, t('api.deleteMessage'));
}

async function updateTaskMessageRaw(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) throw await apiError(res, t('api.updateMessage'));
}

// -- Configuration endpoints --
async function updateConfigRaw(config: Config): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config),
    });
    if (!res.ok) throw await apiError(res, t('api.updateConfig'));
    return res.json();
}

// -- Thread endpoints --
async function switchTaskThreadRaw(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/switch`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, t('api.switchThread'));
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
    if (!res.ok) throw await apiError(res, t('api.forkThread'));
    return res.json();
}

// Export raw functions for queue execution
export {
    createProjectRaw,
    updateProjectRaw,
    deleteProjectRaw,
    createTaskRaw,
    updateTaskRaw,
    deleteTaskRaw,
    moveTaskRaw,
    dismissMemoryAlertRaw,
    commitAndDoneRaw,
    clearDoneTasksRaw,
    initTaskRaw,
    startTaskRaw,
    startSubtaskRaw,
    stopTaskRaw,
    resumeTaskRaw,
    sendTaskMessageRaw,
    createTaskMessageRaw,
    deleteTaskMessagesAfterRaw,
    deleteTaskMessageRaw,
    updateTaskMessageRaw,
    updateConfigRaw,
    switchTaskThreadRaw,
    forkTaskThreadRaw,
};
