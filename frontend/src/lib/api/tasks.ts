import type { ChatTurn, MemoryAlertAckPayload, Task, TaskMessage, TaskSyncDigest } from '../types';
import { API_BASE, apiFetch, apiError, generateId, readEventStream } from './helpers';
import { setBoardCache, getBoardCache, messagesCacheKey, messageFullCacheKey } from '../storage';
import { mergeCachedRows } from '../utils/chat-window';
import { markCached } from './cache';
import { REVIEW_STATUS } from '../utils/review-notify';
import { reviewNotifier } from '../services/review-notifications.svelte';
import { getOfflineStatus, queueWrite, writeOrQueue } from './queue';
import {
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
    type SendResult,
    type SendTaskMessagePayload,
    deleteTaskMessagesAfterRaw,
    deleteTaskMessageRaw,
    updateTaskMessageRaw,
} from './raw/tasks';
import { t } from '$lib/i18n';

// -- Task CRUD endpoints --

// A write that got no answer is queued and replayed: that is `writeOrQueue` ($lib/api/queue.ts), and a function
// below that calls it does not say so again.

/**
 * Create a new task in a project, queued when offline. Deliberately not `writeOrQueue`, and the reason is the
 * id: a create fixes its id before it sets out, so the card drawn on the board and the action held in the queue
 * are the same task — a decision `CreateTaskModal` owns (docs/reconnection.md). That id is also the write's
 * identity on the wire, so a create whose answer was lost is not a second task when it is tried again.
 */
export async function createTask(projectId: string, task: Partial<Task>, clientToken = generateId()): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('createTask', { projectId, taskData: task, clientToken }, { ...task, id: clientToken, status: task.status ?? 'backlog', created_at: new Date().toISOString() } as Task);
    }
    return createTaskRaw(projectId, task, clientToken);
}

/** Fetch a single task by ID. */
export async function getTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`);
    if (!res.ok) throw await apiError(res, t('api.taskNotFound'));
    return res.json();
}

/**
 * The digest a window asks for when it comes back. Never read from the offline mirror: a stored digest would
 * turn "could not ask" into "nothing moved".
 */
export async function getTaskSync(projectId: string, taskId: string): Promise<TaskSyncDigest> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/sync`);
    if (!res.ok) throw await apiError(res, t('api.fetchTaskSync'));
    return res.json();
}

/** Update task metadata (title, model, settings). */
export async function updateTask(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    return writeOrQueue(
        { type: 'updateTask', params: { projectId, taskId, taskData: task } },
        () => updateTaskRaw(projectId, taskId, task),
        () => ({ id: taskId, ...task } as Task),
    );
}

/** Delete a task and its worktree. */
export async function deleteTask(projectId: string, taskId: string): Promise<void> {
    await writeOrQueue({ type: 'deleteTask', params: { projectId, taskId } }, () => deleteTaskRaw(projectId, taskId));
}

/** Move a task to a different column (status). */
export async function moveTask(projectId: string, taskId: string, status: string): Promise<Task> {
    // Every client-side move comes through here, so this is where a self-inflicted arrival is marked.
    if (status === REVIEW_STATUS) reviewNotifier.noteSelfMove(taskId);
    // moved_at is carried so a queued move still lands on top of its column offline
    return writeOrQueue(
        { type: 'moveTask', params: { projectId, taskId, status } },
        () => moveTaskRaw(projectId, taskId, status),
        () => ({ id: taskId, status, moved_at: new Date().toISOString() } as Task),
    );
}

/** Commit changes and mark a task as done. */
export async function commitAndDone(projectId: string, taskId: string, commitMessage?: string): Promise<Task> {
    return writeOrQueue(
        { type: 'commitAndDone', params: { projectId, taskId } },
        () => commitAndDoneRaw(projectId, taskId, commitMessage),
        () => ({ id: taskId, status: 'done', moved_at: new Date().toISOString() } as Task),
    );
}

/** One frame of the commit-message preview stream, with the fields that event carries. */
export type CommitPreviewFrame =
    | { event: 'metadata'; authorName: string; authorEmail: string; mergeTarget: string }
    | { event: 'token'; content: string }
    | { event: 'prompt_progress'; percent: number }
    | { event: 'done'; content: string }
    | { event: 'error'; error: string };

/**
 * Stream the commit message this task's changes deserve, frame by frame, until the server sends its
 * `done` or `error` frame. `signal` is the only bound on the wait: generating the message is a model
 * call, and a slow one is still the answer the dialog waits for.
 *
 * The wire events and their payloads are `writeSSE`'s in `backend/exec/merge.go`, plus the `metadata`
 * frame `HandleCommitPreviewStream` sends first. An event this function does not know is skipped rather
 * than fatal, and so is a frame whose data is not a JSON object. This stays a plain `fetch` rather than
 * `apiFetch`: a preview that never arrived costs the dialog a log line, not the app's offline flag.
 */
export async function* streamCommitPreview(
    projectId: string,
    taskId: string,
    signal?: AbortSignal,
): AsyncGenerator<CommitPreviewFrame> {
    const res = await fetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/commit-preview/stream`, {
        method: 'GET',
        credentials: 'include',
        signal,
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);

    for await (const frame of readEventStream(res)) {
        let payload: unknown;
        try {
            payload = JSON.parse(frame.data);
        } catch {
            continue;
        }
        if (payload === null || typeof payload !== 'object') continue;
        const fields = payload as Record<string, unknown>;

        switch (frame.event) {
            case 'metadata':
                yield {
                    event: 'metadata',
                    authorName: String(fields.author_name ?? ''),
                    authorEmail: String(fields.author_email ?? ''),
                    mergeTarget: String(fields.merge_target ?? ''),
                };
                break;
            case 'token':
                yield { event: 'token', content: String(fields.content ?? '') };
                break;
            case 'prompt_progress':
                yield { event: 'prompt_progress', percent: Number(fields.percent ?? 0) };
                break;
            case 'done':
                yield { event: 'done', content: String(fields.content ?? '') };
                break;
            case 'error':
                yield { event: 'error', error: String(fields.error ?? '') };
                break;
        }
    }
}

/** Dismiss this task's memory alert. */
export async function dismissMemoryAlert(projectId: string, taskId: string, ack: MemoryAlertAckPayload): Promise<Task> {
    return writeOrQueue(
        { type: 'dismissMemoryAlert', params: { projectId, taskId, ack } },
        () => dismissMemoryAlertRaw(projectId, taskId, ack),
        () => ({ id: taskId, memory_alert_ack: ack } as Task),
    );
}

/** Delete all done tasks in a project. */
export async function clearDoneTasks(projectId: string): Promise<{ deleted: number }> {
    return writeOrQueue({ type: 'clearDoneTasks', params: { projectId } }, () => clearDoneTasksRaw(projectId), () => ({ deleted: 0 }));
}

// -- Task execution endpoints --

/** Initialize a task: create its worktree and branch without starting LLM execution.
 * Enables the terminal and file explorer before the task is started. Idempotent. */
export async function initTask(projectId: string, taskId: string): Promise<Task> {
    return initTaskRaw(projectId, taskId);
}

/** Start task execution. Creates a worktree and begins the LLM loop. */
export async function startTask(projectId: string, taskId: string): Promise<Task> {
    return writeOrQueue(
        { type: 'startTask', params: { projectId, taskId } },
        () => startTaskRaw(projectId, taskId),
        () => ({ id: taskId, status: 'in_progress' } as Task),
    );
}

/** Start a subtask (child task) execution. */
export async function startSubtask(projectId: string, taskId: string): Promise<Task> {
    return writeOrQueue(
        { type: 'startSubtask', params: { projectId, taskId } },
        () => startSubtaskRaw(projectId, taskId),
        () => ({ id: taskId, status: 'in_progress' } as Task),
    );
}

/** Stop (pause) a running task. */
export async function stopTask(projectId: string, taskId: string): Promise<Task> {
    return writeOrQueue(
        { type: 'stopTask', params: { projectId, taskId } },
        () => stopTaskRaw(projectId, taskId),
        () => ({ id: taskId, execution_state: 'paused' } as Task),
    );
}

/** Resume a stopped task. */
export async function resumeTask(projectId: string, taskId: string): Promise<Task> {
    return writeOrQueue(
        { type: 'resumeTask', params: { projectId, taskId } },
        () => resumeTaskRaw(projectId, taskId),
        () => ({ id: taskId, status: 'in_progress' } as Task),
    );
}

// -- Message endpoints --

/** Send a user message, which starts a turn and returns its row.
 * The payload is what the queue persists and the wire carries, so a field cannot be queued without being sent. */
export async function sendTaskMessage(payload: SendTaskMessagePayload): Promise<SendResult> {
    return writeOrQueue(
        { type: 'sendTaskMessage', params: { ...payload } },
        (clientToken) => sendTaskMessageRaw({ ...payload, clientToken }),
        () => ({ id: generateId(), role: 'user', content: payload.content }),
    );
}

/** One window of a task's conversation, plus how to ask for the older rows. */
export interface MessageWindow {
    messages: TaskMessage[];
    hasMore: boolean;
    /** Cursor for the next-older window; empty when this window reaches the first row. */
    before: string;
    /** Strong ETag of the newest window; absent on older pages and cached fallbacks. */
    revision?: string;
    /** A direct `304`: the caller keeps the window it already has. */
    unchanged?: boolean;
}

/** How far back one read of the conversation travels, and where it stops. */
export interface MessageWindowQuery {
    before?: string;
    /** The strong ETag of the newest window, sent only while reading the newest window. */
    knownRevision?: string;
    /** The oldest row this page may answer; the rows below it are later, never skipped. */
    from?: string;
    limit?: number;
}

/** The widest window the messages route answers, mirroring `store.MessageWindowMax` in
 * `backend/store/messages.go`: wider is a 400. */
export const MESSAGE_JUMP_LIMIT = 25;

/** Fetch the newest window of a task's conversation, or an older one by cursor.
 * Always tries the network first: a read is what releases the queue (`api/queue.ts`). */
export async function getTaskMessages(
    projectId: string,
    taskId: string,
    query: MessageWindowQuery = {},
): Promise<MessageWindow> {
    const { before = '', knownRevision = '', from = '', limit = 0 } = query;
    const params = new URLSearchParams();
    if (before) params.set('before', before);
    if (from) params.set('from', from);
    if (limit) params.set('limit', String(limit));
    const suffix = params.size > 0 ? `?${params.toString()}` : '';
    // An older page is a slice of the thread: its bytes say nothing about the rows above it.
    const ifNoneMatch = before ? '' : knownRevision;
    try {
        const res = await apiFetch(
            `${API_BASE}/projects/${projectId}/tasks/${taskId}/messages${suffix}`,
            ifNoneMatch ? { headers: { 'If-None-Match': ifNoneMatch } } : undefined,
        );
        const revision = res.headers.get('ETag') ?? undefined;
        if (res.status === 304) {
            return { messages: [], hasMore: false, before: '', revision: revision ?? ifNoneMatch, unchanged: true };
        }
        if (!res.ok) throw await apiError(res, t('api.fetchMessages'));
        const window: MessageWindow = {
            messages: await res.json(),
            hasMore: res.headers.get('X-Messages-More') === 'true',
            before: res.headers.get('X-Messages-Before') ?? '',
            ...(revision && !before ? { revision } : {}),
        };
        // Only the newest window is cached; an older page cached on its own is what a reload would open on.
        if (!before) widenCache(projectId, taskId, window).catch(() => {});
        return window;
    } catch (e) {
        const cached = await getBoardCache<MessageWindow>(messagesCacheKey(projectId, taskId));
        if (cached) {
            return markCached({ messages: cached.messages, hasMore: cached.hasMore, before: cached.before });
        }
        throw e;
    }
}

/** Cache a fresh window together with the rows the cache already held directly above it, so an offline
 * reload opens on the larger of the two reads. */
async function widenCache(projectId: string, taskId: string, window: MessageWindow): Promise<void> {
    const key = messagesCacheKey(projectId, taskId);
    let merged = window;
    try {
        const held = await getBoardCache<MessageWindow>(key);
        // An entry written by an older build may hold no rows array at all.
        if (Array.isArray(held?.messages)) {
            merged = { ...window, messages: mergeCachedRows(window.messages, held.messages) };
        }
    } catch {
        // A cache this browser cannot read is no reason to lose the window it just read.
    }
    await setBoardCache(key, merged);
}

/** What this browser holds of a conversation, read out of its own cache with no network attempt and no
 * failure to catch: the open path asks for it beside its read, to draw while that read is still out. It
 * carries no revision — the baseline names what a read answered, and this answered nothing. */
export async function readCachedWindow(projectId: string, taskId: string): Promise<MessageWindow | null> {
    const held = await getBoardCache<MessageWindow>(messagesCacheKey(projectId, taskId)).catch(() => null);
    if (!held || !Array.isArray(held.messages)) return null;
    return markCached({ messages: held.messages, hasMore: held.hasMore, before: held.before });
}

/** One page of a task's prompts, oldest first, plus how to ask for the older ones. */
export interface TurnPage {
    turns: ChatTurn[];
    hasMore: boolean;
    /** Cursor for the next-older prompts; empty when this page reaches the first prompt. */
    before: string;
}

/**
 * Fetch the newest prompts of a task's conversation, or older ones by cursor: a window counted in answers
 * does not hold the prompt that opened a long turn.
 *
 * Never cached - an old copy would name a message this browser has since deleted, and a caller that cannot
 * read it loses those labels, not the conversation.
 */
export async function getTaskTurns(projectId: string, taskId: string, before = ''): Promise<TurnPage> {
    const query = before ? `?before=${encodeURIComponent(before)}` : '';
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/turns${query}`);
    if (!res.ok) throw await apiError(res, t('api.fetchMessages'));
    return {
        turns: await res.json(),
        hasMore: res.headers.get('X-Turns-More') === 'true',
        before: res.headers.get('X-Turns-Before') ?? '',
    };
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

/** Delete a message and all messages after it. Set regenerate=true to trigger LLM regeneration. */
export async function deleteTaskMessagesAfter(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    await writeOrQueue(
        { type: 'deleteTaskMessagesAfter', params: { projectId, taskId, messageId, regenerate } },
        () => deleteTaskMessagesAfterRaw(projectId, taskId, messageId, regenerate),
    );
}

/** Delete a single message by ID (e.g., dismissing an error). Does not delete messages after it. */
export async function deleteTaskMessage(projectId: string, taskId: string, messageId: string): Promise<void> {
    await writeOrQueue(
        { type: 'deleteTaskMessage', params: { projectId, taskId, messageId } },
        () => deleteTaskMessageRaw(projectId, taskId, messageId),
    );
}

/** What a deletion takes with it: a fork branch, and the files the deleted turns produced (the worktree is
 * rewound to the state the remaining conversation describes). */
export async function checkMessageDelete(projectId: string, taskId: string, messageId: string): Promise<{ will_delete_branch: boolean; branch_name: string; resets_files: boolean }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`);
    if (!res.ok) throw await apiError(res, t('api.checkMessageDelete'));
    return res.json();
}

/** Edit a message's content. */
export async function updateTaskMessage(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    await writeOrQueue(
        { type: 'updateTaskMessage', params: { projectId, taskId, messageId, content } },
        () => updateTaskMessageRaw(projectId, taskId, messageId, content),
    );
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

/**
 * Put this task first in the provider queue it is waiting for a running-task slot in. False means it is
 * not waiting there — it holds its slot already, or waits on a queue no click reorders. Not queued for
 * replay: a reorder of a queue this server keeps in memory is worth nothing after a restart.
 */
export async function prioritizeTask(projectId: string, taskId: string): Promise<boolean> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/prioritize`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.prioritizeTask'));
    const body = (await res.json()) as { promoted?: boolean };
    return body.promoted === true;
}
