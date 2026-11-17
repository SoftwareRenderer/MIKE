import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';
import { installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

// A queued send has no row, so the queue is the record of it. A write the server refuses leaves that record
// for the same reason an applied one does, so a UI that clears its "queued" marker on "no longer queued"
// would report a refusal as a delivery — the verdict therefore has to outlive the entry it came from. Both
// halves are read here: what the replay remembers in memory, and what it stored beside the board.

const PROJECT = 'proj-1';

async function queueModule() {
    return await import('$lib/api/queue');
}

/** Refuse any request naming a task in `refused`, apply the rest; the session check answers normally
 * so the CSRF lookup is never the request under test. */
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

async function queueSend(taskId: string, content = `queued on ${taskId}`) {
    const { enqueueAction } = await queueModule();
    await enqueueAction({ type: 'sendTaskMessage', params: { projectId: PROJECT, taskId, content } });
}

describe('a queued send the server refuses', () => {
    // The first import of the api graph is the slow one; pay for it outside the test budget.
    beforeAll(async () => {
        await queueModule();
    }, 120_000);

    let restoreIndexedDB: (() => void) | undefined;

    beforeEach(() => {
        vi.resetModules();
        vi.unstubAllGlobals();
        restoreIndexedDB?.();
        restoreIndexedDB = installFakeIndexedDB();
    });

    it('is out of the queue and named as refused — its absence is not a delivery', async () => {
        routeRequests(['gone']);
        await queueSend('gone');
        const { flushQueueNow, getQueuedTaskIds, getDroppedTaskIds, getDroppedActions } = await queueModule();

        await flushQueueNow();

        expect(getQueuedTaskIds().has('gone')).toBe(false);
        expect(getDroppedTaskIds().has('gone')).toBe(true);
        expect(getDroppedActions().map(a => a.type)).toEqual(['sendTaskMessage']);
    });

    it('keeps the text beside the verdict, under the id of the action it refused', async () => {
        routeRequests(['gone']);
        await queueSend('gone', 'written offline');
        const mod = await queueModule();

        await mod.flushQueueNow();

        const refused = await mod.getRefusedSends(PROJECT, 'gone');
        expect(refused.map(s => [s.content, s.actionId])).toEqual([
            ['written offline', mod.getDroppedActions()[0].id],
        ]);
    });

    it('names only what it refused, and only for the flush that refused it', async () => {
        routeRequests(['gone']);
        await queueSend('gone');
        await queueSend('alive');
        const mod = await queueModule();

        await mod.flushQueueNow();

        expect([...mod.getDroppedTaskIds()]).toEqual(['gone']);
        expect(mod.getQueuedTaskIds().size).toBe(0);

        // A later flush of something else starts from an empty record, so the same refusal is not
        // announced again on every reconnect.
        await queueSend('later');
        await mod.flushQueueNow();
        expect(mod.getDroppedActions()).toHaveLength(0);
        expect(mod.getDroppedTaskIds().has('gone')).toBe(false);
    });

    it('records nothing for a send it applied', async () => {
        routeRequests([]);
        await queueSend('alive', 'delivered on reconnect');
        const mod = await queueModule();

        await mod.flushQueueNow();

        expect(await mod.getRefusedSends(PROJECT, 'alive')).toEqual([]);
        expect(mod.getQueuedSends(PROJECT, 'alive')).toEqual([]);
    });

    it('stops being the verdict — held or stored — on a task the person writes to again', async () => {
        routeRequests(['gone']);
        await queueSend('gone', 'refused once');
        const mod = await queueModule();
        await mod.flushQueueNow();
        expect(mod.getDroppedTaskIds().has('gone')).toBe(true);
        expect(await mod.getRefusedSends(PROJECT, 'gone')).toHaveLength(1);

        // The user hit send again: a fresh attempt is pending, so neither the replay's own memory nor
        // the stored verdict is news any more.
        await queueSend('gone', 'trying again');

        expect(mod.getDroppedTaskIds().has('gone')).toBe(false);
        expect(mod.getQueuedTaskIds().has('gone')).toBe(true);
        await vi.waitFor(async () => {
            expect(await mod.getRefusedSends(PROJECT, 'gone')).toEqual([]);
        });
    });

    it('is forgotten when the queue is cleared', async () => {
        routeRequests(['gone']);
        await queueSend('gone');
        const mod = await queueModule();
        await mod.flushQueueNow();
        expect(mod.getDroppedActions()).toHaveLength(1);

        // Sign-out empties the database; a record left in memory survives into the next account.
        mod.clearQueue();
        expect(mod.getDroppedActions()).toHaveLength(0);
    });

    it('answers with one task\'s own sends, in the order they were written', async () => {
        routeRequests([]);
        await queueSend('t1', 'first');
        await queueSend('t1', 'second');
        await queueSend('other', 'not this task');
        const { getQueuedSends } = await queueModule();

        expect(getQueuedSends(PROJECT, 't1').map(s => s.content)).toEqual(['first', 'second']);
        expect(getQueuedSends(PROJECT, 'other').map(s => s.content)).toEqual(['not this task']);
        expect(getQueuedSends('other-project', 't1')).toEqual([]);
    });
});
