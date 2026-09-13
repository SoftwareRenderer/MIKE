import { describe, it, expect, beforeAll, beforeEach, vi } from 'vitest';

// A queued write the server refuses leaves the queue for the same reason an applied one does, so a UI
// that clears its "queued" marker on "no longer queued" reports a refusal as a delivery.
//
// No IndexedDB mock: dbPut swallows an unavailable database (see queue-board-scope.unit.ts).

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

async function queueSend(taskId: string) {
    const { enqueueAction } = await queueModule();
    await enqueueAction({
        type: 'sendTaskMessage',
        params: { projectId: PROJECT, taskId, content: `queued on ${taskId}` },
    });
}

describe('a queued write the server refuses', () => {
    // The first import of the api graph is the slow one; pay for it outside the test budget.
    beforeAll(async () => {
        await queueModule();
    }, 120_000);

    beforeEach(() => {
        vi.resetModules();
        vi.unstubAllGlobals();
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

    it('stops being the verdict on a task the user writes to again', async () => {
        routeRequests(['gone']);
        await queueSend('gone');
        const mod = await queueModule();
        await mod.flushQueueNow();
        expect(mod.getDroppedTaskIds().has('gone')).toBe(true);

        // The user hit send again: a fresh attempt is pending, so the old refusal is no news.
        await queueSend('gone');
        expect(mod.getDroppedTaskIds().has('gone')).toBe(false);
        expect(mod.getQueuedTaskIds().has('gone')).toBe(true);
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
});
