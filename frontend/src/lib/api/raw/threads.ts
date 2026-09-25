// One domain's plain HTTP calls: the offline queue replays these, and nothing here knows the queue exists.
import type { Thread } from '../../types';
import { API_BASE, apiFetch, apiError } from '../helpers';
import { t } from '$lib/i18n';

export async function switchTaskThreadRaw(projectId: string, taskId: string, threadId: string): Promise<Thread> {
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/tasks/${taskId}/threads/switch`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ thread_id: threadId }),
    });
    if (!res.ok) throw await apiError(res, t('api.switchThread'));
    return res.json();
}

export async function forkTaskThreadRaw(projectId: string, taskId: string, fromThreadId: string, fromMessageId: string, newContent?: string): Promise<Thread> {
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
