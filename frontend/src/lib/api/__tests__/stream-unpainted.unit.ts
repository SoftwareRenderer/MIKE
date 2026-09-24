/** A window that never hid proves it went unread only by frames it missed, so the keepalive — which
 * arrives every 27 s whatever the task is doing — must never count as one. */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createAppStream, type AppStream } from '../stream';

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
    close() { this.readyState = FakeSocket.CLOSED; this.onclose?.(); }
    open() { this.readyState = FakeSocket.OPEN; this.onopen?.(); }
    receive(data: string) { this.onmessage?.({ data }); }
}

const KEEPALIVE = '{"type":"ping"}';
const TASK_FRAME = '{"type":"task_event","task_id":"t1","event":"tool_result","data":{}}';

let stream: AppStream | null = null;
let focused = true;
let realWS: any;
let realHasFocus: () => boolean;

function start(): FakeSocket {
    stream = createAppStream({});
    const ws = FakeSocket.instances[FakeSocket.instances.length - 1];
    ws.open();
    return ws;
}

beforeEach(() => {
    FakeSocket.instances = [];
    stream = null;
    focused = false;
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-24T12:00:00Z'));
    realWS = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = FakeSocket;
    realHasFocus = document.hasFocus.bind(document);
    document.hasFocus = () => focused;
});
afterEach(() => {
    stream?.close();
    (globalThis as any).WebSocket = realWS;
    document.hasFocus = realHasFocus;
    vi.useRealTimers();
});

describe('msSinceUnpainted', () => {
    it('answers nothing until a frame has gone unread', () => {
        start();
        expect(stream!.msSinceUnpainted()).toBeNull();
        vi.advanceTimersByTime(60_000);
        expect(stream!.msSinceUnpainted()).toBeNull();
    });

    it('marks content that arrived while the window was not focused', () => {
        const ws = start();
        ws.receive(TASK_FRAME);
        expect(stream!.msSinceUnpainted()).toBe(0);
        vi.advanceTimersByTime(4_000);
        expect(stream!.msSinceUnpainted()).toBe(4_000);
    });

    it('counts the newest unread frame, not the oldest', () => {
        const ws = start();
        ws.receive(TASK_FRAME);
        vi.advanceTimersByTime(10_000);
        ws.receive('{"type":"board_refresh"}');
        expect(stream!.msSinceUnpainted()).toBe(0);
    });

    it('never counts the keepalive, which is what makes a live stream a live stream', () => {
        const ws = start();
        for (let round = 0; round < 8; round++) {
            vi.advanceTimersByTime(27_000);
            ws.receive(KEEPALIVE);
        }
        expect(stream!.msSinceUnpainted()).toBeNull();
    });

    it('never counts content a focused window was there to paint', () => {
        focused = true;
        const ws = start();
        ws.receive(TASK_FRAME);
        ws.receive('{"type":"task_updated","task":{"id":"t1"}}');
        expect(stream!.msSinceUnpainted()).toBeNull();
    });

    it('drops a frame it could not read rather than calling it unread content', () => {
        const ws = start();
        ws.receive('not json');
        ws.receive('null');
        expect(stream!.msSinceUnpainted()).toBeNull();
    });

    it('keeps unread content across a reconnect, which cannot repaint it either', () => {
        const ws = start();
        ws.receive(TASK_FRAME);
        ws.close();
        expect(stream!.msSinceUnpainted()).toBe(0);
    });
});
