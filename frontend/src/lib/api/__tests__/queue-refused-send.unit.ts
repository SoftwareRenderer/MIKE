import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

// A queued send has no row, so the queue is the record of it; a refusal has left the queue, so its
// verdict is stored beside the text.

const PROJECT = 'proj-1';

let restoreIndexedDB: (() => void) | undefined;

async function queueModule() {
    return await import('$lib/api/queue');
}

/** Refuse requests naming a task in `refused`, apply the rest, and keep the session check normal. */
function routeRequests(refused: string[]) {
    vi.stubGlobal('fetch', vi.fn(async (url: unknown) => {
        const href = String(url);
        if (href.includes('/api/auth/session')) {
            return { ok: true, status: 200, json: async () => ({ authenticated: true, csrf_token: 'tok' }) };
        }
        const status = refused.some(id => href.includes(`/${id}/`)) ? 404 : 200;
        return {
            ok: status === 200,
            status,
            json: async () => (status === 200 ? { id: 'msg', role: 'user', content: '' } : { error: 'no such task' }),
        };
    }) as unknown as typeof fetch);
}

async function queueSend(taskId: string, content: string) {
    const { enqueueAction } = await queueModule();
    await enqueueAction({ type: 'sendTaskMessage', params: { projectId: PROJECT, taskId, content } });
    return content;
}

describe('the queue as the record of an unsent message', () => {
    beforeAll(async () => {
        await queueModule();
    }, 120_000);

    beforeEach(() => {
        vi.resetModules();
        vi.unstubAllGlobals();
        restoreIndexedDB?.();
        restoreIndexedDB = installFakeIndexedDB();
    });

    it('answers with the task\'s own sends, in the order they were written', async () => {
        routeRequests([]);
        await queueSend('t1', 'first');
        await queueSend('t1', 'second');
        await queueSend('other', 'not this task');
        const { getQueuedSends } = await queueModule();

        expect(getQueuedSends(PROJECT, 't1').map(s => s.content)).toEqual(['first', 'second']);
        expect(getQueuedSends(PROJECT, 'other').map(s => s.content)).toEqual(['not this task']);
        expect(getQueuedSends('other-project', 't1')).toEqual([]);
    });

    it('keeps the text and its verdict after the server refuses the send', async () => {
        routeRequests(['gone']);
        await queueSend('gone', 'written offline');
        const mod = await queueModule();

        await mod.flushQueueNow();

        const refused = await mod.getRefusedSends(PROJECT, 'gone');
        expect(refused.map(s => [s.content, s.actionId])).toEqual([
            ['written offline', mod.getDroppedActions()[0].id],
        ]);
    });

    it('records nothing for a send it applied', async () => {
        routeRequests([]);
        await queueSend('alive', 'delivered on reconnect');
        const mod = await queueModule();

        await mod.flushQueueNow();

        expect(await mod.getRefusedSends(PROJECT, 'alive')).toEqual([]);
        expect(mod.getQueuedSends(PROJECT, 'alive')).toEqual([]);
    });

    it('retires the verdict when the person writes again', async () => {
        routeRequests(['gone']);
        await queueSend('gone', 'refused once');
        const mod = await queueModule();
        await mod.flushQueueNow();
        expect(await mod.getRefusedSends(PROJECT, 'gone')).toHaveLength(1);

        await mod.enqueueAction({
            type: 'sendTaskMessage',
            params: { projectId: PROJECT, taskId: 'gone', content: 'trying again' },
        });

        await vi.waitFor(async () => {
            expect(await mod.getRefusedSends(PROJECT, 'gone')).toEqual([]);
        });
    });
});
