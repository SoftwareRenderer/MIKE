import type { SessionInfo } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import { t } from '$lib/i18n/index.svelte';

/**
 * List every live login session, newest first; the caller's own is flagged
 * `current: true`. The response never carries a token or CSRF token.
 */
export async function listSessions(): Promise<SessionInfo[]> {
    const res = await apiFetch(`${API_BASE}/sessions`);
    if (!res.ok) throw await apiError(res, t('api.fetchSessions'));
    return res.json();
}

/**
 * Revoke one session by id (204 on success). Revoking the CURRENT session is
 * allowed — the next request 401s, so callers run their sign-out path afterwards.
 */
export async function revokeSession(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/sessions/${encodeURIComponent(id)}`, {
        method: 'DELETE',
    });
    if (!res.ok) throw await apiError(res, t('api.revokeSession'));
}

/** Revoke every session except the caller's. Returns how many were revoked. */
export async function revokeOtherSessions(): Promise<{ revoked: number }> {
    const res = await apiFetch(`${API_BASE}/sessions/revoke-others`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, t('api.revokeOtherSessions'));
    return res.json();
}
