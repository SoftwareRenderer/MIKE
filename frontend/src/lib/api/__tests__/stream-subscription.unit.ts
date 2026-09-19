/**
 * The client's half of the server's one-subscription-per-key rule.
 *
 * The server holds one subscription per key per connection, so one `unsubscribe_*` is enough to
 * leave it, and the work a subscription starts — a project's initial-data pass, a task's buffer
 * replay — belongs to the key rather than to the frame that arrived. The client used to queue a log
 * of every message written while the socket was down and *then* re-subscribe every key it held, so a
 * project opened during an outage was subscribed twice and unsubscribed once: the second hold stayed,
 * and with it the project's branch and filesystem watchers. The log also grew with every navigation
 * and was replayed whole, so a reconnect re-read what the user had long left.
 *
 * What replaced the log is a projection of the subscription sets — at most one frame per key, holding
 * the newest intent. These cases hold the consequences of that: one frame per key, an outage over
 * thousands of navigations that costs one frame per key still held, and a key the app entered and left
 * again while down costing nothing at all.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createAppStream, type AppStream, type AppStreamOptions } from '../stream';

/** A `WebSocket` that records what it was asked to write, and changes state only when told. */
class FakeSocket {
    static instances: FakeSocket[] = [];
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSED = 3;

    readyState = FakeSocket.CONNECTING;
    onopen: (() => void) | null = null;
    onmessage: ((e: { data: string }) => void) | null = null;
    onclose: (() => void) | null = null;
    onerror: ((e: unknown) => void) | null = null;
    readonly sent: string[] = [];

    constructor(public url: string) { FakeSocket.instances.push(this); }
    send(data: string) { this.sent.push(data); }
    close() { this.readyState = FakeSocket.CLOSED; this.onclose?.(); }
    open() { this.readyState = FakeSocket.OPEN; this.onopen?.(); }

    /** Frames written to this socket, in order. */
    frames(): Array<{ type: string; data: Record<string, any> }> {
        return this.sent.map(raw => JSON.parse(raw));
    }
    /** The frames of one type, in the order they were written. */
    typed(type: string) { return this.frames().filter(m => m.type === type); }
}

let stream: AppStream | null = null;
let realWS: any;

function start(options: AppStreamOptions = {}): FakeSocket {
    stream = createAppStream({}, options);
    return last();
}

function last(): FakeSocket { return FakeSocket.instances[FakeSocket.instances.length - 1]; }

/**
 * Drop the live socket and wait out the first rung of the ladder, so the next attempt exists and is
 * still handshaking: every subscription call below lands while nothing is open.
 */
function dropAndFail(): FakeSocket {
    last().close();
    vi.advanceTimersByTime(1000);
    return last();
}

/** Drain the promise chain behind an async subscribe, however deep the provider's own awaits. */
async function settle(): Promise<void> {
    for (let i = 0; i < 10; i++) await Promise.resolve();
}

beforeEach(() => {
    FakeSocket.instances = [];
    stream = null;
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-19T12:00:00Z'));
    realWS = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = FakeSocket;
});
afterEach(() => {
    stream?.close();
    (globalThis as any).WebSocket = realWS;
    vi.useRealTimers();
    vi.restoreAllMocks();
});

describe('the frames a reconnect owes', () => {
    it('subscribes once for a project opened while the socket was down', async () => {
        start().open();
        const socket = dropAndFail();

        stream!.subscribeProject('p1');
        await settle();
        socket.open();
        await settle();

        expect(socket.typed('subscribe_project')).toEqual([{ type: 'subscribe_project', data: { project_id: 'p1' } }]);
    });

    it('subscribes once for a project held across the outage', async () => {
        const first = start();
        first.open();
        stream!.subscribeProject('p1');
        await settle();
        expect(first.typed('subscribe_project')).toHaveLength(1);

        const socket = dropAndFail();
        socket.open();
        await settle();

        expect(socket.typed('subscribe_project')).toHaveLength(1);
    });

    it('sends nothing for a key opened and left while the socket was down', async () => {
        start().open();
        const socket = dropAndFail();

        stream!.subscribeProject('p1');
        stream!.unsubscribeProject('p1');
        stream!.subscribeTask('p1', 't1');
        stream!.unsubscribeTask('p1', 't1');
        await settle();
        socket.open();
        await settle();

        expect(socket.frames()).toEqual([]);
    });

    it('owes one frame per key however many navigations the outage covered', async () => {
        const first = start();
        first.open();
        stream!.subscribeProject('p1');
        await settle();

        const socket = dropAndFail();
        // Board ↔ card ↔ board a few thousand times. A message log queued a frame per call and
        // replayed every one of them on the reconnect.
        for (let i = 0; i < 3000; i++) {
            stream!.subscribeTask('p1', `task-${i % 5}`);
            stream!.unsubscribeTask('p1', `task-${i % 5}`);
        }
        // …and a walk through other projects, ending somewhere else.
        for (let i = 0; i < 1000; i++) {
            stream!.subscribeProject(`p-${i}`);
            stream!.unsubscribeProject(`p-${i}`);
        }
        stream!.subscribeProject('p-last');
        await settle();
        socket.open();
        await settle();

        // Two frames for 4000 navigations: one for the project that was on screen when the link
        // dropped, one for the one on screen now. Which of the two goes first is not a contract.
        expect(socket.sent).toHaveLength(2);
        expect(new Set(socket.typed('subscribe_project').map(f => f.data.project_id)))
            .toEqual(new Set(['p1', 'p-last']));
        expect(socket.typed('subscribe_task')).toHaveLength(0);
    });

    it('delivers the unsubscribe of a key left while the socket was down once', async () => {
        const first = start();
        first.open();
        stream!.subscribeProject('p1');
        stream!.subscribeTask('p1', 't1');
        await settle();

        const socket = dropAndFail();
        stream!.unsubscribeProject('p1');
        stream!.unsubscribeTask('p1', 't1');
        await settle();
        socket.open();
        await settle();

        expect(socket.typed('unsubscribe_project')).toEqual([{ type: 'unsubscribe_project', data: { project_id: 'p1' } }]);
        expect(socket.typed('unsubscribe_task')).toEqual([{ type: 'unsubscribe_task', data: { project_id: 'p1', task_id: 't1' } }]);
        expect(socket.typed('subscribe_project')).toHaveLength(0);
        expect(socket.typed('subscribe_task')).toHaveLength(0);
    });

    it('never sends a key\'s unsubscribe after a later subscribe for that key', async () => {
        const first = start();
        first.open();
        stream!.subscribeProject('p1');
        stream!.subscribeTask('p1', 't1');
        await settle();

        const socket = dropAndFail();
        // Left and re-entered during the outage: only the last intent for the key may reach the
        // server, or the client ends up holding a subscription it has no unsubscribe left to send.
        stream!.unsubscribeProject('p1');
        stream!.subscribeProject('p1');
        stream!.subscribeProject('p2');
        stream!.unsubscribeProject('p2');
        stream!.unsubscribeTask('p1', 't1');
        stream!.subscribeTask('p1', 't1');
        await settle();
        socket.open();
        await settle();

        expect(socket.typed('unsubscribe_project')).toEqual([]);
        expect(socket.typed('unsubscribe_task')).toEqual([]);
        expect(socket.typed('subscribe_project').map(f => f.data.project_id)).toEqual(['p1']);
        expect(socket.typed('subscribe_task')).toEqual([
            { type: 'subscribe_task', data: { project_id: 'p1', task_id: 't1' } },
        ]);
    });
});

describe('the known_stats handshake', () => {
    it('declares known_stats on the subscribe a reconnect sends', async () => {
        const stats = { 'task-1': '2026-09-19T10:00:00Z' };
        const first = start({ getKnownStats: () => stats });
        first.open();
        stream!.subscribeProject('p1');
        await settle();
        expect(first.typed('subscribe_project')[0].data.known_stats).toEqual(stats);

        const socket = dropAndFail();
        stream!.subscribeProject('p2'); // opened during the outage, provider resolving while down
        await settle();
        socket.open();
        await settle();

        expect(socket.typed('subscribe_project').map(f => f.data)).toEqual([
            { project_id: 'p2', known_stats: stats }, // the queued one first
            { project_id: 'p1', known_stats: stats }, // then the one held across the outage
        ]);
    });

    it('waits for the provider on the reconnect path instead of subscribing without stats', async () => {
        let release: (stats: Record<string, string>) => void = () => {};
        const first = start({
            getKnownStats: () => new Promise<Record<string, string>>(resolve => { release = resolve; }),
        });
        first.open();
        stream!.subscribeProject('p1');
        await settle();
        expect(first.typed('subscribe_project')).toHaveLength(0); // nothing declared yet
        release({});
        await settle();
        expect(first.typed('subscribe_project')).toHaveLength(1);

        const socket = dropAndFail();
        socket.open();
        await settle();
        expect(socket.typed('subscribe_project')).toHaveLength(0); // still a call outstanding
        release({ 'task-9': '2026-09-19T11:00:00Z' });
        await settle();

        expect(socket.typed('subscribe_project')).toEqual([
            { type: 'subscribe_project', data: { project_id: 'p1', known_stats: { 'task-9': '2026-09-19T11:00:00Z' } } },
        ]);
    });

    it('writes nothing for a project left while the provider was resolving', async () => {
        let release: (stats: Record<string, string>) => void = () => {};
        const socket = start({
            getKnownStats: () => new Promise<Record<string, string>>(resolve => { release = resolve; }),
        });
        socket.open();
        stream!.subscribeProject('p1');
        await settle();

        stream!.unsubscribeProject('p1'); // the user moved on before the provider answered
        release({ 'task-1': '2026-09-19T10:00:00Z' });
        await settle();

        // Nothing was ever asked for, so nothing is written now: a subscribe arriving behind that
        // unsubscribe would be a subscription with nothing left to release it, and the unsubscribe
        // itself would cancel a key the server never held.
        expect(socket.frames()).toEqual([]);
    });
});
