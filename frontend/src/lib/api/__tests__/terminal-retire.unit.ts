/**
 * Only the newest socket for a shell speaks, and one shell exit is reported once.
 *
 * The panel's stale-connection path closes a socket that is open but dead and opens another
 * for the same shell; the old socket's `close` arrives afterwards.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createProjectTerminal } from '../terminal';

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

    constructor(public url: string) { FakeSocket.instances.push(this); }
    send() {}
    close() {
        this.readyState = FakeSocket.CLOSED;
        this.onclose?.();
    }
}

/** The parts of an `@xterm/xterm` Terminal that `createTerminalWS` touches. */
function fakeTerm() {
    return {
        cols: 80,
        rows: 24,
        title: '',
        writes: [] as string[],
        write(s: string) { this.writes.push(s); },
        onData() { return { dispose() {} }; },
        onResize() { return { dispose() {} }; },
    };
}

let realWS: unknown;
beforeEach(() => {
    FakeSocket.instances = [];
    realWS = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = FakeSocket;
});
afterEach(() => {
    (globalThis as any).WebSocket = realWS;
});

function last(): FakeSocket { return FakeSocket.instances[FakeSocket.instances.length - 1]; }

describe('a retired terminal socket', () => {
    it('does not report the shell gone after the reconnect that replaced it', () => {
        const term = fakeTerm();
        let shellGone = 0;
        createProjectTerminal('proj-1', term as any, () => { shellGone++; });
        const first = last();

        createProjectTerminal('proj-1', term as any, () => { shellGone++; });
        const second = last();
        expect(second.url).toBe(first.url);

        first.close();
        expect(shellGone).toBe(0);

        second.close();
        expect(shellGone).toBe(1);
    });

    it('reports the shell gone once when the shell-closed frame and the close both arrive', () => {
        const term = fakeTerm();
        let shellGone = 0;
        createProjectTerminal('proj-3', term as any, () => { shellGone++; });
        const sock = last();

        // The `closed` frame and the close that follows are one exit.
        sock.onmessage?.({ data: JSON.stringify({ type: 'closed' }) });
        sock.close();
        expect(shellGone).toBe(1);
    });

    it('reports the shell gone when a shell-less connection is closed by hand', () => {
        const term = fakeTerm();
        let shellGone = 0;
        createProjectTerminal('proj-2', term as any, () => { shellGone++; });

        last().close();
        expect(shellGone).toBe(1);

        // A second close of the same socket is not a second shell going away.
        last().close();
        expect(shellGone).toBe(1);
    });
});
