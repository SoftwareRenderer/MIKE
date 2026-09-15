import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

function mockRejectedFetch(name = 'TypeError') {
    const err: any = new Error(name === 'AbortError' ? 'The operation was aborted' : 'Failed to fetch');
    err.name = name;
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(err));
}

function mockServerErrorFetch() {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500, json: async () => ({ error: 'internal server error' }) }));
}

describe('sendTaskMessage queueing on network failure', () => {
    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(() => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
    });

    afterEach(() => restoreIndexedDB?.());

    it('queues a sendTaskMessage action and returns a queued result when the network fails (TypeError)', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/api/queue');
        mockRejectedFetch('TypeError');

        const result = await sendTaskMessage('p1', 't1', 'hello world', 'model-a', 'http://llm:8010/v1');

        expect(result.queued).toBe(true);
        expect(result.actionId).toBeTruthy();
        expect(result.content).toBe('hello world');

        const queue = await loadQueue();
        expect(queue).toHaveLength(1);
        expect(queue[0].type).toBe('sendTaskMessage');
        expect(queue[0].params).toMatchObject({
            projectId: 'p1',
            taskId: 't1',
            content: 'hello world',
            modelName: 'model-a',
            serverUrl: 'http://llm:8010/v1',
        });
    });

    // Used to be a test that a timeout (AbortError) queues the message. There is no client-side timeout
    // any more, and an abort is no longer a network event: it means somebody stopped this request.
    it('does NOT queue a send the user cancelled (AbortError), and the rejection reaches the caller', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/api/queue');
        mockRejectedFetch('AbortError');

        await expect(sendTaskMessage('p1', 't1', 'cancelled message')).rejects.toThrow();

        const queue = await loadQueue();
        expect(queue).toHaveLength(0);
    });

    it('does NOT queue and rejects on non-transient server errors (HTTP 500)', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/api/queue');
        mockServerErrorFetch();

        await expect(sendTaskMessage('p1', 't1', 'server error message')).rejects.toBeTruthy();

        const queue = await loadQueue();
        expect(queue).toHaveLength(0);
    });
});
