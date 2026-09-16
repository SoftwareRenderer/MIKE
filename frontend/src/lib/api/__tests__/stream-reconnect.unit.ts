/**
 * The reconnect ladder and the signals that override it.
 *
 * The ladder is the pace for a server that is genuinely down, and every "the user is back" path —
 * the window returning, the browser coming online, the banner's button — used to fall into it at
 * whatever rung a night of failures had reached. That is why reopening the app showed a 30 second
 * countdown instead of a connection. These cases hold the two rules that replaced it: an attempt
 * asked for by evidence starts the ladder over, and a timer that could not run when it was due
 * says the wait has already been served by the wall clock.
 *
 * A suspended page is simulated by moving the system clock and then letting the overdue timer go
 * (`advanceTimersToNextTimer`), which is what resuming does to a pending timer.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createAppStream, type AppStream } from '../stream';
import { HEARTBEAT_INTERVAL_MS, RECONNECT_MAX_DELAY_MS, STALE_THRESHOLD_MS, SUSPENDED_GAP_MS } from '$lib/constants';
import { WAKE_FRAME_GAP_MS } from '$lib/utils/tab-return';

/** A `WebSocket` whose state changes only when the test says so. */
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
    closed = false;

    constructor(public url: string) { FakeSocket.instances.push(this); }
    send() {}
    /** Browsers fire `close` for a socket torn down while still connecting, so this one does too. */
    close() {
        this.closed = true;
        this.readyState = FakeSocket.CLOSED;
        this.onclose?.();
    }
    open() {
        this.readyState = FakeSocket.OPEN;
        this.onopen?.();
    }
    /** The server's keepalive frame, which is what proves a socket is still talking. */
    frame() { this.onmessage?.({ data: '{"type":"ping"}' }); }
}

let stream: AppStream | null = null;
/** Every delay the client was told to wait out, in the order it was told. */
let waits: number[] = [];

function start(): FakeSocket {
    stream = createAppStream({
        onReconnectScheduled: (nextAttemptAt) => { waits.push(nextAttemptAt - Date.now()); },
    });
    return last();
}

function last(): FakeSocket { return FakeSocket.instances[FakeSocket.instances.length - 1]; }
function sockets(): number { return FakeSocket.instances.length; }

/** Move the clock as if this page were not running, then let the pending timer go. */
function suspendFor(ms: number): void {
    vi.setSystemTime(new Date(Date.now() + ms));
    vi.advanceTimersToNextTimer();
}

/** Fail attempts until the ladder has climbed to its ceiling. */
function climbToCeiling(): void {
    last().close();
    while (waits[waits.length - 1] < RECONNECT_MAX_DELAY_MS) {
        vi.advanceTimersByTime(waits[waits.length - 1]);
        last().close();
    }
}

let realWS: any;
beforeEach(() => {
    FakeSocket.instances = [];
    waits = [];
    stream = null;
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-14T12:00:00Z'));
    realWS = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = FakeSocket;
});
afterEach(() => {
    stream?.close();
    (globalThis as any).WebSocket = realWS;
    vi.useRealTimers();
    vi.restoreAllMocks();
});

describe('the retry ladder', () => {
    it('doubles while the page is running, up to the ceiling', () => {
        start();
        climbToCeiling();
        expect(waits).toEqual([1000, 2000, 4000, 8000, 16000, RECONNECT_MAX_DELAY_MS]);
    });

    it('starts over when a retry lands after a gap this page was not running through', () => {
        start();
        climbToCeiling();
        const before = sockets();

        // No browser event fires for a machine that slept with this tab still visible: the clock is
        // the only evidence. The wait the ladder was asking for has been served anyway, and the
        // attempts behind the gap went to a server the page could not have reached.
        suspendFor(8 * 60 * 60 * 1000);
        expect(sockets()).toBe(before + 1); // the wake-up attempt went out at once

        last().close(); // and the server is still not there
        expect(waits[waits.length - 1]).toBe(1000); // next second, not the ceiling again
    });

    it('keeps climbing when a retry is late by less than a suspension gap', () => {
        start();
        last().close();
        expect(waits[waits.length - 1]).toBe(1000);
        suspendFor(SUSPENDED_GAP_MS - 5000); // a busy main thread, not a sleep
        last().close();
        expect(waits[waits.length - 1]).toBe(2000);
    });
});

describe('an attempt asked for by evidence', () => {
    it('replaces the wait it interrupts, and starts the ladder over', () => {
        start();
        climbToCeiling();
        const before = sockets();

        stream!.reconnectNow(); // the window came back, or a person clicked the banner
        expect(sockets()).toBe(before + 1); // attempted now, not after the countdown

        last().close();
        expect(waits[waits.length - 1]).toBe(1000);
    });

    it('leaves no second attempt scheduled behind it', () => {
        start();
        last().close(); // a retry is now scheduled
        stream!.reconnectNow();
        const after = sockets();
        vi.advanceTimersByTime(10_000); // the superseded timer must not open another socket
        expect(sockets()).toBe(after);
    });

    it('does not pile a second attempt onto one already under way', () => {
        start(); // CONNECTING, nothing has answered yet
        const before = sockets();
        stream!.reconnectNow();
        stream!.reconnectNow();
        expect(sockets()).toBe(before);
    });

    it('replaces an attempt that never completed its handshake', () => {
        const socket = start();
        vi.advanceTimersByTime(WAKE_FRAME_GAP_MS + 1000);

        stream!.reconnectNow();
        expect(socket.closed).toBe(true);
        expect(sockets()).toBe(2);
    });

    it('counts an unanswered attempt as the stream speaking', () => {
        start();
        last().close();
        vi.advanceTimersByTime(20_000); // the retry went out at 1s and has not answered
        expect(stream!.msSinceFrame()).toBe(19_000); // a wake signal during an attempt has nothing to add
    });

    it('leaves a socket that is still framed alone', () => {
        const socket = start();
        socket.open();
        vi.advanceTimersByTime(5000);
        socket.frame();
        stream!.reconnectNow();
        expect(sockets()).toBe(1);
        expect(socket.closed).toBe(false);
    });

    it('drops a socket that only looks open, and reconnects immediately', () => {
        const socket = start();
        socket.open();
        vi.advanceTimersByTime(WAKE_FRAME_GAP_MS + 1000);
        expect(socket.closed).toBe(false); // silence alone is not yet the end of the connection

        stream!.reconnectNow();
        expect(socket.closed).toBe(true);
        expect(sockets()).toBe(2);
    });
});

describe('the heartbeat and a suspended page', () => {
    it('retires a first attempt that never completes, and hands over to the ladder', () => {
        const socket = start();
        vi.advanceTimersByTime(STALE_THRESHOLD_MS + HEARTBEAT_INTERVAL_MS);

        expect(socket.closed).toBe(true);
        expect(waits[waits.length - 1]).toBe(1000); // the ladder takes over
    });

    it('replaces a socket that went quiet while the page was not running', () => {
        const socket = start();
        socket.open();

        // Resuming: the first tick lands far too late, and the peer has said nothing since before
        // the gap. That much silence is not the 60 s staleness floor, but a page that was stopped
        // for this long has had no way to learn the socket died, so it is replaced now.
        suspendFor(SUSPENDED_GAP_MS * 2);
        expect(socket.closed).toBe(true);

        vi.advanceTimersByTime(1000); // replaced at the bottom of the ladder, not the top
        expect(sockets()).toBe(2);
    });

    it('holds a live socket across ordinary ticks', () => {
        const socket = start();
        socket.open();
        for (let i = 0; i < 3; i++) {
            vi.advanceTimersByTime(HEARTBEAT_INTERVAL_MS - 5000);
            socket.frame(); // the server's keepalive frame
            vi.advanceTimersByTime(5000);
        }
        expect(socket.closed).toBe(false);
        expect(sockets()).toBe(1);
    });

    it('applies the staleness floor, not the wake gap, to a page that kept running', () => {
        const socket = start();
        socket.open();
        vi.advanceTimersByTime(15_000);
        socket.frame();
        // Silence between the wake gap and the floor at the next tick: only a suspension or a
        // return signal may read a missed keepalive interval as a dead peer, or a running page
        // would trade a live socket for a handshake every half minute.
        vi.advanceTimersByTime(45_000);
        expect(socket.closed).toBe(false);
    });
});
