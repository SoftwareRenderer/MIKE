import { API_BASE, apiFetch, apiError, isAborted, setCsrfToken, clearCsrfToken } from './helpers';
import { t } from '$lib/i18n';

/**
 * The session check that gates first paint, the login that opens a session, the logout that closes one.
 * The session answer and the login response are the only two places the CSRF token is handed out, so it
 * is stored here: a component that had to remember it would break every later write, not just this screen.
 */

/**
 * Whether this browser's cookie names a live session, recording the CSRF token the answer carries.
 *
 * The flag in the body is the answer and the status is not — `handleAuthSession` answers 200 with
 * `false` — so an unexpected status is read rather than raised. First paint shows the login form for a
 * signed-out browser and keeps its offline branch for a body that is no answer at all.
 */
export async function checkSession(): Promise<boolean> {
    const res = await apiFetch(`${API_BASE}/auth/session`);
    const data = await res.json();
    const authenticated = data?.authenticated === true;
    if (authenticated && data.csrf_token) setCsrfToken(data.csrf_token);
    return authenticated;
}

/**
 * Open a session and store the CSRF token the response header hands back. A refused password throws the
 * server's own words, so the caller shows what it is given and adds nothing. No answer at all is remapped
 * to the form's network copy: this POST is never queued, so nothing downstream needs the name
 * `ConnectionFailure` carries.
 */
export async function login(password: string): Promise<void> {
    let res: Response;
    try {
        res = await apiFetch(`${API_BASE}/auth/login`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password }),
        });
    } catch (e) {
        if (isAborted(e)) throw e;
        throw new Error(t('app.login.networkError'), { cause: e });
    }
    if (!res.ok) throw await apiError(res, t('app.login.failed'));
    const token = res.headers.get('X-CSRF-Token');
    if (token) setCsrfToken(token);
}

/**
 * Close this browser's session and forget its CSRF token. A status that is not OK is not raised:
 * Settings → About revokes the current session and then runs this same path, and the caller's teardown
 * of this browser's own stores must not be skipped by the 401 that follows.
 */
export async function logout(): Promise<void> {
    await apiFetch(`${API_BASE}/auth/logout`, { method: 'POST' });
    clearCsrfToken();
}
