import type { SessionInfo } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';

/**
 * List every live login session, newest first.
 * The caller's own session is flagged with `current: true`.
 * The response never carries a token, token hash or CSRF token.
 */
export async function listSessions(): Promise<SessionInfo[]> {
    const res = await apiFetch(`${API_BASE}/sessions`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch sessions');
    return res.json();
}

/**
 * Revoke one session by id (204 on success).
 * Revoking the CURRENT session is allowed — the caller's next request 401s, so
 * the caller is expected to run its sign-out path afterwards.
 */
export async function revokeSession(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/sessions/${encodeURIComponent(id)}`, {
        method: 'DELETE',
    });
    if (!res.ok) throw await apiError(res, 'Failed to revoke session');
}

/** Revoke every session except the caller's. Returns how many were revoked. */
export async function revokeOtherSessions(): Promise<{ revoked: number }> {
    const res = await apiFetch(`${API_BASE}/sessions/revoke-others`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to revoke other sessions');
    return res.json();
}
