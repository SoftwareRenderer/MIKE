/**
 * The Done column's task figure arrives on a WebSocket frame, and this dispatcher decides whether it reaches
 * the store. A test that writes to the store directly would not notice it being dropped, so these send a
 * real frame down the socket.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createAppStream, type AppStream } from '../stream';
import { storageTasksSentence } from '$lib/utils/storage-usage';
import type { StorageUsage } from '$lib/types';

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
    receive(frame: Record<string, any>) { this.onmessage?.({ data: JSON.stringify(frame) }); }
}

let stream: AppStream | null = null;
let realWS: any;

/** Opens a stream and collects every reading it receives. */
function openStream(): { socket: FakeSocket; received: StorageUsage[] } {
    const received: StorageUsage[] = [];
    stream = createAppStream({ onStorageUsage: (usage) => { received.push(usage); } });
    const socket = FakeSocket.instances[FakeSocket.instances.length - 1];
    socket.open();
    return { socket, received };
}

beforeEach(() => {
    FakeSocket.instances = [];
    stream = null;
    realWS = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = FakeSocket;
});

afterEach(() => {
    stream?.close();
    stream = null;
    (globalThis as any).WebSocket = realWS;
});

describe('the task figure on the storage_usage frame', () => {
    it('reaches the consumer that draws the tooltip line', () => {
        const { socket, received } = openStream();
        socket.receive({ type: 'storage_usage', total_bytes: 1024, avail_bytes: 594, tasks_bytes: 430, tasks_count: 3 });

        expect(received[0].tasks_bytes).toBe(430);
        expect(received[0].tasks_count).toBe(3);
        expect(storageTasksSentence(received[0])).toBeTruthy();
    });

    it('stays absent on a reading taken before the walk finished, so no line is drawn for it', () => {
        const { socket, received } = openStream();
        socket.receive({ type: 'storage_usage', total_bytes: 1024, avail_bytes: 594 });

        expect('tasks_bytes' in received[0]).toBe(false);
        expect('tasks_count' in received[0]).toBe(false);
        expect(storageTasksSentence(received[0])).toBeNull();
    });

    it('rides with the filesystem reading rather than replacing it', () => {
        const { socket, received } = openStream();
        socket.receive({ type: 'storage_usage', total_bytes: 1024, avail_bytes: 594, tasks_bytes: 430, tasks_count: 3 });

        expect(received[0]).toMatchObject({ total_bytes: 1024, avail_bytes: 594 });
    });
});
