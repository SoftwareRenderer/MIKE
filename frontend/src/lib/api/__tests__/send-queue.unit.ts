import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

// What a queued send has to carry to answer later the way it would have answered now: the row the pending
// entry is drawn from, and every field of the wire body the server cannot guess. `writeOrQueue`'s own
// decision — answered, unanswered, refused, cancelled — is `write-or-queue.unit.ts`.

function mockRejectedFetch() {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(Object.assign(new Error('Failed to fetch'), { name: 'TypeError' })));
}

/** Answers the replay, recording each body that went out. */
function recordingFetch(bodies: { url: string; body: string }[]) {
    return vi.fn(async (url: unknown, init?: RequestInit) => {
        const href = String(url);
        if (href.includes('/api/auth/session')) {
            return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'tok' }) };
        }
        bodies.push({ url: href, body: String(init?.body) });
        return { ok: true, status: 200, json: async () => ({ id: 'm1', role: 'user', content: 'the message' }) };
    }) as unknown as typeof fetch;
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
        mockRejectedFetch();

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

    // Each of these is a field the server cannot invent on the replay: a replay that loses the effort runs
    // the task at the model's configured default instead (exec/loop.go falls back to
    // GetModelReasoningEffort when the request carries none), and an edited message is one send carrying the
    // row it replaces — the server truncates as the message lands, so a replay that dropped the intent would
    // stack the edit onto the thread it was meant to replace.
    it.each([
        ['the reasoning effort chosen for it',
            { content: 'think harder', modelName: 'model-a', reasoningEffort: 'high' },
            { reasoningEffort: 'high' },
            { content: 'think harder', model_name: 'model-a', reasoning_effort: 'high' }],
        ['the intent to replace a message',
            { content: 'worded differently', replaceMessageId: 'm-7' },
            { replaceMessageId: 'm-7' },
            { content: 'worded differently', replace_message_id: 'm-7' }],
    ])('queues and replays a send carrying %s', async (_name, send, heldParams, sentBody) => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue, flushQueueNow } = await import('$lib/api/queue');
        mockRejectedFetch();

        await sendTaskMessage({ projectId: 'p1', taskId: 't1', ...send });
        expect((await loadQueue())[0].params).toMatchObject(heldParams);

        const sent: { url: string; body: string }[] = [];
        vi.stubGlobal('fetch', recordingFetch(sent));
        await flushQueueNow();

        expect(sent.map(s => s.url)).toEqual(['/api/projects/p1/tasks/t1/send']);
        expect(JSON.parse(sent[0].body)).toMatchObject(sentBody);
    });
});
