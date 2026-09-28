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

/** Import queue helpers lazily to avoid circular deps */
let _setHttpReachable: ((reachable: boolean) => void) | null = null;

/** Where `apiFetch` reports what its own requests saw. */
export function registerSetHttpReachable(fn: (reachable: boolean) => void): void {
    _setHttpReachable = fn;
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
 * Whether a request needs the CSRF token. `CSRFMiddleware` skips GET, HEAD and OPTIONS, so a safe request
 * that asked for one would spend a round trip to `/api/auth/session` for a header the server ignores — and
 * the session check is the request that supplies that token, so asking on a safe method makes the boot probe
 * pay for itself twice.
 */
function needsCsrf(method: string | undefined): boolean {
    const m = (method ?? 'GET').toUpperCase();
    return m !== 'GET' && m !== 'HEAD' && m !== 'OPTIONS';
}

/**
 * Wrapper around fetch that includes credentials (cookies) and, for a method the CSRF middleware guards, its
 * token.
 *
 * **No implicit deadline.** Every call used to be aborted at 15 s unless the caller passed a signal, which
 * is a decision on the work rather than on the queue: a big task's diff, a merge on commit-and-done, or a
 * clone replayed from the offline queue simply failed at 15 s while the server carried on with it — and the
 * abort was then counted as going offline. `ensureCsrfToken` keeps its 5 s bound because it is best-effort
 * and swallows its failure.
 *
 * Callers that have something to cancel pass `init.signal` (the component's lifetime, or a Stop button).
 * Three outcomes are reported, and they are not the same:
 *
 *   - aborted → `RequestAborted`, and neither offline reason is touched;
 *   - no connection → `ConnectionFailure` and the HTTP reason goes up, which is what the queue wants;
 *   - answered, whatever the status → the HTTP reason goes down, because a `404` is still a server.

 *
 * A server that accepts a request and never answers is the case the old timeout existed for. It now shows
 * up as a request that keeps waiting — which is what the user asked for by starting the operation — and the
 * server reports its own stalls (`subproc.Watch`, the lock slow-wait reports) instead of the client guessing.
 */
export async function apiFetch(url: string, init?: RequestInit & { signal?: AbortSignal }): Promise<Response> {
    const headers = new Headers(init?.headers);
    // The login endpoint is public and has no token to send yet.
    if (needsCsrf(init?.method) && !url.includes('/api/auth/login')) {
        const token = await ensureCsrfToken();
        if (token) headers.set('X-CSRF-Token', token);
    }
    try {
        const res = await fetch(url, { ...init, credentials: 'include', headers, signal: init?.signal });
        _setHttpReachable?.(true);
        return res;
    } catch (e: any) {
        if (e?.name === 'AbortError') {
            // Somebody cancelled this request. That is not an outage, and marking one is how a cancelled
            // dialog used to start queueing every later write to IndexedDB, where it outlives the tab.
            throw new RequestAborted(url, { cause: e });
        }
        _setHttpReachable?.(false);
        throw new ConnectionFailure({ cause: e });
    }
}

/** Headers for a caller that opens its own response instead of going through `apiFetch` — the progress
 * streams that cost the caller a line rather than the HTTP reason (`docs/reconnection.md → How a request
 * ends`). Nothing else about the request changes. Every such caller posts, so the token is asked for unless
 * a safe method says otherwise. */
export async function csrfHeaders(init?: HeadersInit, method = 'POST'): Promise<Headers> {
    const headers = new Headers(init);
    if (needsCsrf(method)) {
        const token = await ensureCsrfToken();
        if (token) headers.set('X-CSRF-Token', token);
    }
    return headers;
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

/** One frame of a server progress stream: the frame's `event:` name, and its `data:` text. */
export interface EventFrame {
    /** The frame's `event:` field, or `'message'` when it carries none. */
    event: string;
    /** The frame's `data:` lines joined with `\n`; never a frame with no `data:` field at all. */
    data: string;
}

/**
 * The one reader of a server progress stream: a `Response` whose body the server is writing as
 * blank-line-terminated frames. Every core consumer of one iterates this rather than splitting the
 * bytes itself — `grep -rn getReader frontend/src` is the count.
 *
 * The frame grammar is a cross-boundary contract: the Go side writes `data: %s\n\n`
 * (`backend/api/project.go`, `backend/extensions/handler.go`) or `event: %s\ndata: %s\n\n`
 * (`backend/exec/merge.go`), so the split is on the blank line and never on a single newline — a line
 * split across two chunks is not a boundary. The tail of an unfinished frame is kept for the next
 * chunk, and a tail the stream never completes is dropped: half a frame is not a frame, and whether a
 * stream that stopped early counts as failure is the caller's question, because only it knows which
 * frame it needed to see (`done`, a build result, a cloned project).
 *
 * No deadline is applied here, and a long wait is not read as a stalled server. The caller's
 * `AbortSignal` ends a stream, and a rejected read propagates unchanged, so an abort stays a
 * cancellation and never becomes a connection failure.
 */
export async function* readEventStream(res: Response): AsyncGenerator<EventFrame> {
    const reader = res.body?.getReader();
    if (!reader) throw new Error(t('api.streamingNotSupported'));

    const decoder = new TextDecoder();
    let tail = '';
    try {
        while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            tail += decoder.decode(value, { stream: true });

            let boundary = tail.indexOf('\n\n');
            while (boundary !== -1) {
                const frame = parseEventFrame(tail.slice(0, boundary));
                tail = tail.slice(boundary + 2);
                if (frame) yield frame;
                boundary = tail.indexOf('\n\n');
            }
        }
    } finally {
        reader.releaseLock();
    }
}

/**
 * One frame's fields. A line that is neither `event:` nor `data:` is ignored, which is also how a
 * keep-alive comment reaches nothing. `data:` loses exactly one leading space, the separator the
 * writers add — the payload of a build-log line keeps every other byte.
 */
function parseEventFrame(raw: string): EventFrame | null {
    let event = 'message';
    const data: string[] = [];
    for (const line of raw.split('\n')) {
        if (line.startsWith('event:')) {
            event = line.slice(6).trim();
        } else if (line.startsWith('data:')) {
            const value = line.slice(5);
            data.push(value.startsWith(' ') ? value.slice(1) : value);
        }
    }
    return data.length ? { event, data: data.join('\n') } : null;
}

export { API_BASE };
