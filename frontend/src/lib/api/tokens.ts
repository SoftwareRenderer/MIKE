import type { APITokenInfo, CreatedAPIToken } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import { t } from '$lib/i18n';

/**
 * API tokens (Settings → About): credentials a headless client presents as
 * `Authorization: Bearer <token>` on the handful of task and project routes they cover.
 *
 * None of these calls is queued for retry while offline. The create response carries the only copy
 * of the secret, so a replay would issue a different token and a queue would drop the copy the user
 * was meant to copy out — and revoking later than asked is the one outcome this screen must not
 * produce quietly.
 */

/** List every API token, newest first. The rows carry labels and timestamps, never a credential. */
export async function listAPITokens(): Promise<APITokenInfo[]> {
    const res = await apiFetch(`${API_BASE}/tokens`);
    if (!res.ok) throw await apiError(res, t('api.fetchAPITokens'));
    return res.json();
}

/**
 * Issue a token. The returned `token` is the only time this secret exists anywhere but the
 * server's hash — the caller must surface it and it must not be kept past this screen.
 */
export async function createAPIToken(label: string): Promise<CreatedAPIToken> {
    const res = await apiFetch(`${API_BASE}/tokens`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ label }),
    });
    if (!res.ok) throw await apiError(res, t('api.createAPIToken'));
    return res.json();
}

/** Revoke a token by id (204 on success). The next request presenting it authenticates as nothing. */
export async function revokeAPIToken(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/tokens/${encodeURIComponent(id)}`, {
        method: 'DELETE',
    });
    if (!res.ok) throw await apiError(res, t('api.revokeAPIToken'));
}
