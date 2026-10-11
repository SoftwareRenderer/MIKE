// Two windows on one profile read and write a single saved queue, so one of them has to send it: two senders
// would send every write twice. The newest window takes the replay, an owner hands it back on the way out, and a
// window with nowhere to store the claim still sends its own queue.
// docs/reconnection.md → "One window replays the queue, and one record holds it".
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createFakeIndexedDB, installFakeIndexedDB } from '../../__tests__/fake-indexeddb';

/** The channel, shared by the windows this test opens the way the browser shares one between its tabs. */
function installSharedChannel() {
    type Handler = (event: MessageEvent) => void;
    const opened: { name: string; handler: Handler | null }[] = [];
    class SharedChannel {
        handler: Handler | null = null;
        constructor(readonly name: string) { opened.push(this); }
        addEventListener(type: string, handler: Handler) { if (type === 'message') this.handler = handler; }
        postMessage(data: unknown) {
            for (const other of opened) {
                if (other !== this && other.name === this.name) other.handler?.({ data } as MessageEvent);
            }
        }
    }
    vi.stubGlobal('BroadcastChannel', SharedChannel);
}

/** Answers every request and records the sends that went out. */
function answeringFetch(sent: string[]) {
    return vi.fn(async (url: unknown) => {
        sent.push(String(url));
        return { ok: true, status: 200, headers: new Headers(), json: async () => ({ id: 'm1' }) };
    }) as unknown as typeof fetch;
}

/** A window that has just booted: its own copy of the queue module, reading the record and taking the replay.
 * A module instance never unwires itself from `window`, so each scenario keeps to its own task. */
async function openWindow() {
    vi.resetModules();
    const { setCsrfToken } = await import('$lib/api/helpers');
    setCsrfToken('csrf-under-test');
    const queue = await import('$lib/api/queue');
    await queue.loadQueue();
    return queue;
}

const sendFor = (taskId: string) => ({
    type: 'sendTaskMessage' as const,
    params: { projectId: 'p1', taskId, content: `written for ${taskId}`, modelName: 'm', serverUrl: 'u' },
});
/** The sends this scenario's own task owes — a window left over from another scenario has its own task. */
const sendsOf = (sent: string[], taskId: string) => sent.filter(url => url === sendUrl(taskId));
const sendUrl = (taskId: string) => `/api/projects/p1/tasks/${taskId}/send`;

describe('two windows on one queue', () => {
    let restoreIndexedDB: (() => void) | undefined;
    let sent: string[];

    beforeEach(() => {
        installSharedChannel();
        sent = [];
        vi.stubGlobal('fetch', answeringFetch(sent));
    });

    afterEach(() => {
        restoreIndexedDB?.();
        vi.unstubAllGlobals();
    });

    it('sends a shared write once, from the window that opened last', async () => {
        restoreIndexedDB = installFakeIndexedDB(createFakeIndexedDB());
        const first = await openWindow();
        await first.enqueueAction(sendFor('t-opened-first'));

        const second = await openWindow();     // takes the replay from the window that wrote the send

        await first.flushQueueNow();
        await second.flushQueueNow();

        expect(sendsOf(sent, 't-opened-first')).toEqual([sendUrl('t-opened-first')]);
    });

    it('still names a pending send in the window that does not send it', async () => {
        restoreIndexedDB = installFakeIndexedDB(createFakeIndexedDB());
        const first = await openWindow();
        const second = await openWindow();

        await second.enqueueAction(sendFor('t-read-only-window'));

        // The reader's pending row and queued badge are drawn from the record, so they say what the owner owes.
        await vi.waitFor(() => {
            expect(first.getQueuedSends('p1', 't-read-only-window').map(s => s.content))
                .toEqual(['written for t-read-only-window']);
            expect(first.getQueuedTaskIds().has('t-read-only-window')).toBe(true);
        });
    });

    it('hands the queue to the window still open when the owner closes', async () => {
        restoreIndexedDB = installFakeIndexedDB(createFakeIndexedDB());
        const first = await openWindow();
        await first.enqueueAction(sendFor('t-orphaned'));
        const second = await openWindow();
        expect(second.getQueuedSends('p1', 't-orphaned')).toHaveLength(1);

        // The owner's page goes away — and with it the window that would have sent what the first one wrote.
        window.dispatchEvent(new Event('pagehide'));

        await vi.waitFor(() => {
            expect(sendsOf(sent, 't-orphaned')).toEqual([sendUrl('t-orphaned')]);
        });
        const { loadQueue } = await import('$lib/storage');
        expect(await loadQueue()).toEqual([]);
    });

    it('sends what it queued when there is nowhere to store the claim', async () => {
        // No IndexedDB at all: the record cannot be read or written, so this window is the only window there is.
        const only = await openWindow();
        await only.enqueueAction(sendFor('t-no-storage'));

        await only.flushQueueNow();

        expect(sendsOf(sent, 't-no-storage')).toEqual([sendUrl('t-no-storage')]);
    });

    it('does not cost the reading window one re-read per notice of a burst', async () => {
        restoreIndexedDB = installFakeIndexedDB(createFakeIndexedDB());
        const reader = await openWindow();
        let repaints = 0;
        reader.onQueueChanged(() => { repaints++; });

        // A window throttled in the background is handed its sibling's notices together on wake — this is that
        // backlog, delivered faster than one read of the record completes.
        const elsewhere = new BroadcastChannel('mike-queue');
        const queued = 20;
        for (let i = 0; i < queued; i++) elsewhere.postMessage({ notice: 'changed', windowId: 'another-window' });

        // Every read this burst provokes settles on microtasks, so draining a few turns leaves nothing pending.
        for (let turn = 0; turn < queued; turn++) await Promise.resolve();

        expect(repaints).toBeGreaterThan(0);
        expect(repaints).toBeLessThan(queued);
    });
});
