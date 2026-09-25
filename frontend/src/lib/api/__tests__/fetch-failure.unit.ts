import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

import { apiFetch, setCsrfToken, registerSetHttpReachable, isAborted, RequestAborted, ConnectionFailure } from '../helpers';
import { isTransientError } from '../queue';

/**
 * `apiFetch` is the one reader of every API call's failure, so the two ways a request can end are decided
 * here and nowhere else: "this app cancelled it" and "the server did not answer". They were the same event
 * — an abort marked the whole app offline, which queued every later write into IndexedDB, where it outlives
 * both the tab and the session.
 */

function stubFetchReject(name: string, message = 'boom') {
    const err: any = new Error(message);
    err.name = name;
    const fetchMock = vi.fn().mockRejectedValue(err);
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
}

describe('apiFetch failure classification', () => {
    /** What `apiFetch` reports: false on no answer, true on any answer. */
    let reachabilityReports: boolean[];

    beforeEach(() => {
        vi.resetModules();
        reachabilityReports = [];
        registerSetHttpReachable((v: boolean) => reachabilityReports.push(v));
        // A token in hand keeps ensureCsrfToken from spending its own best-effort request.
        setCsrfToken('csrf-under-test');
    });

    afterEach(() => {
        vi.useRealTimers();
        vi.unstubAllGlobals();
    });

    it('does not abort a request on its own clock', async () => {
        vi.useFakeTimers();
        let signal: AbortSignal | undefined;
        vi.stubGlobal('fetch', vi.fn((_url: any, init: any) => {
            signal = init?.signal;
            return new Promise<Response>(() => { /* the server is thinking */ });
        }));

        let state = 'pending';
        const watched = apiFetch('/api/projects/p1/tasks/t1/diff').then(
            () => { state = 'resolved'; },
            () => { state = 'rejected'; },
        );
        await Promise.resolve();
        vi.advanceTimersByTime(60_000);
        await Promise.resolve();

        expect(state).toBe('pending');
        // No hidden AbortController at all: with no caller signal there is nothing that can time this
        // call out. The old code attached one at 15 s, and a big diff, a merge on commit-and-done and a
        // replayed clone all outlast it while the server is doing exactly what it was asked to do.
        expect(signal).toBeUndefined();
        void watched;
    });

    it('reports a cancellation as a cancellation, and does not call it an outage', async () => {
        stubFetchReject('AbortError', 'The user aborted a request.');
        const controller = new AbortController();

        const err = await apiFetch('/api/projects/p1/tasks/t1/commit-and-done', { signal: controller.signal })
            .then(() => null)
            .catch((e) => e);

        expect(err).toBeInstanceOf(RequestAborted);
        expect(isAborted(err)).toBe(true);
        // The name every existing cancellation check in this codebase already reads.
        expect((err as any).name).toBe('AbortError');
        expect(reachabilityReports).toEqual([]);
        expect(isTransientError(err)).toBe(false);
    });

    it('reports no-connection as a connection failure, reports the server unreachable, and says what to check', async () => {
        stubFetchReject('TypeError', 'Failed to fetch');

        const err = await apiFetch('/api/config').then(() => null).catch((e) => e);

        expect(err).toBeInstanceOf(ConnectionFailure);
        expect(isAborted(err)).toBe(false);
        expect((err as Error).message).toContain('Connection issue');
        expect(reachabilityReports).toEqual([false]);
        // The cause survives, so the raw TypeError is still reachable in a debugger and in console output.
        expect((err as any).cause?.name).toBe('TypeError');
    });

    it('reports the server reachable on an answer, whatever the status is', async () => {
        // A 404 is a server that answered, and an answer is what releases the queue.
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
            ok: false, status: 404, headers: new Headers(), json: async () => ({ error: 'no such task' }),
        }));

        const res = await apiFetch('/api/projects/p1/tasks/gone');

        expect(res.status).toBe(404);
        expect(reachabilityReports).toEqual([true]);
    });

    it('keeps a queued retry available for the failure that is actually retryable', () => {
        const network: any = new Error('Failed to fetch');
        network.name = 'TypeError';
        expect(isTransientError(network)).toBe(true);
        expect(isTransientError(new RequestAborted('/api/x'))).toBe(false);
        expect(isTransientError(new Error('HTTP 500'))).toBe(false);
    });
});
