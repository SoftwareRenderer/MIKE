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

// The tray is the other half of an edited message, and the server cannot invent it on the replay: a queue
// that lost it would replay an edit that took the pictures off the row. An emptied tray matters most — "no
// tray" and "no tray given" must not arrive as the same request.
describe("an edited message's tray in the queue", () => {
    let restoreIndexedDB: (() => void) | undefined;

    const picture = { mimeType: 'image/png', data: 'aGVsbG8=' };

    beforeEach(() => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
    });

    afterEach(async () => {
        // The offline flag and the queue are module state, so they go back whether or not the test threw.
        const { clearQueue, setOfflineStatus } = await import('$lib/api/queue');
        await clearQueue();
        setOfflineStatus(false);
        restoreIndexedDB?.();
    });

    /** Queues the write while the window is offline — what forkTaskThread() reads to decide — then replays it
     * and answers the body that went out. */
    async function replay(write: () => Promise<unknown>): Promise<Record<string, string>> {
        const { loadQueue, flushQueueNow, setOfflineStatus } = await import('$lib/api/queue');
        setOfflineStatus(true);
        await write();
        expect(await loadQueue()).toHaveLength(1);

        const sent: { url: string; body: string }[] = [];
        vi.stubGlobal('fetch', recordingFetch(sent));
        await flushQueueNow();
        expect(sent).toHaveLength(1);
        return JSON.parse(sent[0].body);
    }

    it('replays an edit with the pictures the editor held', async () => {
        const { updateTaskMessage } = await import('$lib/api/tasks');
        const body = await replay(() => updateTaskMessage('p1', 't1', 'm1', 'worded differently', [picture]));
        expect(body).toMatchObject({ content: 'worded differently', attachments: JSON.stringify([picture]) });
    });

    it('replays an emptied tray as a clearing, not as a tray left alone', async () => {
        const { updateTaskMessage } = await import('$lib/api/tasks');
        const body = await replay(() => updateTaskMessage('p1', 't1', 'm1', 'worded differently', []));
        expect(body).toMatchObject({ content: 'worded differently', attachments: '[]' });
    });

    it("leaves the row's pictures alone when the edit never touched them", async () => {
        const { updateTaskMessage } = await import('$lib/api/tasks');
        const body = await replay(() => updateTaskMessage('p1', 't1', 'm1', 'worded differently'));
        expect(Object.keys(body)).toEqual(['content']);
    });

    it("replays a fork of an edited question carrying that question's tray", async () => {
        const { forkTaskThread } = await import('$lib/api/threads');
        const body = await replay(() => forkTaskThread('p1', 't1', 'th-1', 'm1', 'asked differently', [picture]));
        expect(body).toMatchObject({
            from_thread_id: 'th-1',
            from_message_id: 'm1',
            new_content: 'asked differently',
            attachments: JSON.stringify([picture]),
        });
    });
});
