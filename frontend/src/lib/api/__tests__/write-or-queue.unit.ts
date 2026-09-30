import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

/**
 * The promise every task write makes: a request the server answered is answered by the server, and a request
 * nobody answered is kept and sent when the connection returns. `writeOrQueue` decides that in one place, so the
 * window between the connection dying and the app noticing is covered by the write that failed inside it.
 */

const PROJECT = 'p1';
const TASK = 't1';
const TYPED = { title: 'typed while the wire was down' };

/** The failure shape `apiFetch` turns a dead connection into: `ConnectionFailure` carries `name: 'TypeError'`. */
function unanswered() {
    return vi.fn().mockRejectedValue(Object.assign(new Error('Failed to fetch'), { name: 'TypeError' }));
}

/** Answers every request, recording what went out. */
function answering(log: string[], init: { status?: number; body?: unknown } = {}) {
    const status = init.status ?? 200;
    return vi.fn(async (url: unknown, req?: RequestInit) => {
        log.push(`${req?.method ?? 'GET'} ${String(url)}`);
        return {
            ok: status >= 200 && status < 300,
            status,
            json: async () => init.body ?? { id: TASK, title: 'the server row' },
            headers: new Headers(),
        };
    }) as unknown as typeof fetch;
}

describe('a task write the server never answered', () => {
    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(async () => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
        // A token in hand keeps `apiFetch` from spending a CSRF request of its own before the one under test.
        const { setCsrfToken } = await import('$lib/api/helpers');
        setCsrfToken('csrf-under-test');
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        restoreIndexedDB?.();
    });

    it('answers with the server row and queues nothing', async () => {
        const { updateTask } = await import('$lib/api/tasks');
        const { loadQueue, waitForFlush } = await import('$lib/api/queue');
        const sent: string[] = [];
        vi.stubGlobal('fetch', answering(sent));

        const saved = await updateTask(PROJECT, TASK, TYPED);

        expect(saved.title).toBe('the server row');
        expect((saved as any).queued).toBeUndefined();
        expect(sent).toEqual([`PUT /api/projects/${PROJECT}/tasks/${TASK}`]);
        await waitForFlush();
        expect(await loadQueue()).toEqual([]);
    });

    it('keeps the write, with what was typed, when nobody answers', async () => {
        const { updateTask } = await import('$lib/api/tasks');
        const { loadQueue, waitForFlush, getOfflineStatus } = await import('$lib/api/queue');
        vi.stubGlobal('fetch', unanswered());

        const saved = await updateTask(PROJECT, TASK, TYPED);

        // The screen keeps what the person typed, and the queue holds it for the connection to return.
        expect(saved.title).toBe(TYPED.title);
        expect((saved as any).queued).toBe(true);
        await waitForFlush();
        const queued = await loadQueue();
        expect(queued).toHaveLength(1);
        expect(queued[0]).toMatchObject({ type: 'updateTask', params: { projectId: PROJECT, taskId: TASK, taskData: TYPED } });
        // The failed request is what declared the server unreachable, so the banner the person sees and the
        // queue that holds their words are raised by the same event.
        expect(getOfflineStatus()).toBe(true);
    });

    it('hands a refusal back to the caller instead of queueing it', async () => {
        const { updateTask } = await import('$lib/api/tasks');
        const { loadQueue, waitForFlush, getOfflineStatus } = await import('$lib/api/queue');
        vi.stubGlobal('fetch', answering([], { status: 403, body: { error: 'no' } }));

        await expect(updateTask(PROJECT, TASK, TYPED)).rejects.toThrow();

        // An answer of any status is an answer: a write the server refused must not come back out of
        // IndexedDB later, and a refusal says nothing about the connection.
        await waitForFlush();
        expect(await loadQueue()).toEqual([]);
        expect(getOfflineStatus()).toBe(false);
    });

    it('treats a cancellation as the person stopping this write', async () => {
        const { updateTask } = await import('$lib/api/tasks');
        const { loadQueue, waitForFlush } = await import('$lib/api/queue');
        vi.stubGlobal('fetch', vi.fn().mockRejectedValue(Object.assign(new Error('cancelled'), { name: 'AbortError' })));

        await expect(updateTask(PROJECT, TASK, TYPED)).rejects.toThrow();

        await waitForFlush();
        expect(await loadQueue()).toEqual([]);
    });

    it('spends no request once the app knows the server cannot be answered', async () => {
        const { updateTask } = await import('$lib/api/tasks');
        const { waitForFlush } = await import('$lib/api/queue');
        vi.stubGlobal('fetch', unanswered());
        await updateTask(PROJECT, TASK, TYPED);
        await waitForFlush();

        const sent: string[] = [];
        vi.stubGlobal('fetch', answering(sent));
        const second = await updateTask(PROJECT, TASK, { title: 'second edit' });

        expect(sent).toEqual([]);
        expect((second as any).queued).toBe(true);
    });
});
