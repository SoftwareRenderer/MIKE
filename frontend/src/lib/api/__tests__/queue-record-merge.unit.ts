// The offline queue is one record every window on the profile reads, so a change is written against what the
// record holds now: a window that dumps its own array back erases another window's action, and one that removes
// "the head" removes the wrong row. docs/reconnection.md → "One window replays the queue, and one record holds it".
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

/** What `$lib/storage` would have written to IndexedDB — one record, shared by both windows. */
const persisted = vi.hoisted(() => ({ queue: null as any[] | null }));

vi.mock('$lib/storage', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/storage')>();
    return {
        ...actual,
        loadQueue: async () => persisted.queue ?? [],
        updateQueue: async (change: (held: any[]) => any[]) => {
            persisted.queue = change(persisted.queue ?? []);
            return persisted.queue;
        },
    };
});

const record = () => (persisted.queue ?? []).map((a: any) => a.params.taskId);

/** A window that has just read the record — its own copy of the queue module. */
async function openWindow() {
    vi.resetModules();
    const mod = await import('$lib/api/queue');
    await mod.loadQueue();
    return mod;
}

const update = (taskId: string) => ({
    type: 'updateTask' as const,
    params: { projectId: 'p1', taskId, taskData: { title: `renamed ${taskId}` } },
});

beforeEach(() => {
    persisted.queue = null;
});

afterEach(() => {
    vi.unstubAllGlobals();
});

describe('one queue record, several windows', () => {
    it('keeps an action queued in another window', async () => {
        const first = await openWindow();
        const second = await openWindow();

        await first.enqueueAction(update('t1'));
        await second.enqueueAction(update('t2'));

        expect(record()).toEqual(['t1', 't2']);
    });

    it('replays a record that grew mid-send in order, instead of overwriting it', async () => {
        const first = await openWindow();
        const second = await openWindow();
        const sent: string[] = [];
        await first.enqueueAction(update('t1'));

        // t1 goes out from the first window; the second queues t2 while that request is still open.
        let queueSecondWhileInFlight: () => void = () => {};
        const inFlight = new Promise<void>(resolve => { queueSecondWhileInFlight = resolve; });
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            sent.push(String(url));
            await inFlight;
            return { ok: true, status: 200, json: async () => ({ id: 't1' }) };
        }));

        const flushing = first.flushQueueNow();
        await second.enqueueAction(update('t2'));
        queueSecondWhileInFlight();
        await flushing;

        // The window holding the record replays all of it, in order, and takes t1 out by identity rather than
        // writing its older copy over t2.
        // (`/api/auth/session` is the CSRF bootstrap each window does for itself.)
        const sentTasks = sent
            .map(url => url.match(/tasks\/([^/]+)/)?.[1])
            .filter((id): id is string => !!id);
        expect(sentTasks).toEqual(['t1', 't2']);
        expect(record()).toEqual([]);
    });
});
