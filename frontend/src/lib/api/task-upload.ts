import { apiFetch, apiError, ensureCsrfToken } from './helpers';
import { t } from '$lib/i18n';

/**
 * Not `apiFetch`'s offline queue: a queued upload replays later than the message that names its
 * path, so the agent would be sent to a file that is not there yet.
 */
export async function uploadTaskFile(
    projectId: string,
    taskId: string,
    file: File,
    signal?: AbortSignal,
): Promise<string> {
    await ensureCsrfToken();
    const form = new FormData();
    form.append('file', file, file.name);

    const res = await apiFetch(
        `/api/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/uploads`,
        { method: 'POST', body: form, signal },
    );
    if (!res.ok) throw await apiError(res, t('api.uploadFile'));
    const data = (await res.json()) as { path?: string };
    if (!data.path) throw new Error(t('attachments.uploadFailed', { name: file.name }));
    return data.path;
}
