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

        const result = await sendTaskMessage({ projectId: 'p1', taskId: 't1', content: 'hello world', modelName: 'model-a', serverUrl: 'http://llm:8010/v1' });

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

        await expect(sendTaskMessage({ projectId: 'p1', taskId: 't1', content: 'cancelled message' })).rejects.toThrow();

        const queue = await loadQueue();
        expect(queue).toHaveLength(0);
    });

    // A replay that loses the effort runs the task at the model's configured default instead
    // (exec/loop.go falls back to GetModelReasoningEffort when the request carries none).
    it('replays a queued send with the reasoning effort chosen for it', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue, flushQueueNow } = await import('$lib/api/queue');
        mockRejectedFetch('TypeError');

        await sendTaskMessage({ projectId: 'p1', taskId: 't1', content: 'think harder', modelName: 'model-a', reasoningEffort: 'high' });
        expect((await loadQueue())[0].params).toMatchObject({ reasoningEffort: 'high' });

        const sends: { url: string; body: string }[] = [];
        vi.stubGlobal('fetch', vi.fn(async (url: unknown, init?: RequestInit) => {
            const href = String(url);
            if (href.includes('/api/auth/session')) {
                return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'tok' }) };
            }
            sends.push({ url: href, body: String(init?.body) });
            return { ok: true, status: 200, json: async () => ({ id: 'm1', role: 'user', content: 'think harder' }) };
        }) as unknown as typeof fetch);

        await flushQueueNow();

        expect(sends.map(s => s.url)).toEqual(['/api/projects/p1/tasks/t1/send']);
        expect(JSON.parse(sends[0].body)).toMatchObject({
            content: 'think harder',
            model_name: 'model-a',
            reasoning_effort: 'high',
        });
    });

    // An edited message is one send carrying the row it replaces: the server truncates as the message
    // lands, so the queue has to carry that intent or a replay would stack the edit onto the thread it was
    // meant to replace.
    it('queues an edit-and-send with its replace intent and replays it', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue, flushQueueNow } = await import('$lib/api/queue');
        mockRejectedFetch('TypeError');

        await sendTaskMessage({ projectId: 'p1', taskId: 't1', content: 'worded differently', replaceMessageId: 'm-7' });
        expect((await loadQueue())[0].params).toMatchObject({ replaceMessageId: 'm-7' });

        const bodies: any[] = [];
        vi.stubGlobal('fetch', vi.fn(async (url: unknown, init?: RequestInit) => {
            const href = String(url);
            if (href.includes('/api/auth/session')) {
                return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'tok' }) };
            }
            bodies.push(JSON.parse(String(init?.body)));
            return { ok: true, status: 200, json: async () => ({ id: 'm8', role: 'user', content: 'worded differently' }) };
        }) as unknown as typeof fetch);

        await flushQueueNow();

        expect(bodies).toEqual([expect.objectContaining({ content: 'worded differently', replace_message_id: 'm-7' })]);
    });

    it('does NOT queue and rejects on non-transient server errors (HTTP 500)', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/api/queue');
        mockServerErrorFetch();

        await expect(sendTaskMessage({ projectId: 'p1', taskId: 't1', content: 'server error message' })).rejects.toBeTruthy();

        const queue = await loadQueue();
        expect(queue).toHaveLength(0);
    });
});
