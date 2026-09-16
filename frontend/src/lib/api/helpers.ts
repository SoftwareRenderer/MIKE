import { t } from '$lib/i18n';
import { randomUUIDv4 } from '$lib/utils/random';

const API_BASE = '/api';

/**
 * The ID the server stores as this row's identity, so it is built from `crypto.getRandomValues`
 * rather than `crypto.randomUUID`, which a plain-HTTP origin does not have.
 */
export function generateId(): string {
    return randomUUIDv4();
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

/**
 * A request this app cancelled itself: the view unmounted, the user navigated away, or Stop/Cancel was
 * pressed. It is a different fact from "the server did not answer", and conflating the two is what made a
 * cancelled commit mark the whole app offline — which then queued every later write into IndexedDB.
 *
 * `name` stays `AbortError` on purpose, and **the name is the test**: `instanceof DOMException` is not,
 * because this is an `Error` that carries the name. Call `isAborted(err)` rather than either. A check that
 * misses it turns a silent cancellation into a loud error toast; a check that over-matches turns a real
 * outage into silence, so the check belongs on the cancellation and nowhere else.
 */
export class RequestAborted extends Error {
    readonly name = 'AbortError';

    constructor(url: string, options?: ErrorOptions) {
        super(`${t('api.requestCancelled')}: ${url}`, options);
    }
}

/** True when the failure is this app cancelling its own request — stay silent, the user asked for this. */
export function isAborted(err: unknown): boolean {
    return err instanceof RequestAborted || (err as any)?.name === 'AbortError';
}

/**
 * A request that got no answer at all: the connection failed, so the server is not running, not reachable,
 * or restarted mid-request. The message is for the toast at the top of the screen, so it names what to
 * check instead of quoting a `TypeError` the user cannot act on.
 *
 * `name` stays `TypeError`, for the same reason `RequestAborted` keeps `AbortError`: the checks this
 * codebase already has (`isTransientError`, `isDisconnectedError`, the offline queue) read the DOM name, and
 * a class they do not recognise silently turns "queue it, the server never saw it" into "the user's request
 * failed". The original error is on `cause`.
 */
export class ConnectionFailure extends Error {
    readonly name = 'TypeError';

    constructor(options?: ErrorOptions) {
        super(t('api.noAnswer'), options);
    }
}

/**
 * Wrapper around fetch that includes credentials (cookies) and CSRF token on every request.
 *
 * **No implicit deadline.** Every call used to be aborted at 15 s unless the caller passed a signal, which
 * is a decision on the work rather than on the queue: a big task's diff, a merge on commit-and-done, or a
 * clone replayed from the offline queue simply failed at 15 s while the server carried on with it — and the
 * abort was then counted as going offline. `ensureCsrfToken` keeps its 5 s bound because it is best-effort
 * and swallows its failure.
 *
 * Callers that have something to cancel pass `init.signal` (the component's lifetime, or a Stop button).
 * Two outcomes are reported, and they are not the same:
 *
 *   - aborted → `RequestAborted`, and the offline flag is **not** touched;
 *   - no connection → `ConnectionFailure` and the offline flag goes up, which is what the queue wants.
 *
 * A server that accepts a request and never answers is the case the old timeout existed for. It now shows
 * up as a request that keeps waiting — which is what the user asked for by starting the operation — and the
 * server reports its own stalls (`subproc.Watch`, the lock slow-wait reports) instead of the client guessing.
 */
export async function apiFetch(url: string, init?: RequestInit & { signal?: AbortSignal }): Promise<Response> {
    const headers = new Headers(init?.headers);
    // Skip CSRF for login endpoint (public)
    if (!url.includes('/api/auth/login')) {
        const token = await ensureCsrfToken();
        if (token) headers.set('X-CSRF-Token', token);
    }
    try {
        return await fetch(url, { ...init, credentials: 'include', headers, signal: init?.signal });
    } catch (e: any) {
        if (e?.name === 'AbortError') {
            // Somebody cancelled this request. That is not an outage, and marking one is how a cancelled
            // dialog used to start queueing every later write to IndexedDB, where it outlives the tab.
            throw new RequestAborted(url, { cause: e });
        }
        _setOfflineStatus?.(true);
        throw new ConnectionFailure({ cause: e });
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
