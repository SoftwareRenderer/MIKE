/**
 * A WebSocket frame that is not JSON is dropped, not thrown.
 *
 * Both sockets used to `JSON.parse` inside `onmessage`, where a throw disappears into
 * the browser's event loop: the frame is lost, the socket stays open, and the app
 * believes it is current. These cases hold the three things that matter — the frame is
 * discarded, the connection survives it, and the NEXT frame still works — because a
 * guard that closed the socket instead would trade a lost frame for a lost session.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { parseStreamFrame } from '../helpers';
import { createTerminal } from '../terminal';
import { createAppStream } from '../stream';

/** The minimum `WebSocket` the two clients touch, recorded so a test can talk back. */
class FakeSocket {
    static instances: FakeSocket[] = [];
    static OPEN = 1;
    readyState = 1;
    onopen: (() => void) | null = null;
    onmessage: ((e: { data: string }) => void) | null = null;
    onclose: (() => void) | null = null;
    onerror: ((e: unknown) => void) | null = null;
    sent: string[] = [];
    closed = false;

    constructor(public url: string) { FakeSocket.instances.push(this); }
    send(data: string) { this.sent.push(data); }
    close() { this.closed = true; this.readyState = 3; this.onclose?.(); }
    receive(data: string) { this.onmessage?.({ data }); }
}

function fakeTerminal() {
    return {
        cols: 80,
        rows: 24,
        written: [] as string[],
        write(s: string) { this.written.push(s); },
        onData() { return { dispose() {} }; },
        onResize() { return { dispose() {} }; },
    };
}

let realWS: any;
beforeEach(() => {
    FakeSocket.instances = [];
    // The dropped-frame warning is the subject of its own case; keep it out of the
    // output of every other one.
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    realWS = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = FakeSocket;
});
afterEach(() => {
    (globalThis as any).WebSocket = realWS;
    vi.restoreAllMocks();
});

describe('parseStreamFrame', () => {
    it('returns the parsed message', () => {
        expect(parseStreamFrame('{"type":"ping"}', 'test')).toEqual({ type: 'ping' });
    });

    it.each([
        ['truncated JSON', '{"type":"task_ev'],
        ['plain text', 'pong'],
        ['empty', ''],
        ['JSON null', 'null'],
        ['a bare number', '42'],
        ['a bare string', '"ready"'],
        ['undefined', undefined],
    ])('drops %s', (_name, data) => {
        expect(parseStreamFrame(data, 'test')).toBeNull();
    });

    it('reports a bad frame once per socket name, not on every frame', () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        // The latch is keyed by socket name and never re-arms, so each case reports on a name
        // no earlier case used - that is what keeps both warnings observable here.
        parseStreamFrame('nonsense', 'socket-a');
        parseStreamFrame('more nonsense', 'socket-a');
        expect(warn).toHaveBeenCalledTimes(1);
        expect(warn.mock.calls[0][0]).toContain('[socket-a]');
    });

    it('never echoes the payload into the log', () => {
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        parseStreamFrame('{"secret":"hunter2"', 'socket-b');
        expect(warn).toHaveBeenCalledTimes(1);
        expect(warn.mock.calls.map(c => String(c[0])).join(' ')).not.toContain('hunter2');
    });
});

describe('terminal socket', () => {
    it('drops a bad frame, keeps the shell, and honours the next one', () => {
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        const term = fakeTerminal();
        createTerminal('p1', 't1', term as any);
        const ws = FakeSocket.instances[0];

        expect(() => ws.receive('not json at all')).not.toThrow();
        expect(ws.closed).toBe(false);

        ws.receive(JSON.stringify({ type: 'output', data: 'hello from the shell' }));
        expect(term.written.join('')).toContain('hello from the shell');
    });

    it('ignores a well-formed frame with no recognised type without throwing', () => {
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        const term = fakeTerminal();
        createTerminal('p1', 't1', term as any);
        const ws = FakeSocket.instances[0];
        expect(() => ws.receive(JSON.stringify({ type: 'unknown' }))).not.toThrow();
        expect(ws.closed).toBe(false);
    });
});

describe('app stream socket', () => {
    it('drops a bad frame, keeps the stream, and dispatches the next event', () => {
        vi.spyOn(console, 'warn').mockImplementation(() => {});
        const events: Array<[string, unknown]> = [];
        const stream = createAppStream({
            onTaskEvent: (taskId, event) => events.push([taskId, event]),
        });
        const ws = FakeSocket.instances[0];

        expect(() => ws.receive('{"type":')).not.toThrow();
        expect(ws.closed).toBe(false);

        ws.receive(JSON.stringify({ type: 'task_event', task_id: 'frost-amber', event: 'complete', data: {} }));
        expect(events).toEqual([['frost-amber', { type: 'complete', data: {} }]]);
        stream.close();
    });
});
