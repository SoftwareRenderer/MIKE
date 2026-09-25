// One domain's plain HTTP calls: the offline queue replays these, and nothing here knows the queue exists.
import type { MemoryAlertAckPayload, Task, TaskMessage } from '../../types';
import { API_BASE, apiFetch, apiError } from '../helpers';
import { t } from '$lib/i18n';

// -- Task CRUD endpoints --

export async function createTaskRaw(projectId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, t('api.createTask'));
    return res.json();
}

export async function updateTaskRaw(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(task),
    });
    if (!res.ok) throw await apiError(res, t('api.updateTask'));
    return res.json();
}

export async function deleteTaskRaw(projectId: string, taskId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.deleteTask'));
}

export async function moveTaskRaw(projectId: string, taskId: string, status: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
    });
    if (!res.ok) throw await apiError(res, t('api.moveTask'));
    return res.json();
}

/** Record the dismissal of a task's out-of-memory alert on its row, where every device reads it. */
export async function dismissMemoryAlertRaw(projectId: string, taskId: string, ack: MemoryAlertAckPayload): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/memory-alert-ack`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(ack),
    });
    if (!res.ok) throw await apiError(res, t('api.dismissMemoryAlert'));
    return res.json();
}

export async function commitAndDoneRaw(projectId: string, taskId: string, commitMessage?: string): Promise<Task> {
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

export async function clearDoneTasksRaw(projectId: string): Promise<{ deleted: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/done`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.clearDoneTasks'));
    return res.json();
}

// -- Task execution endpoints --

export async function initTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/init`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.initializeTask'));
    return res.json();
}

export async function startTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.startTask'));
    return res.json();
}

export async function startSubtaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/start-subtask`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.startSubtask'));
    return res.json();
}

export async function stopTaskRaw(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/stop`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.stopTask'));
    return res.json();
}

export async function resumeTaskRaw(projectId: string, taskId: string): Promise<Task> {
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
export async function sendTaskMessageRaw(payload: SendTaskMessagePayload): Promise<SendResult> {
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

export async function createTaskMessageRaw(projectId: string, taskId: string, role: string, content: string): Promise<TaskMessage> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role, content }),
    });
    if (!res.ok) throw await apiError(res, t('api.createMessage'));
    return res.json();
}

export async function deleteTaskMessagesAfterRaw(projectId: string, taskId: string, messageId: string, regenerate = false): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, regenerate }),
    });
    if (!res.ok) throw await apiError(res, t('api.deleteMessages'));
}

/** Delete a single message by ID (e.g., dismissing an error). Does not delete messages after it. */
export async function deleteTaskMessageRaw(projectId: string, taskId: string, messageId: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message_id: messageId, delete_single: true }),
    });
    if (!res.ok) throw await apiError(res, t('api.deleteMessage'));
}

export async function updateTaskMessageRaw(projectId: string, taskId: string, messageId: string, content: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) throw await apiError(res, t('api.updateMessage'));
}
