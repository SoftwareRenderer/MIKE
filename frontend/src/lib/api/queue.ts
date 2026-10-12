import { generateId, isAborted, registerSetHttpReachable } from './helpers';
import { QUEUE_RETRY_BASE_DELAY_MS, RECONNECT_MAX_DELAY_MS } from '$lib/constants';
import { loadQueue as loadQueueFromStorage, updateQueue as updateQueueRecord, seizeQueue, claimQueue, releaseQueue, type QueuedAction as StoredQueuedAction } from '../storage';
import { shouldReplay } from '$lib/utils/queue-ownership';
import { getBoardCache, refusedSendsCacheKey, removeBoardCache, setBoardCache } from '../storage';
import type { PendingSend } from '../utils/pending-sends';
import type { Task } from '../types';
// The endpoint calls a queued action replays live under ./raw/, and that layer never imports this one:
// the dependency between the two runs in one direction.
import { createProjectRaw, deleteProjectRaw, resetProjectStatsRaw, updateProjectRaw } from './raw/projects';
import {
    clearDoneTasksRaw,
    commitAndDoneRaw,
    createTaskRaw,
    deleteTaskMessageRaw,
    deleteTaskMessagesAfterRaw,
    deleteTaskRaw,
    dismissMemoryAlertRaw,
    moveTaskRaw,
    resumeTaskRaw,
    sendTaskMessageRaw,
    startSubtaskRaw,
    startTaskRaw,
    stopTaskRaw,
    updateTaskMessageRaw,
    updateTaskRaw,
    type SendTaskMessagePayload,
} from './raw/tasks';
import { updateConfigRaw } from './raw/config';
import { forkTaskThreadRaw, switchTaskThreadRaw } from './raw/threads';

// Re-export QueuedAction from storage (canonical definition)
export type { StoredQueuedAction as QueuedAction };

registerSetHttpReachable((reachable: boolean) => setHttpReachable(reachable));

// =============================================================================
// Offline Action Queue
// =============================================================================
// Queues write operations while the app cannot reach the server, as `getOfflineStatus` decides. Persists
// to IndexedDB so actions survive a page refresh, and replays them in order once nothing holds them.
// =============================================================================

type QueuedAction = StoredQueuedAction;

/** All recognized action types for the offline queue. */
type ActionType =
    // Task CRUD
    | 'createTask' | 'updateTask' | 'deleteTask' | 'moveTask'
    | 'commitAndDone' | 'clearDoneTasks' | 'dismissMemoryAlert'
    // Task execution
    | 'startTask' | 'startSubtask' | 'stopTask' | 'resumeTask'
    // Messages
    | 'sendTaskMessage' | 'deleteTaskMessagesAfter'
    | 'deleteTaskMessage' | 'updateTaskMessage'
    // Threads
    | 'switchTaskThread' | 'forkTaskThread'
    // Projects
    | 'createProject' | 'updateProject' | 'deleteProject'
    // Config
    | 'updateConfig' | 'resetProjectStats'


// Two independent reasons to hold a write: what the socket says, and what this app's own requests say.
// Each is released only by its own evidence, because an answered request says nothing about a socket
// that is gone, and a socket that is up says nothing about a server that stopped answering HTTP.
let declaredOffline = false;
let httpUnreachable = false;
/** A replay in flight: writes go out with it instead of queueing behind it, and a reason raised now wins. */
let replaying = false;
// How soon a queue replays after one of its own sends failed. An answered request says the server is
// reachable, not that this action will land, and every replay end provokes a read that answers.
let replayRetryDelayMs = 0;
let replayHeldUntil = 0;
let actionQueue: QueuedAction[] = [];
let flushPromise: Promise<void> | null = null;

// The record is one per browser profile, so one window replays it and the rest only read it — two senders would
// send each write twice. utils/queue-ownership.ts holds the rule; storage.ts holds the record.
const windowId = generateId();
let ownsReplay = false;

/** The notice windows exchange so a reader follows without polling. */
type QueueNotice = 'changed' | 'claimed' | 'released';
const queueChannel: BroadcastChannel | null = typeof BroadcastChannel === 'function'
    ? new BroadcastChannel('mike-queue')
    : null;

function announce(notice: QueueNotice): void {
    queueChannel?.postMessage({ notice, windowId });
}

/** Take the replay at boot, from whoever held it: the newest window sends. Two windows booting together both
 * write, so the read back decides, and until the other notices, one action can go out twice. */
async function takeReplayAtBoot(): Promise<void> {
    ownsReplay = await seizeQueue({ windowId, takenAt: Date.now() });
    announce('claimed');
}

/** Ask the record whether this window still sends: its own name, or nobody's. Taking a record an owner let go is
 * worth announcing, or readers never learn who took it. */
async function checkReplay(): Promise<boolean> {
    const heldBefore = ownsReplay;
    ownsReplay = await claimQueue({ windowId, takenAt: Date.now() });
    if (ownsReplay && !heldBefore) announce('claimed');
    return ownsReplay;
}

/** Re-read the record, so a reader's pending rows and badges describe the shared queue. A window the browser
 * froze missed the notices, so the tab-return watcher asks for this on its way back. */
export async function adoptQueue(): Promise<void> {
    actionQueue = await loadQueueFromStorage();
    notifyQueueChanged();
}

/** A `claimed` ends this window's sending. Every notice is worth a re-read, and an empty record left behind by a
 * closed owner is this window's to pick up. */
queueChannel?.addEventListener('message', (event: MessageEvent) => {
    const data = event.data as { notice?: QueueNotice; windowId?: string };
    if (!data || data.windowId === windowId) return;
    if (data.notice === 'claimed') ownsReplay = false;
    void rereadAfterNotice();
});

/**
 * Re-read once for a burst of notices rather than once per notice: a replay that lands N actions posts N of them.
 * A notice arriving mid-read sets a second pass, so the last one always gets a read that started after it.
 */
let rereading: Promise<void> | null = null;
let rereadAgain = false;
function rereadAfterNotice(): Promise<void> {
    if (rereading) {
        rereadAgain = true;
        return rereading;
    }
    const pass = (async () => {
        do {
            rereadAgain = false;
            await adoptQueue();
            sendQueueIfReachable();
        } while (rereadAgain);
    })();
    rereading = pass;
    const done = () => { if (rereading === pass) rereading = null; };
    pass.then(done, done);
    return pass;
}

// An owner that vanished would leave the queue unsent, so it hands the record back on the way out. A page in the
// back-forward cache is frozen rather than gone, so it keeps the claim.
if (typeof window !== 'undefined') {
    window.addEventListener('pagehide', (event: PageTransitionEvent) => {
        if (!ownsReplay || event.persisted) return;
        ownsReplay = false;
        void releaseQueue(windowId).then(() => announce('released'));
    });
}
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

/** Whether a write should be queued instead of sent. */
export function getOfflineStatus(): boolean {
    return !replaying && (declaredOffline || httpUnreachable);
}

/** Set when the socket drops, the browser reports offline, or the boot probe gets no answer. */
export function setOfflineStatus(offline: boolean) {
    declaredOffline = offline;
    if (offline) replaying = false;
    sendQueueIfReachable();
}

/** The HTTP reason: no answer is unreachable, and any answer of any status is not. `apiFetch` is its
 * only writer, through the seam above. */
function setHttpReachable(reachable: boolean) {
    httpUnreachable = !reachable;
    if (!reachable) replaying = false;
    sendQueueIfReachable();
}

/** The queue goes out when no reason holds it and its pace is spent. The cheap checks come first because a
 * window that only reads still answers this on every reachability flip. */
function sendQueueIfReachable() {
    if (!shouldReplay({
        hasWork: queueLength() > 0,
        held: getOfflineStatus(),
        heldUntil: replayHeldUntil,
        now: Date.now(),
    })) return;
    void startReplay();
}

/** The one replay this window may run. The handle and the in-flight flag are taken before the record is read, so
 * a caller waiting for the queue to settle waits for that read too. */
function startReplay(): Promise<void> {
    if (!flushPromise) {
        replaying = true;
        flushPromise = replayIfOwned().finally(() => {
            replaying = false;
            flushPromise = null;
        });
    }
    return flushPromise;
}

/** Take the replay if the record allows it; a reader waits for the owner. */
async function replayIfOwned(): Promise<void> {
    try {
        if (!(await checkReplay())) return;
        if (getOfflineStatus() || queueLength() === 0) return;
        const resolvedTasks = await doFlush(stopAtFirstTransientError);
        notifyQueueFlushed([], resolvedTasks);
    } finally {
        noteReplayDrained();
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

/**
 * Read the record, then take the replay: this window is the newest, so it sends and an older one goes quiet.
 */
export async function loadQueue(): Promise<QueuedAction[]> {
    const loaded = await loadQueueFromStorage();
    actionQueue = Array.isArray(loaded) ? loaded : [];
    await takeReplayAtBoot();
    return actionQueue;
}

/** Apply a change to the shared record and keep what it now holds as this window's copy. A change is stated
 * against the record because every window shares it. */
async function changeQueue(change: (held: QueuedAction[]) => QueuedAction[]): Promise<void> {
    actionQueue = await updateQueueRecord(change, actionQueue);
    announce('changed');
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
    await changeQueue(held => [...held, item]);
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

const SEND_ACTIONS: Set<string> = new Set(['sendTaskMessage']);

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
        // A newer window took the claim: the rest of the record is its to replay.
        if (!ownsReplay) break;
        const action = actionQueue[0];
        try {
            const result = await executeQueuedAction(action, idMap);
            trackResult(action, result, idMap, resolvedTasks);
            // Out by identity: the record may have grown since the send, so the head is not this action.
            await changeQueue(held => held.filter(a => a.id !== action.id));
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
                await changeQueue(held => held.filter(a => a.id !== action.id));
            }
        }
    }

    return resolvedTasks;
}

/** Boot's own attempt: the record goes out without waiting for the socket, through the same door as any replay. */
export async function flushQueueNow(): Promise<void> {
    if (queueLength() === 0) return;
    return startReplay();
}

/** Stop the replay at the first retryable error, and hold the queue before trying again; a refused action
 * is dropped by `doFlush` itself. */
function stopAtFirstTransientError() {
    replayRetryDelayMs = replayRetryDelayMs === 0
        ? QUEUE_RETRY_BASE_DELAY_MS
        : Math.min(replayRetryDelayMs * 2, RECONNECT_MAX_DELAY_MS);
    replayHeldUntil = Date.now() + replayRetryDelayMs;
    return true;
}

/** A replay that applied everything waits at the base pace again. */
function noteReplayDrained() {
    if (queueLength() > 0) return;
    replayRetryDelayMs = 0;
    replayHeldUntil = 0;
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
            return createTaskRaw(p.projectId, p.taskData, p.clientToken);
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
            await forkTaskThreadRaw(p.projectId, taskId, p.fromThreadId, p.fromMessageId, p.newContent, p.clientToken);
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
 * `params` carries the write's `clientToken`, which the replay sends again — see `writeOrQueue`.
 */
export async function queueWrite<T>(type: ActionType, params: Record<string, any>, optimistic: T): Promise<T> {
    const queued = await enqueueAction({ type, params });
    return { ...optimistic, queued: true, actionId: queued.id } as T;
}

/**
 * The offline promise for one write: a request the server answered is answered by the server, and a request
 * nobody answered is kept and sent when the connection returns. A refusal of any status *is* an answer and goes
 * back to the caller, because a write the server refused must not come back out of IndexedDB behind the
 * person's back; a cancellation is not a failure, which is what `isTransientError` separates. Queuing from the
 * catch is safe because `apiFetch` has already opened the queue by the time the request fails.
 */
export async function writeOrQueue<T>(
    action: { type: ActionType; params: Record<string, any> },
    live: (clientToken: string) => Promise<T>,
    optimistic?: () => T,
): Promise<T> {
    // Minted before the write sets out, so the live attempt and the replay of it are one write to the server.
    // Only a send, a create and a fork are guarded by it; the rest carry it unused.
    const clientToken = generateId();
    const params = { ...action.params, clientToken };
    const keep = async (): Promise<T> =>
        optimistic === undefined
            ? await enqueueAction({ type: action.type, params }).then(() => undefined as T)
            : await queueWrite(action.type, params, optimistic());
    if (getOfflineStatus()) return keep();
    try {
        return await live(clientToken);
    } catch (e) {
        if (!isTransientError(e)) throw e;
        return keep();
    }
}
