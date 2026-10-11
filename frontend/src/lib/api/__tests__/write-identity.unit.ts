// One identity per write, minted before it sets out, so a send the server received but never answered is
// recognised as the write it already has. Only a message, a create and a fork are guarded; the rest carry the
// field unused (docs/reconnection.md → "One identity per write, so a replay cannot land twice").
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

const SEND = { projectId: 'p1', taskId: 't1', content: 'the message', modelName: 'm', serverUrl: 'u' };

/** Every body the app put on the wire, in the order it went. */
function recordingBodies(bodies: Record<string, any>[]) {
    return vi.fn(async (_url: unknown, init?: RequestInit) => {
        bodies.push(JSON.parse(String(init?.body ?? '{}')));
        return { ok: true, status: 201, headers: new Headers(), json: async () => ({ id: 'm1' }) };
    }) as unknown as typeof fetch;
}

describe('the identity one write carries', () => {
    let restoreIndexedDB: (() => void) | undefined;
    let bodies: Record<string, any>[];

    beforeEach(async () => {
        vi.resetModules();
        restoreIndexedDB = installFakeIndexedDB();
        bodies = [];
        const { setCsrfToken } = await import('$lib/api/helpers');
        setCsrfToken('csrf-under-test');
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        restoreIndexedDB?.();
    });

    it('is the same one on the replay of a send that got no answer', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { flushQueueNow } = await import('$lib/api/queue');
        // The server has the message and the answer never arrives, so the queue keeps what was on its way.
        vi.stubGlobal('fetch', vi.fn(async (url: unknown, init?: RequestInit) => {
            if (String(url).endsWith('/send')) {
                bodies.push(JSON.parse(String(init?.body)));
                throw Object.assign(new Error('Failed to fetch'), { name: 'TypeError' });
            }
            return { ok: true, status: 200, headers: new Headers(), json: async () => ({}) };
        }) as unknown as typeof fetch);

        const queued = await sendTaskMessage(SEND);
        expect(queued.queued).toBe(true);

        vi.stubGlobal('fetch', recordingBodies(bodies));
        await flushQueueNow();

        expect(bodies).toHaveLength(2);
        expect(bodies[0].client_token).toBeTruthy();
        expect(bodies[1].client_token).toBe(bodies[0].client_token);
    });

    it('is a fresh one for the next send, because that is a different write', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        vi.stubGlobal('fetch', recordingBodies(bodies));

        await sendTaskMessage(SEND);
        await sendTaskMessage({ ...SEND, content: 'asked again' });

        expect(bodies.map(b => b.content)).toEqual(['the message', 'asked again']);
        expect(bodies[1].client_token).not.toBe(bodies[0].client_token);
    });

    it('is a fresh one for a send the server refused and the person sends again', async () => {
        const { sendTaskMessage } = await import('$lib/api/tasks');
        const { loadQueue } = await import('$lib/storage');
        let first = true;
        vi.stubGlobal('fetch', vi.fn(async (url: unknown, init?: RequestInit) => {
            if (String(url).endsWith('/send')) {
                bodies.push(JSON.parse(String(init?.body)));
                if (first) { first = false; return { ok: false, status: 409, headers: new Headers(), json: async () => ({ error: 'no' }) }; }
            }
            return { ok: true, status: 201, headers: new Headers(), json: async () => ({ id: 'm1' }) };
        }) as unknown as typeof fetch);

        await expect(sendTaskMessage(SEND)).rejects.toThrow();
        // A refusal is an answer: nothing is kept, so nothing can come back under that identity.
        expect(await loadQueue()).toEqual([]);

        await sendTaskMessage(SEND);
        expect(bodies).toHaveLength(2);
        expect(bodies[1].client_token).not.toBe(bodies[0].client_token);
    });

    it('is the id a create fixed for itself, held by the record it leaves behind', async () => {
        const { createTask } = await import('$lib/api/tasks');
        const { setOfflineStatus } = await import('$lib/api/queue');
        const { loadQueue } = await import('$lib/storage');
        vi.stubGlobal('fetch', recordingBodies(bodies));
        setOfflineStatus(true);

        const queued = await createTask('p1', { prompt: 'a task written offline' });

        // The card on the board, the row in the record and the identity on the wire are fixed by one decision.
        const [held] = await loadQueue();
        expect(held.params.clientToken).toBe(queued.id);
    });

    it('is the id a create gave its attempt, again when the caller queues that attempt', async () => {
        const { createTask } = await import('$lib/api/tasks');
        const { enqueueAction, flushQueueNow } = await import('$lib/api/queue');
        // The create flow holds one id and hands it to both halves: the attempt, then the record.
        vi.stubGlobal('fetch', vi.fn(async () => {
            throw Object.assign(new Error('Failed to fetch'), { name: 'TypeError' });
        }) as unknown as typeof fetch);

        await expect(createTask('p1', { prompt: 'a task nobody answered' }, 'card-1')).rejects.toThrow();
        await enqueueAction({
            type: 'createTask',
            params: { projectId: 'p1', taskData: { prompt: 'a task nobody answered' }, clientToken: 'card-1' },
        });

        vi.stubGlobal('fetch', recordingBodies(bodies));
        await flushQueueNow();

        expect(bodies[0].client_token).toBe('card-1');
    });
});
