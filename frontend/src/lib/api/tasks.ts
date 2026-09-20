import type { MemoryAlertAckPayload, Task, TaskMessage } from '../types';
import { API_BASE, apiFetch, apiError, generateId } from './helpers';
import { setBoardCache, getBoardCache, messagesCacheKey, messageFullCacheKey } from '../storage';
import { markCached } from './cache';
import { REVIEW_STATUS } from '../utils/review-notify';
import { reviewNotifier } from '../services/review-notifications.svelte';
import {
    getOfflineStatus,
    enqueueAction,
    queueWrite,
    isTransientError,
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
} from './queue';
import { t } from '$lib/i18n';

// -- Task CRUD endpoints --

/** Create a new task in a project. Queued when offline. */
export async function createTask(projectId: string, task: Partial<Task>): Promise<Task> {
    if (getOfflineStatus()) {
        const id = generateId();
        return await queueWrite('createTask', { projectId, taskData: task }, { ...task, id, status: task.status ?? 'backlog', created_at: new Date().toISOString() } as Task);
    }
    return createTaskRaw(projectId, task);
}

/** Fetch a single task by ID. */
export async function getTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`);
    if (!res.ok) throw await apiError(res, t('api.taskNotFound'));
    return res.json();
}

/** Update task metadata (title, model, settings). Queued when offline. */
export async function updateTask(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('updateTask', { projectId, taskId, taskData: task }, { id: taskId, ...task } as Task);
    }
    return updateTaskRaw(projectId, taskId, task);
}

/** Delete a task and its worktree. Queued when offline. */
export async function deleteTask(projectId: string, taskId: string): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'deleteTask', params: { projectId, taskId } });
        return;
    }
    return deleteTaskRaw(projectId, taskId);
}

/** Move a task to a different column (status). Queued when offline. */
export async function moveTask(projectId: string, taskId: string, status: string): Promise<Task> {
    // Every client-side move comes through here, so this is where a self-inflicted arrival is marked.
    if (status === REVIEW_STATUS) reviewNotifier.noteSelfMove(taskId);
    if (getOfflineStatus()) {
        // Carry moved_at so a queued move still lands on top of its column offline
        return await queueWrite('moveTask', { projectId, taskId, status }, { id: taskId, status, moved_at: new Date().toISOString() } as Task);
    }
    return moveTaskRaw(projectId, taskId, status);
}

/** Commit changes and mark a task as done. Queued when offline. */
export async function commitAndDone(projectId: string, taskId: string, commitMessage?: string): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('commitAndDone', { projectId, taskId }, { id: taskId, status: 'done', moved_at: new Date().toISOString() } as Task);
    }
    return commitAndDoneRaw(projectId, taskId, commitMessage);
}

/** Dismiss this task's memory alert. Queued when offline, like any other write. */
export async function dismissMemoryAlert(projectId: string, taskId: string, ack: MemoryAlertAckPayload): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('dismissMemoryAlert', { projectId, taskId, ack }, { id: taskId, memory_alert_ack: ack } as Task);
    }
    return dismissMemoryAlertRaw(projectId, taskId, ack);
}

/** Delete all done tasks in a project. Queued when offline. */
export async function clearDoneTasks(projectId: string): Promise<{ deleted: number }> {
    if (getOfflineStatus()) {
        return await queueWrite('clearDoneTasks', { projectId }, { deleted: 0 });
    }
    return clearDoneTasksRaw(projectId);
}

// -- Task execution endpoints --

/** Initialize a task: create its worktree and branch without starting LLM execution.
 * Enables the terminal and file explorer before the task is started. Idempotent. */
export async function initTask(projectId: string, taskId: string): Promise<Task> {
    return initTaskRaw(projectId, taskId);
}

/** Start task execution. Creates a worktree and begins the LLM loop. Queued when offline. */
export async function startTask(projectId: string, taskId: string): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('startTask', { projectId, taskId }, { id: taskId, status: 'in_progress' } as Task);
    }
    return startTaskRaw(projectId, taskId);
}

/** Start a subtask (child task) execution. Queued when offline. */
export async function startSubtask(projectId: string, taskId: string): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('startSubtask', { projectId, taskId }, { id: taskId, status: 'in_progress' } as Task);
    }
    return startSubtaskRaw(projectId, taskId);
}

/** Stop (pause) a running task. Queued when offline. */
export async function stopTask(projectId: string, taskId: string): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('stopTask', { projectId, taskId }, { id: taskId, execution_state: 'paused' } as Task);
    }
    return stopTaskRaw(projectId, taskId);
}

/** Resume a stopped task. Queued when offline. */
export async function resumeTask(projectId: string, taskId: string): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('resumeTask', { projectId, taskId }, { id: taskId, status: 'in_progress' } as Task);
    }
    return resumeTaskRaw(projectId, taskId);
}

// -- Message endpoints --

/** Send a user message via HTTP POST. Triggers executor and returns the created message. Supports image attachments. Queued when offline or on network failure. */
export async function sendTaskMessage(
    projectId: string,
    taskId: string,
    content: string,
    modelName?: string,
    serverUrl?: string,
    attachments?: Array<{ mimeType: string; data: string }>,
    reasoningEffort?: string,
    regenerate = false
): Promise<{ id: string; role: string; content: string; thread_id?: string; queued?: boolean; actionId?: string }> {
    // Queue the message when offline or on network failure so it isn't lost.
    const queueMessage = () => {
        const id = generateId();
        return queueWrite('sendTaskMessage', { projectId, taskId, content, modelName, serverUrl, attachments, reasoningEffort, regenerate }, { id, role: 'user', content });
    };
    if (getOfflineStatus()) {
        return await queueMessage();
    }
    try {
        return await sendTaskMessageRaw(projectId, taskId, content, modelName, serverUrl, attachments, reasoningEffort, regenerate);
    } catch (e) {
        // Network failure — the request never reached the server — so queue the message instead of
        // losing it. A cancellation is not transient (isTransientError says so): the user stopped this
        // send, and a queued copy would replay it after they left. Server refusals still propagate.
        if (isTransientError(e)) {
            return await queueMessage();
        }
        throw e;
    }
}

/** One window of a task's conversation, plus how to ask for the older rows. */
export interface MessageWindow {
    messages: TaskMessage[];
    hasMore: boolean;
    /** Cursor for the next-older window; empty when this window reaches the first row. */
    before: string;
}

/** Fetch the newest window of a task's conversation, or an older one by cursor.
 * Always tries the network first — the isOffline flag tracks WebSocket state,
 * not HTTP availability. On mobile wake (tab resume), the WebSocket may be
 * disconnected but HTTP still works, so we must not short-circuit to cache. */
export async function getTaskMessages(projectId: string, taskId: string, before = ''): Promise<MessageWindow> {
    const query = before ? `?before=${encodeURIComponent(before)}` : '';
    try {
        const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages${query}`);
        if (!res.ok) throw await apiError(res, t('api.fetchMessages'));
        const window: MessageWindow = {
            messages: await res.json(),
            hasMore: res.headers.get('X-Messages-More') === 'true',
            before: res.headers.get('X-Messages-Before') ?? '',
        };
        // Only the newest window is worth caching: paging needs the network anyway, and an older page
        // left in the cache is what an offline reload would open on.
        if (!before) setBoardCache(messagesCacheKey(projectId, taskId), window).catch(() => {});
        return window;
    } catch (e) {
        const cached = await getBoardCache<MessageWindow>(messagesCacheKey(projectId, taskId));
        if (cached) return markCached(cached);
        throw e;
    }
}

/** Fetch a single message with full content (uncompressed reasoning_content, tool_calls).
 * Falls back to cached copy on failure. */
export async function getTaskMessageFull(projectId: string, taskId: string, messageId: string): Promise<TaskMessage> {
    try {
        const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}?full=1`);
        if (!res.ok) throw await apiError(res, t('api.fetchMessage'));
        const data = await res.json();
        // Cache fire-and-forget
        setBoardCache(messageFullCacheKey(projectId, taskId, messageId), data).catch(() => {});
        return data;
    } catch (e) {
        const cached = await getBoardCache<TaskMessage>(messageFullCacheKey(projectId, taskId, messageId));
        if (cached) return markCached(cached);
        throw e;
    }
}

/** Create a message directly (e.g., for system messages). Queued when offline. */
export async function createTaskMessage(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    if (getOfflineStatus()) {
        const id = generateId();
        return await queueWrite('createTaskMessage', { projectId, taskId, role, content }, { id, task_id: taskId, thread_id: '', role, content, created_at: new Date().toISOString() });
    }
    return createTaskMessageRaw(projectId, taskId, role, content);
}

/** Delete a message and all messages after it. Set regenerate=true to trigger LLM regeneration. Queued when offline. */
export async function deleteTaskMessagesAfter(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'deleteTaskMessagesAfter', params: { projectId, taskId, messageId, regenerate } });
        return;
    }
    return deleteTaskMessagesAfterRaw(projectId, taskId, messageId, regenerate);
}

/** Delete a single message by ID (e.g., dismissing an error). Does not delete messages after it. Queued when offline. */
export async function deleteTaskMessage(projectId: string, taskId: string, messageId: string): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'deleteTaskMessage', params: { projectId, taskId, messageId } });
        return;
    }
    return deleteTaskMessageRaw(projectId, taskId, messageId);
}

/** Check if deleting a message would also delete a fork branch. */
export async function checkMessageDelete(projectId: string, taskId: string, messageId: string): Promise<{ will_delete_branch: boolean; branch_name: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`);
    if (!res.ok) throw await apiError(res, t('api.checkMessageDelete'));
    return res.json();
}

/** Edit a message's content. Queued when offline. */
export async function updateTaskMessage(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'updateTaskMessage', params: { projectId, taskId, messageId, content } });
        return;
    }
    return updateTaskMessageRaw(projectId, taskId, messageId, content);
}

/** Manually trigger context summarization for a task. Pass an AbortSignal to allow cancellation. */
export async function summarizeTask(projectId: string, taskId: string, signal?: AbortSignal): Promise<{ summary: string; old_count: number; new_count: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/summarize`, { method: 'POST', signal });
    if (!res.ok) throw await apiError(res, t('api.summarizeTask'));
    return res.json();
}

/** Trigger Pi agent context compaction via RPC. Only works for Pi tasks. */
export async function compactTask(projectId: string, taskId: string): Promise<{ status: string; summary: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/compact`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.compactTask'));
    return res.json();
}
