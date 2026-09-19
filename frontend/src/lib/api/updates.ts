import type { UpdateApplyResult, UpdatePreparedResult, UpdateRestartResult, UpdateStatus } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import { t } from '$lib/i18n';

/** The five release-update routes. None of them queue when offline: an install replayed from a
 *  persisted queue would stop the server at a moment nobody chose. docs/auto-update.md. */

/** What is already known. The server performs no egress to answer it. */
export async function fetchUpdateStatus(): Promise<UpdateStatus> {
    const res = await apiFetch(`${API_BASE}/updates/status`);
    if (!res.ok) throw await apiError(res, t('api.fetchUpdateStatus'));
    return res.json();
}

/** Fetch the manifest now. A refusal answers 409 with the sentence to show. */
export async function checkForUpdates(): Promise<UpdateStatus> {
    const res = await apiFetch(`${API_BASE}/updates/check`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.checkForUpdates'));
    return res.json();
}

/** Download and verify the newest release. It lands beside the install, not inside it. */
export async function prepareUpdate(): Promise<UpdatePreparedResult> {
    const res = await apiFetch(`${API_BASE}/updates/prepare`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.prepareUpdate'));
    return res.json();
}

/** Put the downloaded release in place and restart the server onto it. */
export async function applyUpdate(): Promise<UpdateApplyResult> {
    const res = await apiFetch(`${API_BASE}/updates/apply`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.applyUpdate'));
    return res.json();
}

/** End this process the way a signal would, which is what applies a staged release. */
export async function restartToUpdate(): Promise<UpdateRestartResult> {
    const res = await apiFetch(`${API_BASE}/updates/restart`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.restartToUpdate'));
    return res.json();
}
