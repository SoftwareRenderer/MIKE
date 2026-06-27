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
    // Check module-level token first, then window.__csrfToken (set by E2E tests)
    if (csrfToken) return csrfToken;
    if (typeof window !== 'undefined' && (window as any).__csrfToken) return (window as any).__csrfToken;
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

/** Import queue helpers lazily to avoid circular deps */
let _setOfflineStatus: ((offline: boolean) => void) | null = null;
export function registerSetOffline(fn: (offline: boolean) => void): void {
    _setOfflineStatus = fn;
}

/** Wrapper around fetch that includes credentials (cookies) and CSRF token on every request. */
export async function apiFetch(url: string, init?: RequestInit): Promise<Response> {
    const headers = new Headers(init?.headers);
    // Skip CSRF for login endpoint (public)
    if (!url.includes('/api/auth/login')) {
        const token = await ensureCsrfToken();
        if (token) headers.set('X-CSRF-Token', token);
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
