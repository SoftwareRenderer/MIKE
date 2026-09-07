const API_BASE = '/api';

/** Generate a unique ID. Falls back to timestamp-based ID if crypto.randomUUID is unavailable. */
export function generateId(): string {
    if (typeof crypto !== 'undefined' && crypto.randomUUID) {
        return crypto.randomUUID();
    }
    return `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
}

/** CSRF token, set on login or session check. Held in memory only. */
let csrfToken: string = '';

/** Set the CSRF token (called after login or session check). */
export function setCsrfToken(token: string): void {
    csrfToken = token;
}

/** Clear the CSRF token (called on logout). */
export function clearCsrfToken(): void {
    csrfToken = '';
}

/** Lazily fetch CSRF token from session endpoint (for E2E test auth). */
export async function ensureCsrfToken(): Promise<string> {
    // Use the in-memory token if set, else fetch from the session endpoint
    if (csrfToken) return csrfToken;
    // Auto-fetch from session endpoint (public, no CSRF needed)
    try {
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 5000);
        try {
            const res = await fetch('/api/auth/session', { credentials: 'include', signal: controller.signal });
            const data = await res.json();
            if (data.authenticated && data.csrf_token) {
                csrfToken = data.csrf_token;
                return csrfToken;
            }
        } finally {
            clearTimeout(timeoutId);
        }
    } catch { /* ignore */ }
    return '';
}

/**
 * Parse one WebSocket frame into the message object every handler expects.
 *
 * A frame the parser cannot read is dropped and reported once per socket, not
 * thrown. Both WebSocket consumers used to call `JSON.parse` straight inside the
 * handler, where a throw is swallowed by the browser event loop: the frame vanishes,
 * the socket stays open, and the app silently misses whatever that frame carried - the
 * worst shape a failure can have, because nothing is observably wrong. The LSP client
 * already wrapped its parses this way (`utils/lsp-connection-manager.ts`); this is the
 * same rule with one implementation.
 *
 * The server is not the adversary here — a frame it cannot serialize is a bug or a
 * truncated write, not an attack — so this is about not losing a live connection over
 * it, and about saying so once rather than on every frame.
 */
const reportedBadFrame = new Set<string>();

export function parseStreamFrame(data: unknown, source: string): any | null {
    const msg = asJsonObject(data);
    if (msg !== null) return msg;
    // Once per socket *name*, and the payload is never echoed: a frame carries repository
    // content, and the log outlives the moment it came from. The set is module-level, so a
    // reconnect does not re-arm it — one bad frame from `stream` says the thing, and a
    // repeating one would be the same message over and over for the rest of the session.
    if (!reportedBadFrame.has(source)) {
        reportedBadFrame.add(source);
        console.warn(`[${source}] dropped a WebSocket frame that is not a JSON object; further bad frames are dropped silently`);
    }
    return null;
}

/**
 * The frame's JSON, when it is an object — a truncated write, a plain-text frame and a
 * bare `null`/number/string are one failure to the caller, which has no use for the
 * distinction beyond "this frame carried no message".
 */
function asJsonObject(data: unknown): any | null {
    let parsed: unknown;
    try {
        parsed = JSON.parse(String(data));
    } catch {
        return null;
    }
    return parsed !== null && typeof parsed === 'object' ? parsed : null;
}

/** Test seam: forget the reported-already state. */
export function resetBadFrameReports(): void {
    reportedBadFrame.clear();
}

/** Import queue helpers lazily to avoid circular deps */
let _setOfflineStatus: ((offline: boolean) => void) | null = null;
export function registerSetOffline(fn: (offline: boolean) => void): void {
    _setOfflineStatus = fn;
}

/** Wrapper around fetch that includes credentials (cookies) and CSRF token on every request.
 * If init.signal is provided, it is used directly (no default timeout). */
export async function apiFetch(url: string, init?: RequestInit & { signal?: AbortSignal }): Promise<Response> {
    const headers = new Headers(init?.headers);
    // Skip CSRF for login endpoint (public)
    if (!url.includes('/api/auth/login')) {
        const token = await ensureCsrfToken();
        if (token) headers.set('X-CSRF-Token', token);
    }
    if (init?.signal) {
        // External signal provided — use it directly (caller manages timeout/cancel)
        const res = await fetch(url, { ...init, credentials: 'include', headers, signal: init.signal });
        return res;
    }
    // Add timeout to prevent hanging requests when server is unreachable
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 15000);
    try {
        return await fetch(url, { ...init, credentials: 'include', headers, signal: controller.signal });
    } catch (e: any) {
        // If request fails, mark as offline so subsequent calls use the queue
        if (e?.name === 'TypeError' || e?.name === 'AbortError') {
            _setOfflineStatus?.(true);
        }
        throw e;
    } finally {
        clearTimeout(timeoutId);
    }
}

/** Extract error message from a failed API response, falling back to a default. */
export async function apiError(res: Response, defaultMsg: string): Promise<Error> {
    try {
        const body = await res.json();
        return new Error(body.error || defaultMsg);
    } catch {
        return new Error(defaultMsg);
    }
}

export { API_BASE };
