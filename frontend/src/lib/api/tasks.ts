import type { Task, TaskMessage } from '../types';
import { API_BASE, apiFetch, apiError, generateId } from './helpers';
import {
    getOfflineStatus,
    enqueueAction,
    queueWrite,
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
} from './queue';

// -- Task CRUD endpoints --

/** Create a new task in a project. Queued when offline. */
export async function createTask(projectId: string, task: Partial<Task>): Promise<Task> {
    if (getOfflineStatus()) {
        const id = generateId();
        return await queueWrite('createTask', { projectId, taskData: task }, { ...task, id, status: 'backlog', created_at: new Date().toISOString() } as Task);
    }
    return createTaskRaw(projectId, task);
}

/** Fetch a single task by ID. */
export async function getTask(projectId: string, taskId: string): Promise<Task> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}`);
    if (!res.ok) throw await apiError(res, 'Task not found');
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
    if (getOfflineStatus()) {
        return await queueWrite('moveTask', { projectId, taskId, status }, { id: taskId, status } as Task);
    }
    return moveTaskRaw(projectId, taskId, status);
}

/** Commit changes and mark a task as done. Queued when offline. */
export async function commitAndDone(projectId: string, taskId: string, commitMessage?: string): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('commitAndDone', { projectId, taskId }, { id: taskId, status: 'done' } as Task);
    }
    return commitAndDoneRaw(projectId, taskId, commitMessage);
}

/** Get a preview of the commit message that would be used for commit-and-done. */
export async function getCommitPreview(projectId: string, taskId: string): Promise<{ commit_message: string; author_name: string; author_email: string; merge_target: string }> {
    return getCommitPreviewRaw(projectId, taskId);
}

/** Delete all done tasks in a project. Queued when offline. */
export async function clearDoneTasks(projectId: string): Promise<{ deleted: number }> {
    if (getOfflineStatus()) {
        return await queueWrite('clearDoneTasks', { projectId }, { deleted: 0 });
    }
    return clearDoneTasksRaw(projectId);
}

// -- Task execution endpoints --

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

/** Stop a running task. Queued when offline. */
export async function stopTask(projectId: string, taskId: string): Promise<Task> {
    if (getOfflineStatus()) {
        return await queueWrite('stopTask', { projectId, taskId }, { id: taskId, execution_state: 'idle' } as Task);
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

/** Send a user message via HTTP POST. Triggers executor and returns the created message. Supports image attachments. Queued when offline. */
export async function sendTaskMessage(
    projectId: string,
    taskId: string,
    content: string,
    modelName?: string,
    serverUrl?: string,
    attachments?: Array<{ mimeType: string; data: string }>,
    reasoningEffort?: string
): Promise<{ id: string; role: string; content: string; thread_id?: string }> {
    if (getOfflineStatus()) {
        const id = generateId();
        return await queueWrite('sendTaskMessage', { projectId, taskId, content, modelName, serverUrl, attachments, reasoningEffort }, { id, role: 'user', content });
    }
    return sendTaskMessageRaw(projectId, taskId, content, modelName, serverUrl, attachments, reasoningEffort);
}

/** Fetch all messages for a task. */
export async function getTaskMessages(projectId: string, taskId: string): Promise<TaskMessage[]> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch messages');
    return res.json();
}

/** Fetch a single message with full content (uncompressed reasoning_content, tool_calls). */
export async function getTaskMessageFull(projectId: string, taskId: string, messageId: string): Promise<TaskMessage> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}?full=1`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch message');
    return res.json();
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

/** Regenerate from a message: deletes it and all messages after it. Alias for deleteTaskMessagesAfter. */
export const regenerateTaskMessages = deleteTaskMessagesAfter;

/** Check if deleting a message would also delete a fork branch. */
export async function checkMessageDelete(projectId: string, taskId: string, messageId: string): Promise<{ will_delete_branch: boolean; branch_name: string }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/messages/${messageId}`);
    if (!res.ok) throw await apiError(res, 'Failed to check message delete');
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

/** Manually trigger context summarization for a task. */
export async function summarizeTask(projectId: string, taskId: string): Promise<{ summary: string; old_count: number; new_count: number }> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/summarize`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, 'Failed to summarize task');
    return res.json();
}
