import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

/**
 * The queue holds a write for two reasons: what the socket says, and what this app's own requests say.
 * Each releases only on its own evidence.
 */

const SEND = { projectId: 'p1', taskId: 't1', content: 'the message', modelName: 'm', serverUrl: 'u' };

function unansweredFetch() {
    return vi.fn().mockRejectedValue(Object.assign(new Error('Failed to fetch'), { name: 'TypeError' }));
}

/** Answers every request, and records what went out. */
function answeringFetch(log: string[]) {
    return vi.fn(async (url: unknown, init?: RequestInit) => {
        const href = String(url);
        log.push(`${init?.method ?? 'GET'} ${href}`);
        if (href.includes('/messages')) {
            return { ok: true, status: 200, json: async () => [], headers: new Headers() };
        }
        return { ok: true, status: 200, json: async () => ({ id: 'm1', role: 'user', content: 'the message' }) };
    }) as unknown as typeof fetch;
}

describe('the two reasons the queue holds a write', () => {
    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(async () => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
        // A token in hand keeps `apiFetch` from spending a CSRF request of its own before the one under
        // test, so each scenario's request list is the one it wrote.
        const { setCsrfToken } = await import('$lib/api/helpers');
        setCsrfToken('csrf-under-test');
    });

    afterEach(() => restoreIndexedDB?.());

    it('keeps later writes queued until something answers, even with the socket up', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { getOfflineStatus } = await import('$lib/api/queue');
        vi.stubGlobal('fetch', unansweredFetch());

        const first = await sendTaskMessage(SEND);
        expect(first.queued).toBe(true);
        expect(getOfflineStatus()).toBe(true);

        // Nothing has answered yet, so the queue still holds and no request is spent.
        const live: string[] = [];
        vi.stubGlobal('fetch', answeringFetch(live));
        const second = await sendTaskMessage({ ...SEND, content: 'second' });

        expect(second.queued).toBe(true);
        expect(live).toEqual([]);
    });

    it('releases the queue on the next answered read and replays what it held', async () => {
        const { sendTaskMessage, getTaskMessages } = await import('$lib/api/tasks');
        const { getOfflineStatus, getQueuedTaskIds, waitForFlush } = await import('$lib/api/queue');
        vi.stubGlobal('fetch', unansweredFetch());

        await sendTaskMessage(SEND);
        expect(getQueuedTaskIds().has('t1')).toBe(true);

        const live: string[] = [];
        vi.stubGlobal('fetch', answeringFetch(live));
        // A read is what releases the queue.
        await getTaskMessages('p1', 't1');
        expect(getOfflineStatus()).toBe(false);

        await waitForFlush();

        expect(getQueuedTaskIds().has('t1')).toBe(false);
        expect(live).toEqual([
            'GET /api/projects/p1/tasks/t1/messages',
            'POST /api/projects/p1/tasks/t1/send',
        ]);
    });

    it('does not let an answered read release the socket’s reason', async () => {
        const { sendTaskMessage, getTaskMessages } = await import('$lib/api/tasks');
        const { setOfflineStatus, getOfflineStatus, getQueuedTaskIds, waitForFlush } = await import('$lib/api/queue');
        const live: string[] = [];
        vi.stubGlobal('fetch', answeringFetch(live));

        setOfflineStatus(true);
        const queued = await sendTaskMessage(SEND);
        expect(queued.queued).toBe(true);

        await getTaskMessages('p1', 't1');
        expect(getOfflineStatus()).toBe(true);
        expect(getQueuedTaskIds().has('t1')).toBe(true);

        // The socket comes back: only now does the queue go out.
        setOfflineStatus(false);
        await waitForFlush();
        expect(getQueuedTaskIds().has('t1')).toBe(false);
        expect(live.filter(c => c.startsWith('POST'))).toEqual(['POST /api/projects/p1/tasks/t1/send']);
    });

    it('lets a reason raised during a replay hold a write instead of being masked by the replay', async () => {
        // The replay's send never comes back. A reason raised during it is newer evidence than the replay:
        // masked, the next write would go out live while the queue stayed empty for a message never sent.
        const log: string[] = [];
        vi.stubGlobal('fetch', vi.fn(async (url: unknown, init?: RequestInit) => {
            const href = String(url);
            log.push(`${init?.method ?? 'GET'} ${href}`);
            if (href.endsWith('/send')) return new Promise<Response>(() => { /* the server is thinking */ });
            return { ok: true, status: 200, json: async () => ({}) };
        }) as unknown as typeof fetch);

        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { setOfflineStatus, getOfflineStatus } = await import('$lib/api/queue');

        setOfflineStatus(true);
        await sendTaskMessage(SEND);
        setOfflineStatus(false);      // the queue goes out, and that send hangs
        // The record is read before anything is sent, so wait for the replay to reach its request — the reason
        // below is raised during the send, which is what this scenario is about.
        await vi.waitFor(() => expect(log.filter(c => c.endsWith('/send'))).toEqual(['POST /api/projects/p1/tasks/t1/send']));
        setOfflineStatus(true);       // the socket drops again, mid-replay

        const second = await sendTaskMessage({ ...SEND, content: 'second' });

        expect(getOfflineStatus()).toBe(true);
        expect(second.queued).toBe(true);
        expect(log.filter(c => c.endsWith('/send'))).toEqual(['POST /api/projects/p1/tasks/t1/send']);
    });

    it('paces the replay after its own send failed, instead of retrying on every answered read', async () => {
        // Every replay end provokes a read that answers here; only this queued send fails. Without a pace
        // it would be retried on every read.
        const sends: string[] = [];
        vi.stubGlobal('fetch', vi.fn(async (url: unknown) => {
            if (String(url).endsWith('/send')) {
                sends.push(String(url));
                throw Object.assign(new Error('Failed to fetch'), { name: 'TypeError' });
            }
            return { ok: true, status: 200, headers: new Headers(), json: async () => [] };
        }) as unknown as typeof fetch);

        const { sendTaskMessage, getTaskMessages } = await import('$lib/api/tasks');
        const { setOfflineStatus, waitForFlush, getQueuedTaskIds } = await import('$lib/api/queue');
        const { QUEUE_RETRY_BASE_DELAY_MS } = await import('$lib/constants');

        setOfflineStatus(true);
        await sendTaskMessage(SEND);
        setOfflineStatus(false);          // the queue goes out, and its own send fails
        await waitForFlush();
        expect(sends).toHaveLength(1);
        expect(getQueuedTaskIds().has('t1')).toBe(true);

        await getTaskMessages('p1', 't1');
        await getTaskMessages('p1', 't1');
        await waitForFlush();
        expect(sends).toHaveLength(1);

        // After the pause, the next answered read tries once more.
        await new Promise((resolve) => setTimeout(resolve, QUEUE_RETRY_BASE_DELAY_MS + 300));
        await getTaskMessages('p1', 't1');
        await waitForFlush();
        expect(sends).toHaveLength(2);
    });
});
